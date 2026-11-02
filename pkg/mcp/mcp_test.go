package mcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	apiclient "github.com/dexadata/dexaflow/pkg/client"
	"github.com/dexadata/dexaflow/pkg/mcp"
)

// controlPlane answers /api/v2/dags and records the Authorization header of
// each call, so a test can tell whose identity a tool used.
type controlPlane struct {
	*httptest.Server
	mu    sync.Mutex
	auths []string
}

func newControlPlane(t *testing.T) *controlPlane {
	t.Helper()
	cp := &controlPlane{}
	cp.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cp.mu.Lock()
		cp.auths = append(cp.auths, r.Header.Get("Authorization"))
		cp.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"dags":[{"dag_id":"etl","is_paused":false}],"total_entries":1}`))
	}))
	t.Cleanup(cp.Close)
	return cp
}

func (cp *controlPlane) seen() []string {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	return slices.Clone(cp.auths)
}

type countIn struct{}

type countOut struct {
	Count int `json:"count"`
}

// addCountTool registers a site tool that counts DAGs through the client the
// server hands it.
func addCountTool(s *mcp.Server) {
	mcp.AddTool(s, &mcpsdk.Tool{Name: "count_dags", Description: "Count the DAGs."},
		func(ctx context.Context, api *apiclient.ClientWithResponses, _ *mcpsdk.CallToolRequest, _ countIn) (*mcpsdk.CallToolResult, countOut, error) {
			resp, err := api.ListDagsWithResponse(ctx, nil)
			if err != nil {
				return nil, countOut{}, err
			}
			if resp.JSON200 == nil {
				return nil, countOut{}, nil
			}
			var n int
			if resp.JSON200.Dags != nil {
				n = len(*resp.JSON200.Dags)
			}
			return nil, countOut{Count: n}, nil
		})
}

type bearerRT struct{ token string }

func (b bearerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return http.DefaultTransport.RoundTrip(r)
}

// connectHTTP serves s over the Streamable HTTP transport and connects a
// client that sends token as its bearer ("" sends none).
func connectHTTP(t *testing.T, s *mcp.Server, token string) *mcpsdk.ClientSession {
	t.Helper()
	srv := httptest.NewServer(mcp.HTTPHandler(s, mcp.ProtectedResource{}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "0"}, nil)
	sess, err := client.Connect(ctx, &mcpsdk.StreamableClientTransport{
		Endpoint: srv.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerRT{token: token}},
	}, nil)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

// connectStdio connects a client to s over in-memory transports, the shape of
// the stdio transport: one local caller.
func connectStdio(t *testing.T, s *mcp.Server) *mcpsdk.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	ct, st := mcpsdk.NewInMemoryTransports()
	ss, err := s.SDK().Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "0"}, nil)
	sess, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

func newServer(t *testing.T, cp *controlPlane, token string, transport mcp.Transport, opts ...mcp.Option) *mcp.Server {
	t.Helper()
	api, err := apiclient.New(cp.URL, token)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return mcp.NewServer(api, cp.URL, "1.2.3", transport, opts...)
}

// TestAddToolCallsTheControlPlaneAsTheCaller: on the HTTP transport a site
// tool reaches the control plane with the bearer of the request that called
// it, never with a process identity, and a request without one is refused.
func TestAddToolCallsTheControlPlaneAsTheCaller(t *testing.T) {
	cp := newControlPlane(t)
	s := newServer(t, cp, "", mcp.TransportHTTP)
	addCountTool(s)

	sess := connectHTTP(t, s, "caller-token")
	res, err := sess.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "count_dags"})
	if err != nil || res.IsError {
		t.Fatalf("count_dags: err=%v result=%+v", err, res)
	}
	var out countOut
	raw, _ := json.Marshal(res.StructuredContent)
	if err = json.Unmarshal(raw, &out); err != nil || out.Count != 1 {
		t.Fatalf("count_dags = %s, want count 1", raw)
	}
	if got := cp.seen(); len(got) != 1 || got[0] != "Bearer caller-token" {
		t.Fatalf("control plane saw Authorization %q, want one call as Bearer caller-token", got)
	}

	anon := connectHTTP(t, s, "")
	res, err = anon.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "count_dags"})
	if err == nil && !res.IsError {
		t.Fatal("count_dags without a bearer succeeded on the http transport")
	}
	if got := cp.seen(); len(got) != 1 {
		t.Fatalf("a bearer-less call reached the control plane: %q", got)
	}
}

// TestAddToolOnStdioUsesTheProcessClient: on stdio the one local caller is
// the process token.
func TestAddToolOnStdioUsesTheProcessClient(t *testing.T) {
	cp := newControlPlane(t)
	s := newServer(t, cp, "process-token", mcp.TransportStdio)
	addCountTool(s)

	sess := connectStdio(t, s)
	res, err := sess.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "count_dags"})
	if err != nil || res.IsError {
		t.Fatalf("count_dags: err=%v result=%+v", err, res)
	}
	if got := cp.seen(); len(got) != 1 || got[0] != "Bearer process-token" {
		t.Fatalf("control plane saw Authorization %q, want Bearer process-token", got)
	}
}

type capabilities struct {
	Server     string          `json:"server"`
	Version    string          `json:"version"`
	Transport  string          `json:"transport"`
	ToolGroups map[string]bool `json:"tool_groups"`
	Tools      []string        `json:"tools"`
}

func readCapabilities(t *testing.T, sess *mcpsdk.ClientSession) capabilities {
	t.Helper()
	res, err := sess.ReadResource(context.Background(), &mcpsdk.ReadResourceParams{URI: mcp.CapabilitiesURI})
	if err != nil {
		t.Fatalf("reading %s: %v", mcp.CapabilitiesURI, err)
	}
	if len(res.Contents) != 1 {
		t.Fatalf("%s returned %d contents, want 1", mcp.CapabilitiesURI, len(res.Contents))
	}
	var c capabilities
	if err := json.Unmarshal([]byte(res.Contents[0].Text), &c); err != nil {
		t.Fatalf("decoding %q: %v", res.Contents[0].Text, err)
	}
	return c
}

// TestCapabilitiesResource: the resource reports the version, the transport,
// which optional tool groups are on, and every tool, site tools included.
func TestCapabilitiesResource(t *testing.T) {
	cp := newControlPlane(t)

	plain := newServer(t, cp, "tok", mcp.TransportStdio)
	addCountTool(plain)
	c := readCapabilities(t, connectStdio(t, plain))
	if c.Server != "dexaflow" || c.Version != "1.2.3" || c.Transport != "stdio" {
		t.Fatalf("capabilities = %+v, want dexaflow 1.2.3 over stdio", c)
	}
	if !c.ToolGroups["read"] || c.ToolGroups["run_control"] {
		t.Fatalf("tool_groups = %v, want read on and run_control off", c.ToolGroups)
	}
	for _, want := range []string{"list_dags", "diagnose_run", "search_logs", "count_dags"} {
		if !slices.Contains(c.Tools, want) {
			t.Fatalf("tools = %v, missing %s", c.Tools, want)
		}
	}
	if slices.Contains(c.Tools, "trigger_run") {
		t.Fatalf("tools = %v lists trigger_run with run control off", c.Tools)
	}

	ctl := newServer(t, cp, "tok", mcp.TransportStdio, mcp.WithRunControl(nil))
	withCtl := readCapabilities(t, connectStdio(t, ctl))
	if !withCtl.ToolGroups["run_control"] || !slices.Contains(withCtl.Tools, "trigger_run") {
		t.Fatalf("with run control: tool_groups=%v tools=%v", withCtl.ToolGroups, withCtl.Tools)
	}

	listed, err := connectStdio(t, plain).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	names := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	got := slices.Clone(c.Tools)
	slices.Sort(got)
	if strings.Join(names, ",") != strings.Join(got, ",") {
		t.Fatalf("capabilities tools %v differ from tools/list %v", got, names)
	}
}
