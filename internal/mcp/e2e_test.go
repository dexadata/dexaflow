//go:build e2e

// Package mcp_test end-to-end exercise: it builds the real leoflow-mcp binary,
// launches it the way an MCP client (Claude Desktop/Code) would — as a stdio
// subprocess over the actual MCP protocol — and drives its tools and resources
// against a seeded control plane. This is the "realistic agent against the
// deployed binary" gate: it catches wiring/protocol/transport bugs that the
// in-process handler tests (which call NewServer directly) cannot.
//
// Opt-in (it compiles a binary and spawns it): `go test -tags e2e ./internal/mcp/`
// or `make test-mcp-e2e`.
package mcp_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// seededControlPlane emulates the /api/v2 surface for one DAG "etl" whose run
// "r1" failed at task "load" (try 2) with a recognizable error in its log.
func seededControlPlane(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v2/dags":
			_, _ = w.Write([]byte(`{"dags":[{"dag_id":"etl","is_paused":false}],"total_entries":1}`))
		case "/api/v2/dags/etl/dagRuns/r1":
			_, _ = w.Write([]byte(`{"dag_id":"etl","dag_run_id":"r1","state":"failed","run_type":"manual"}`))
		case "/api/v2/dags/etl/dagRuns/r1/taskInstances":
			_, _ = w.Write([]byte(`{"task_instances":[` +
				`{"task_id":"extract","state":"success","try_number":1},` +
				`{"task_id":"load","state":"failed","try_number":2,"duration":3.5}],"total_entries":2}`))
		case "/api/v2/dags/etl/dagRuns/r1/taskInstances/extract/logs/1":
			// A clean log for the successful task; a run-wide search must scan it
			// too, but it carries no "boom".
			_, _ = w.Write([]byte("extract connecting\nextract done\n"))
		case "/api/v2/dags/etl/dagRuns/r1/taskInstances/load/logs/2":
			_, _ = w.Write([]byte("connecting\nValueError: boom in load\ndone\n"))
		case "/api/v2/dags/etl/spec":
			// A dbt graph so diagnose_run can surface load's models + downstream.
			_, _ = w.Write([]byte(`{"tasks":[` +
				`{"task_id":"extract","entrypoint":"dbt seed --select extract"},` +
				`{"task_id":"load","depends_on":["extract"],"entrypoint":"dbt build --select load_a load_b --project-dir /p"},` +
				`{"task_id":"report","depends_on":["load"],"entrypoint":"dbt run --select report"}]}`))
		default:
			t.Errorf("control plane got unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func buildMCPBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "dexaflow-mcp")
	out, err := exec.CommandContext(t.Context(), "go", "build", "-o", bin, "github.com/dexadata/dexaflow/cmd/dexaflow-mcp").CombinedOutput()
	if err != nil {
		t.Fatalf("building leoflow-mcp: %v\n%s", err, out)
	}
	return bin
}

// freePort asks the OS for an unused TCP port and hands it back. There is an
// inherent bind race, but the window is tiny and this is standard for spawning a
// child that needs a known address.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().String()
}

// waitHealthy polls the binary's /healthz until it answers or the deadline hits.
func waitHealthy(t *testing.T, ctx context.Context, addr string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/healthz", nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("leoflow-mcp http transport never became healthy")
}

type e2eAuthRT struct {
	token string
	base  http.RoundTripper
}

func (a e2eAuthRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("Authorization", "Bearer "+a.token)
	return a.base.RoundTrip(r)
}

