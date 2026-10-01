package mcp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

// TestDiagnoseRun composes run + task-instances + logs into one diagnosis: it
// reports the run state, isolates the failed task, and returns a truncated,
// sanitized tail of that task's log (ADR 0050 R19). It must not treat a
// successful task as failed, and must surface the failing task's log.
func TestDiagnoseRun(t *testing.T) {
	h := testHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v2/dags/etl/dagRuns/r1":
			_, _ = io.WriteString(w, `{"dag_id":"etl","dag_run_id":"r1","state":"failed"}`)
		case "/api/v2/dags/etl/dagRuns/r1/taskInstances":
			_, _ = io.WriteString(w, `{"task_instances":[`+
				`{"task_id":"extract","state":"success","try_number":1},`+
				`{"task_id":"load","state":"failed","try_number":2,"duration":3.5}],"total_entries":2}`)
		case "/api/v2/dags/etl/dagRuns/r1/taskInstances/load/logs/2":
			_, _ = io.WriteString(w, "setup\nrunning query\nTraceback (most recent call last)\nValueError: boom\x1b[0m\n")
		case "/api/v2/dags/etl/spec": // diagnose_run enriches downstream/models best-effort
			_, _ = io.WriteString(w, `{"tasks":[{"task_id":"extract"},{"task_id":"load","depends_on":["extract"]}]}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	_, out, err := h.diagnoseRun(context.Background(), nil, diagnoseRunInput{DagID: "etl", RunID: "r1"})
	if err != nil {
		t.Fatalf("diagnoseRun: %v", err)
	}
	if out.RunState != "failed" {
		t.Errorf("run_state = %q, want failed", out.RunState)
	}
	if out.TotalTasks != 2 {
		t.Errorf("total_tasks = %d, want 2", out.TotalTasks)
	}
	if len(out.FailedTasks) != 1 || out.FailedTasks[0].TaskID != "load" {
		t.Fatalf("failed_tasks = %+v, want exactly [load]", out.FailedTasks)
	}
	ft := out.FailedTasks[0]
	if ft.TryNumber != 2 {
		t.Errorf("try_number = %d, want 2", ft.TryNumber)
	}
	if !strings.Contains(ft.LogTail, "ValueError: boom") {
		t.Errorf("log_tail missing the failure line: %q", ft.LogTail)
	}
	if strings.Contains(ft.LogTail, "\x1b") {
		t.Errorf("log_tail must be sanitized of control/ANSI bytes: %q", ft.LogTail)
	}
}

// TestDiagnoseRunDbtAware: for a failed task, diagnose_run additionally surfaces
// the downstream tasks it blocks (from the spec's depends_on graph) and, for a dbt
// task, the models it runs (parsed from its --select). Best-effort from the spec.
func TestDiagnoseRunDbtAware(t *testing.T) {
	h := testHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v2/dags/etl/dagRuns/r1":
			_, _ = io.WriteString(w, `{"dag_id":"etl","dag_run_id":"r1","state":"failed"}`)
		case "/api/v2/dags/etl/dagRuns/r1/taskInstances":
			_, _ = io.WriteString(w, `{"task_instances":[`+
				`{"task_id":"raw","state":"success","try_number":1},`+
				`{"task_id":"stg","state":"failed","try_number":1},`+
				`{"task_id":"mart","state":"upstream_failed","try_number":0}],"total_entries":3}`)
		case "/api/v2/dags/etl/dagRuns/r1/taskInstances/stg/logs/1":
			_, _ = io.WriteString(w, "boom\n")
		case "/api/v2/dags/etl/spec":
			_, _ = io.WriteString(w, `{"tasks":[`+
				`{"task_id":"raw","entrypoint":"dbt seed --select raw"},`+
				`{"task_id":"stg","depends_on":["raw"],"entrypoint":"dbt build --select stg_a stg_b --project-dir /x"},`+
				`{"task_id":"mart","depends_on":["stg"],"entrypoint":"dbt run --select mart"}]}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	})

	_, out, err := h.diagnoseRun(context.Background(), nil, diagnoseRunInput{DagID: "etl", RunID: "r1"})
	if err != nil {
		t.Fatalf("diagnoseRun: %v", err)
	}
	var stg *failedTask
	for i := range out.FailedTasks {
		if out.FailedTasks[i].TaskID == "stg" {
			stg = &out.FailedTasks[i]
		}
	}
	if stg == nil {
		t.Fatalf("stg not in failed tasks: %+v", out.FailedTasks)
	}
	if len(stg.DownstreamBlocked) != 1 || stg.DownstreamBlocked[0] != "mart" {
		t.Errorf("stg downstream_blocked = %v, want [mart]", stg.DownstreamBlocked)
	}
	if len(stg.Models) != 2 || stg.Models[0] != "stg_a" || stg.Models[1] != "stg_b" {
		t.Errorf("stg models = %v, want [stg_a stg_b]", stg.Models)
	}
}