func textOf(contents []mcpsdk.Content) string {
	var b strings.Builder
	for _, c := range contents {
		if tc, ok := c.(*mcpsdk.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestMCPBinaryEndToEnd(t *testing.T) {
	cp := seededControlPlane(t)
	defer cp.Close()
	bin := buildMCPBinary(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "--server", cp.URL)
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "e2e-client", Version: "0"}, nil)
	sess, err := client.Connect(ctx, &mcpsdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connecting to leoflow-mcp binary: %v", err)
	}
	defer func() { _ = sess.Close() }()

	// tools/list — the three read tools are discoverable over the real protocol.
	tools, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	want := map[string]bool{"list_dags": false, "diagnose_run": false, "search_logs": false}
	for _, tl := range tools.Tools {
		if _, ok := want[tl.Name]; ok {
			want[tl.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("tool %q not advertised by the binary", name)
		}
	}

	// diagnose_run — the highest-value flow: ask why r1 failed, get the failed
	// task and its log, end to end through the real binary.
	res, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      "diagnose_run",
		Arguments: map[string]any{"dag_id": "etl", "run_id": "r1"},
	})
	if err != nil {
		t.Fatalf("CallTool diagnose_run: %v", err)
	}
	if res.IsError {
		t.Fatalf("diagnose_run returned an error result: %s", textOf(res.Content))
	}
	got := textOf(res.Content)
	for _, want := range []string{
		`"run_state":"failed"`, `"task_id":"load"`, "ValueError: boom in load",
		"load_a", "load_b", // dbt models parsed from the fused --select
		"report", // downstream task blocked by load's failure
	} {
		if !strings.Contains(got, want) {
			t.Errorf("diagnose_run output missing %q; got: %s", want, got)
		}
	}

	// search_logs run-wide — omit task_id so the binary enumerates the run's
	// task instances and searches every task's log, tagging each match with its
	// task_id. Only "load" carries the boom, so the match must be tagged load.
	sr, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      "search_logs",
		Arguments: map[string]any{"dag_id": "etl", "run_id": "r1", "query": "boom"},
	})
	if err != nil {
		t.Fatalf("CallTool search_logs (run-wide): %v", err)
	}
	if sr.IsError {
		t.Fatalf("search_logs run-wide returned an error result: %s", textOf(sr.Content))
	}
	sgot := textOf(sr.Content)
	for _, want := range []string{`"task_id":"load"`, "ValueError: boom in load"} {
		if !strings.Contains(sgot, want) {
			t.Errorf("run-wide search_logs output missing %q; got: %s", want, sgot)
		}
	}

	// A resource read over the real protocol.
	rr, err := sess.ReadResource(ctx, &mcpsdk.ReadResourceParams{URI: "run://detail/etl/r1"})
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	if len(rr.Contents) == 0 || !strings.Contains(rr.Contents[0].Text, `"state":"failed"`) {
		t.Errorf("run://detail content missing state; got %+v", rr.Contents)
	}
}

// TestMCPBinaryHTTPTransport is the Pro-transport gate (ADR 0050 Phase 3): the
// real binary is launched with --transport http, a real MCP client connects over
// Streamable HTTP carrying its own bearer, and the token reaches the control
// plane on the resulting tool call — the whole per-request pass-through (D9)
// through the actual binary, not the in-process handler.
func TestMCPBinaryHTTPTransport(t *testing.T) {
	var (
		mu      sync.Mutex
		gotAuth string
	)
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/dags" {
			mu.Lock()
			gotAuth = r.Header.Get("Authorization")
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"dags":[{"dag_id":"etl","is_paused":false}],"total_entries":1}`))
	}))
	defer cp.Close()
	bin := buildMCPBinary(t)
	addr := freePort(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "--transport", "http", "--listen", addr, "--server", cp.URL)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting http binary: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	waitHealthy(t, ctx, addr)

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "e2e-http-client", Version: "0"}, nil)
	transport := &mcpsdk.StreamableClientTransport{
		Endpoint:   "http://" + addr + "/mcp",
		HTTPClient: &http.Client{Transport: e2eAuthRT{token: "usertok", base: http.DefaultTransport}},
	}
	sess, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("connecting over http: %v", err)
	}
	defer func() { _ = sess.Close() }()

	res, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{Name: "list_dags"})
	if err != nil {
		t.Fatalf("CallTool list_dags over http: %v", err)
	}
	if res.IsError {
		t.Fatalf("list_dags returned an error result: %s", textOf(res.Content))
	}
	mu.Lock()
	seen := gotAuth
	mu.Unlock()
	if seen != "Bearer usertok" {
		t.Errorf("caller token did not reach the control plane via the http binary; saw %q", seen)
	}
}