// TestSearchLogs finds case-insensitive substring matches in one task's log,
// reports 1-based line numbers, sanitizes control/ANSI bytes, and returns the
// total found even when the returned set is capped.
func TestSearchLogs(t *testing.T) {
	h := testHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/dags/etl/dagRuns/r1/taskInstances/load/logs/2" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_, _ = io.WriteString(w, "starting\nERROR: connection refused\nretrying\nerror: timeout\x1b[0m\ndone\n")
	})

	_, out, err := h.searchLogs(context.Background(), nil, searchLogsInput{
		DagID: "etl", RunID: "r1", TaskID: "load", TryNumber: 2, Query: "error",
	})
	if err != nil {
		t.Fatalf("searchLogs: %v", err)
	}
	if out.TotalMatches != 2 || len(out.Matches) != 2 {
		t.Fatalf("matches = %+v (total %d), want 2", out.Matches, out.TotalMatches)
	}
	if out.Matches[0].LineNumber != 2 || out.Matches[1].LineNumber != 4 {
		t.Errorf("line numbers = %d,%d, want 2,4", out.Matches[0].LineNumber, out.Matches[1].LineNumber)
	}
	if strings.Contains(out.Matches[1].Line, "\x1b") {
		t.Errorf("match line must be sanitized: %q", out.Matches[1].Line)
	}
	if out.Truncated {
		t.Error("should not be truncated with the default cap")
	}
}

// TestSearchLogsRunWide: with task_id OMITTED, search_logs enumerates the run's
// task instances and searches every task's log, tagging each match with the
// task_id it came from (and a 1-based line number). Matches are sanitized.
func TestSearchLogsRunWide(t *testing.T) {
	h := testHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v2/dags/etl/dagRuns/r1/taskInstances":
			_, _ = io.WriteString(w, `{"task_instances":[`+
				`{"task_id":"extract","state":"success","try_number":1},`+
				`{"task_id":"load","state":"failed","try_number":2}],"total_entries":2}`)
		case "/api/v2/dags/etl/dagRuns/r1/taskInstances/extract/logs/1":
			_, _ = io.WriteString(w, "connecting\nerror in extract\ndone\n")
		case "/api/v2/dags/etl/dagRuns/r1/taskInstances/load/logs/2":
			_, _ = io.WriteString(w, "load starting\nERROR: boom\x1b[0m\ndone\n")
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	})

	_, out, err := h.searchLogs(context.Background(), nil, searchLogsInput{
		DagID: "etl", RunID: "r1", Query: "error", // no TaskID -> run-wide
	})
	if err != nil {
		t.Fatalf("searchLogs run-wide: %v", err)
	}
	if out.TotalMatches != 2 || len(out.Matches) != 2 {
		t.Fatalf("matches = %+v (total %d), want 2 across the run", out.Matches, out.TotalMatches)
	}
	// Matches are tagged with the task they came from, in task-instance order.
	if out.Matches[0].TaskID != "extract" || out.Matches[0].LineNumber != 2 {
		t.Errorf("match[0] = %+v, want extract@line2", out.Matches[0])
	}
	if out.Matches[1].TaskID != "load" || out.Matches[1].LineNumber != 2 {
		t.Errorf("match[1] = %+v, want load@line2", out.Matches[1])
	}
	if out.Matches[1].TryNumber != 2 {
		t.Errorf("match[1] try_number = %d, want 2 (the searched attempt)", out.Matches[1].TryNumber)
	}
	if strings.Contains(out.Matches[1].Line, "\x1b") {
		t.Errorf("run-wide match line must be sanitized: %q", out.Matches[1].Line)
	}
	if out.Truncated {
		t.Error("should not be truncated with the default cap")
	}
}

// TestSearchLogsRunWideCaps: the cap on returned matches is applied across the
// WHOLE run, and the true total (over all tasks) is still reported so the agent
// knows there is more.
func TestSearchLogsRunWideCaps(t *testing.T) {
	h := testHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v2/dags/d/dagRuns/r/taskInstances":
			_, _ = io.WriteString(w, `{"task_instances":[`+
				`{"task_id":"a","state":"failed","try_number":1},`+
				`{"task_id":"b","state":"failed","try_number":1}],"total_entries":2}`)
		case "/api/v2/dags/d/dagRuns/r/taskInstances/a/logs/1":
			_, _ = io.WriteString(w, "error a1\nerror a2\n")
		case "/api/v2/dags/d/dagRuns/r/taskInstances/b/logs/1":
			_, _ = io.WriteString(w, "error b1\nerror b2\n")
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	})
	_, out, err := h.searchLogs(context.Background(), nil, searchLogsInput{
		DagID: "d", RunID: "r", Query: "error", MaxMatches: 2,
	})
	if err != nil {
		t.Fatalf("searchLogs run-wide: %v", err)
	}
	if len(out.Matches) != 2 || out.TotalMatches != 4 || !out.Truncated {
		t.Errorf("want 2 returned / 4 total / truncated, got %d / %d / %v", len(out.Matches), out.TotalMatches, out.Truncated)
	}
}

// TestSearchLogsRunWideToleratesMissingLog: in run-wide mode a task whose log
// can't be fetched (never ran, or the endpoint errors) is skipped rather than
// failing the whole search — the matches from the other tasks still come back.
func TestSearchLogsRunWideToleratesMissingLog(t *testing.T) {
	h := testHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v2/dags/d/dagRuns/r/taskInstances":
			_, _ = io.WriteString(w, `{"task_instances":[`+
				`{"task_id":"a","state":"failed","try_number":1},`+
				`{"task_id":"b","state":"upstream_failed","try_number":0}],"total_entries":2}`)
		case "/api/v2/dags/d/dagRuns/r/taskInstances/a/logs/1":
			_, _ = io.WriteString(w, "error a1\n")
		default:
			// b has try_number 0 (never ran); its log must never be requested.
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	_, out, err := h.searchLogs(context.Background(), nil, searchLogsInput{
		DagID: "d", RunID: "r", Query: "error",
	})
	if err != nil {
		t.Fatalf("searchLogs run-wide: %v", err)
	}
	if out.TotalMatches != 1 || len(out.Matches) != 1 || out.Matches[0].TaskID != "a" {
		t.Errorf("want 1 match tagged a, got %+v (total %d)", out.Matches, out.TotalMatches)
	}
}

// TestSearchLogsTruncates caps the returned matches but still reports the true
// total, so the agent knows there is more.
func TestSearchLogsTruncates(t *testing.T) {
	h := testHandlers(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "error a\nerror b\nerror c\n")
	})
	_, out, err := h.searchLogs(context.Background(), nil, searchLogsInput{
		DagID: "d", RunID: "r", TaskID: "t", Query: "error", MaxMatches: 1,
	})
	if err != nil {
		t.Fatalf("searchLogs: %v", err)
	}
	if len(out.Matches) != 1 || out.TotalMatches != 3 || !out.Truncated {
		t.Errorf("want 1 returned / 3 total / truncated, got %d / %d / %v", len(out.Matches), out.TotalMatches, out.Truncated)
	}
}

// TestDiagnoseRunCapsGiantLine: a single newline-free megabyte line must not
// slip into the agent context whole — line-count truncation doesn't catch it, so
// each line is length-capped (D10 "cap line length").
func TestDiagnoseRunCapsGiantLine(t *testing.T) {
	giant := strings.Repeat("A", 5_000_000)
	h := testHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v2/dags/etl/dagRuns/r1":
			_, _ = io.WriteString(w, `{"dag_id":"etl","dag_run_id":"r1","state":"failed"}`)
		case "/api/v2/dags/etl/dagRuns/r1/taskInstances":
			_, _ = io.WriteString(w, `{"task_instances":[{"task_id":"load","state":"failed","try_number":1}],"total_entries":1}`)
		case "/api/v2/dags/etl/dagRuns/r1/taskInstances/load/logs/1":
			_, _ = io.WriteString(w, giant) // one line, no newline
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	_, out, err := h.diagnoseRun(context.Background(), nil, diagnoseRunInput{DagID: "etl", RunID: "r1"})
	if err != nil {
		t.Fatalf("diagnoseRun: %v", err)
	}
	if len(out.FailedTasks) != 1 {
		t.Fatalf("want 1 failed task, got %d", len(out.FailedTasks))
	}
	if n := len([]rune(out.FailedTasks[0].LogTail)); n > maxLineRunes+64 {
		t.Errorf("log_tail not length-capped: %d runes (cap %d)", n, maxLineRunes)
	}
	if !strings.Contains(out.FailedTasks[0].LogTail, "line truncated") {
		t.Error("a truncated line should carry the truncation marker")
	}
}

// TestSearchLogsCapsGiantLine: a matching line that is enormous is length-capped
// in the returned match, not surfaced whole.
func TestSearchLogsCapsGiantLine(t *testing.T) {
	giant := "error " + strings.Repeat("B", 5_000_000)
	h := testHandlers(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, giant)
	})
	_, out, err := h.searchLogs(context.Background(), nil, searchLogsInput{
		DagID: "d", RunID: "r", TaskID: "t", Query: "error",
	})
	if err != nil {
		t.Fatalf("searchLogs: %v", err)
	}
	if len(out.Matches) != 1 {
		t.Fatalf("want 1 match, got %d", len(out.Matches))
	}
	if n := len([]rune(out.Matches[0].Line)); n > maxLineRunes+64 {
		t.Errorf("match line not length-capped: %d runes (cap %d)", n, maxLineRunes)
	}
}

// TestSearchLogsMissingArgs: the run locators and the query are required, but
// task_id is now optional (omitting it means a run-wide search, not an error).
func TestSearchLogsMissingArgs(t *testing.T) {
	h := &handlers{}
	if _, _, err := h.searchLogs(context.Background(), nil, searchLogsInput{DagID: "d", RunID: "r", TaskID: "t"}); err == nil {
		t.Error("expected an error when query is missing")
	}
	if _, _, err := h.searchLogs(context.Background(), nil, searchLogsInput{RunID: "r", Query: "q"}); err == nil {
		t.Error("expected an error when dag_id is missing")
	}
	if _, _, err := h.searchLogs(context.Background(), nil, searchLogsInput{DagID: "d", Query: "q"}); err == nil {
		t.Error("expected an error when run_id is missing")
	}
}

// TestDiagnoseRunMissingArgs: dag_id and run_id are required.
func TestDiagnoseRunMissingArgs(t *testing.T) {
	h := &handlers{}
	if _, _, err := h.diagnoseRun(context.Background(), nil, diagnoseRunInput{DagID: "etl"}); err == nil {
		t.Error("expected an error when run_id is missing")
	}
}

// TestNewServerRegistersListDags connects a client to the server over an
// in-memory transport and asserts list_dags is discoverable via tools/list —
// the tool has to be REGISTERED, not just implemented (a registered-but-invisible
// tool is the powerlab UX trap this guards against).
func TestNewServerRegistersListDags(t *testing.T) {
	ctx := context.Background()
	api, err := apiclient.New("http://control-plane.invalid", "")
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	srv := NewServer(api, "", "test", false)

	serverT, clientT := mcpsdk.NewInMemoryTransports()
	_, err = srv.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = sess.Close() }()

	res, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	found := false
	for _, tool := range res.Tools {
		if tool.Name == "list_dags" {
			found = true
		}
	}
	if !found {
		t.Errorf("list_dags not registered/discoverable via tools/list; got %d tools", len(res.Tools))
	}
}

// TestNewServerAdvertisesResources connects a client and asserts every resource
// is discoverable via resources/list (static) and resources/templates/list
// (templated). This is the same "registered-but-invisible" guard the tools test
// applies — a resource clients can't enumerate is a resource they won't use.
func TestNewServerAdvertisesResources(t *testing.T) {
	ctx := context.Background()
	api, err := apiclient.New("http://control-plane.invalid", "")
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	srv := NewServer(api, "", "test", false)

	serverT, clientT := mcpsdk.NewInMemoryTransports()
	if _, err = srv.Connect(ctx, serverT, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = sess.Close() }()

	seen := map[string]bool{}
	resList, err := sess.ListResources(ctx, nil)
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	for _, r := range resList.Resources {
		seen[r.URI] = true
	}
	for _, want := range []string{"dag://list", "health://control-plane"} {
		if !seen[want] {
			t.Errorf("static resource %q not advertised via resources/list", want)
		}
	}

	tmpl := map[string]bool{}
	tmplList, err := sess.ListResourceTemplates(ctx, nil)
	if err != nil {
		t.Fatalf("ListResourceTemplates: %v", err)
	}
	for _, r := range tmplList.ResourceTemplates {
		tmpl[r.URITemplate] = true
	}
	for _, want := range []string{
		"run://detail/{dag_id}/{run_id}",
		"task://instances/{dag_id}/{run_id}",
		"log://task/{dag_id}/{run_id}/{task_id}/{try_number}",
		"dag://source/{dag_id}",
		"dag://spec/{dag_id}",
	} {
		if !tmpl[want] {
			t.Errorf("resource template %q not advertised via resources/templates/list", want)
		}
	}
}

// testHandlers spins up a fake control plane and returns handlers wired to it.
func testHandlers(t *testing.T, fn http.HandlerFunc) *handlers {
	t.Helper()
	srv := httptest.NewServer(fn)
	t.Cleanup(srv.Close)
	c, err := apiclient.New(srv.URL, "")
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return &handlers{api: c}
}

// TestPerRequestTokenPassThrough: in http mode (requireBearer), a request
// carrying an Authorization header makes the handler build a client with THAT
// token, and the control plane sees it — the pass-through the Pro transport
// relies on (D9).
func TestPerRequestTokenPassThrough(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"dags":[],"total_entries":0}`)
	}))
	defer srv.Close()

	base, err := apiclient.New(srv.URL, "") // base has NO token
	if err != nil {
		t.Fatal(err)
	}
	h := &handlers{api: base, serverURL: srv.URL, requireBearer: true}
	req := &mcpsdk.CallToolRequest{Extra: &mcpsdk.RequestExtra{
		Header: http.Header{"Authorization": []string{"Bearer usertok"}},
	}}
	if _, _, err := h.listDags(context.Background(), req, listDagsInput{}); err != nil {
		t.Fatalf("listDags: %v", err)
	}
	if gotAuth != "Bearer usertok" {
		t.Errorf("per-request token not passed through; control plane saw %q", gotAuth)
	}
}

// TestHTTPModeRefusesMissingBearer: in http mode a bearer-less request is
// refused, NOT served with the base client — otherwise an anonymous caller could
// ride an ambient process token (the D9 defect the review caught). The control
// plane must never be touched.
func TestHTTPModeRefusesMissingBearer(t *testing.T) {
	var touched bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		touched = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"dags":[],"total_entries":0}`)
	}))
	defer srv.Close()

	base, err := apiclient.New(srv.URL, "ambient-should-not-be-used")
	if err != nil {
		t.Fatal(err)
	}
	h := &handlers{api: base, serverURL: srv.URL, requireBearer: true}

	// No Extra at all, and an explicitly empty header — both must be refused.
	for _, req := range []*mcpsdk.CallToolRequest{
		{},
		{Extra: &mcpsdk.RequestExtra{Header: http.Header{}}},
		{Extra: &mcpsdk.RequestExtra{Header: http.Header{"Authorization": []string{"Bearer "}}}},
	} {
		if _, _, err := h.listDags(context.Background(), req, listDagsInput{}); err == nil {
			t.Errorf("bearer-less http request should be refused, req=%+v", req)
		}
	}
	if touched {
		t.Error("a refused request must never reach the control plane")
	}
}

// TestStdioIgnoresHeader: in stdio mode (requireBearer=false) the base
// (process-token) client is always used and any inbound Authorization header is
// ignored — so stdio can't be coaxed into per-request re-tokening.
func TestStdioIgnoresHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"dags":[],"total_entries":0}`)
	}))
	defer srv.Close()

	base, err := apiclient.New(srv.URL, "basetok")
	if err != nil {
		t.Fatal(err)
	}
	h := &handlers{api: base, serverURL: srv.URL} // requireBearer=false (stdio)
	req := &mcpsdk.CallToolRequest{Extra: &mcpsdk.RequestExtra{
		Header: http.Header{"Authorization": []string{"Bearer usertok"}},
	}}
	if _, _, err := h.listDags(context.Background(), req, listDagsInput{}); err != nil {
		t.Fatalf("listDags: %v", err)
	}
	if gotAuth != "Bearer basetok" {
		t.Errorf("stdio must use the base token and ignore the header; control plane saw %q", gotAuth)
	}
}

// TestListDagsShapesOutput: the tool calls /api/v2/dags and returns a compact
// shape (dag_id + is_paused), not the verbose upstream payload (ADR 0050 D7/R18).
func TestListDagsShapesOutput(t *testing.T) {
	h := testHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/dags" {
			t.Errorf("path = %q, want /api/v2/dags", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"dags":[{"dag_id":"etl","is_paused":false},{"dag_id":"ml","is_paused":true}],"total_entries":2}`))
	})

	_, out, err := h.listDags(context.Background(), nil, listDagsInput{})
	if err != nil {
		t.Fatalf("listDags: %v", err)
	}
	if out.TotalEntries != 2 || len(out.Dags) != 2 {
		t.Fatalf("output = %+v, want 2 dags", out)
	}
	if out.Dags[0].DagID != "etl" || out.Dags[0].IsPaused {
		t.Errorf("dags[0] = %+v, want etl/not-paused", out.Dags[0])
	}
	if out.Dags[1].DagID != "ml" || !out.Dags[1].IsPaused {
		t.Errorf("dags[1] = %+v, want ml/paused", out.Dags[1])
	}
}

// TestListDagsControlPlaneError: a non-200 from the control plane surfaces as an
// error, not an empty-but-successful result (so the agent does not conclude
// "there are no DAGs" when the call actually failed).
func TestListDagsControlPlaneError(t *testing.T) {
	h := testHandlers(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	if _, _, err := h.listDags(context.Background(), nil, listDagsInput{}); err == nil {
		t.Error("expected an error when the control plane returns 500")
	}
}
