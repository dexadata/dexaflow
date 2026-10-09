package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const testUIBase = "https://flow.example.com"

// withUI sets the UI base URL on handlers built by testHandlers, the same way
// the WithUIBaseURL option does on NewServer.
func withUI(h *handlers, base string) *handlers {
	WithUIBaseURL(base)(h)
	return h
}

// TestWebURLShapes pins each entity's link to the UI route that opens it
// (#1471): a DAG, a run, a task attempt, and one log line, whose anchor is the
// line's 0-based index in the UI log viewer.
func TestWebURLShapes(t *testing.T) {
	l := uiLinks{base: "https://flow.example.com"}
	cases := []struct{ name, got, want string }{
		{"dag", l.dag("etl"), "https://flow.example.com/dags/etl"},
		{"run", l.run("etl", "r1"), "https://flow.example.com/dags/etl/runs/r1"},
		{"task", l.task("etl", "r1", "load", 2), "https://flow.example.com/dags/etl/runs/r1/tasks/load?try_number=2"},
		{"task without a try", l.task("etl", "r1", "load", 0), "https://flow.example.com/dags/etl/runs/r1/tasks/load"},
		{"log line", l.logLine("etl", "r1", "load", 2, 7), "https://flow.example.com/dags/etl/runs/r1/tasks/load?try_number=2#7"},
		{"escaped segments", l.run("my dag", "manual__2026/10/08"), "https://flow.example.com/dags/my%20dag/runs/manual__2026%2F10%2F08"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: web_url = %q, want %q", c.name, c.got, c.want)
		}
	}
}

// TestWebURLBaseNormalized: a trailing slash on the base is dropped and a path
// prefix (a UI served under /flow) is kept.
func TestWebURLBaseNormalized(t *testing.T) {
	h := withUI(&handlers{}, "https://example.com/flow/")
	if got, want := h.links.dag("etl"), "https://example.com/flow/dags/etl"; got != want {
		t.Errorf("web_url = %q, want %q", got, want)
	}
}

// TestWebURLAbsentWithoutBase: with no base URL every builder returns "", so
// the omitempty web_url fields stay out of the results.
func TestWebURLAbsentWithoutBase(t *testing.T) {
	var l uiLinks
	for name, got := range map[string]string{
		"dag":      l.dag("etl"),
		"run":      l.run("etl", "r1"),
		"task":     l.task("etl", "r1", "load", 1),
		"log line": l.logLine("etl", "r1", "load", 1, 0),
	} {
		if got != "" {
			t.Errorf("%s: web_url = %q without a base URL, want empty", name, got)
		}
	}
}

func dagListServer(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"dags":[{"dag_id":"etl","is_paused":false}],"total_entries":1}`)
	}
}

func TestListDagsWebURL(t *testing.T) {
	h := withUI(testHandlers(t, dagListServer(t)), testUIBase)
	_, out, err := h.listDags(context.Background(), nil, listDagsInput{})
	if err != nil {
		t.Fatalf("listDags: %v", err)
	}
	if got, want := out.Dags[0].WebURL, testUIBase+"/dags/etl"; got != want {
		t.Errorf("web_url = %q, want %q", got, want)
	}
}

func TestListDagsNoWebURLWithoutBase(t *testing.T) {
	h := testHandlers(t, dagListServer(t))
	_, out, err := h.listDags(context.Background(), nil, listDagsInput{})
	if err != nil {
		t.Fatalf("listDags: %v", err)
	}
	b, _ := json.Marshal(out)
	if strings.Contains(string(b), "web_url") {
		t.Errorf("web_url present without a base URL: %s", b)
	}
}

func failedRunServer(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v2/dags/etl/dagRuns/r1":
			_, _ = io.WriteString(w, `{"dag_id":"etl","dag_run_id":"r1","state":"failed"}`)
		case "/api/v2/dags/etl/dagRuns/r1/taskInstances":
			_, _ = io.WriteString(w, `{"task_instances":[`+
				`{"task_id":"extract","state":"success","try_number":1},`+
				`{"task_id":"load","state":"failed","try_number":2}],"total_entries":2}`)
		case "/api/v2/dags/etl/dagRuns/r1/taskInstances/load/logs/2":
			_, _ = io.WriteString(w, "boot\n::group::setup\nok\n::endgroup::\nERROR boom\n")
		case "/api/v2/dags/etl/dagRuns/r1/taskInstances/extract/logs/1":
			_, _ = io.WriteString(w, "fine\n")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func TestDiagnoseRunWebURL(t *testing.T) {
	h := withUI(testHandlers(t, failedRunServer(t)), testUIBase)
	_, out, err := h.diagnoseRun(context.Background(), nil, diagnoseRunInput{DagID: "etl", RunID: "r1"})
	if err != nil {
		t.Fatalf("diagnoseRun: %v", err)
	}
	if got, want := out.WebURL, testUIBase+"/dags/etl/runs/r1"; got != want {
		t.Errorf("run web_url = %q, want %q", got, want)
	}
	if len(out.FailedTasks) != 1 {
		t.Fatalf("failed_tasks = %+v", out.FailedTasks)
	}
	if got, want := out.FailedTasks[0].WebURL, testUIBase+"/dags/etl/runs/r1/tasks/load?try_number=2"; got != want {
		t.Errorf("task web_url = %q, want %q", got, want)
	}
}

// TestSearchLogsWebURL: each match links to its line in the UI log viewer. The
// viewer drops ::group:: and ::endgroup:: markers before numbering, so line 5
// of the raw log ("ERROR boom", after two markers) is UI index 2.
func TestSearchLogsWebURL(t *testing.T) {
	h := withUI(testHandlers(t, failedRunServer(t)), testUIBase)
	want := testUIBase + "/dags/etl/runs/r1/tasks/load?try_number=2#2"

	_, out, err := h.searchLogs(context.Background(), nil, searchLogsInput{
		DagID: "etl", RunID: "r1", TaskID: "load", TryNumber: 2, Query: "boom",
	})
	if err != nil {
		t.Fatalf("searchLogs: %v", err)
	}
	if len(out.Matches) != 1 || out.Matches[0].WebURL != want {
		t.Errorf("task-scoped matches = %+v, want one with web_url %q", out.Matches, want)
	}

	_, out, err = h.searchLogs(context.Background(), nil, searchLogsInput{DagID: "etl", RunID: "r1", Query: "boom"})
	if err != nil {
		t.Fatalf("searchLogs run-wide: %v", err)
	}
	if len(out.Matches) != 1 || out.Matches[0].WebURL != want {
		t.Errorf("run-wide matches = %+v, want one with web_url %q", out.Matches, want)
	}
}

func TestSearchLogsNoWebURLWithoutBase(t *testing.T) {
	h := testHandlers(t, failedRunServer(t))
	_, out, err := h.searchLogs(context.Background(), nil, searchLogsInput{DagID: "etl", RunID: "r1", Query: "boom"})
	if err != nil {
		t.Fatalf("searchLogs: %v", err)
	}
	b, _ := json.Marshal(out)
	if strings.Contains(string(b), "web_url") {
		t.Errorf("web_url present without a base URL: %s", b)
	}
}

func TestResourcesWebURL(t *testing.T) {
	h := withUI(testHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v2/dags":
			_, _ = io.WriteString(w, `{"dags":[{"dag_id":"etl"}],"total_entries":1}`)
		case "/api/v2/dags/etl/dagRuns/r1":
			_, _ = io.WriteString(w, `{"dag_id":"etl","dag_run_id":"r1","state":"success"}`)
		case "/api/v2/dags/etl/dagRuns/r1/taskInstances":
			_, _ = io.WriteString(w, `{"task_instances":[{"task_id":"load","state":"failed","try_number":3}],"total_entries":1}`)
		}
	}), testUIBase)
	ctx := context.Background()
	cases := []struct {
		uri  string
		read func(context.Context, string) (string, error)
		want string
	}{
		{"dag://list", readText(h.readDagList), `"web_url":"` + testUIBase + `/dags/etl"`},
		{"run://detail/etl/r1", readText(h.readRunDetail), `"web_url":"` + testUIBase + `/dags/etl/runs/r1"`},
		{"task://instances/etl/r1", readText(h.readTaskInstances), `"web_url":"` + testUIBase + `/dags/etl/runs/r1/tasks/load?try_number=3"`},
	}
	for _, c := range cases {
		text, err := c.read(ctx, c.uri)
		if err != nil {
			t.Fatalf("%s: %v", c.uri, err)
		}
		// json.Marshal escapes nothing in these URLs but '?'-free JSON is not
		// guaranteed; compare against the decoded-escape form too.
		if !strings.Contains(strings.ReplaceAll(text, `&`, "&"), c.want) {
			t.Errorf("%s: %s does not contain %s", c.uri, text, c.want)
		}
	}
}

func TestResourcesNoWebURLWithoutBase(t *testing.T) {
	h := testHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v2/dags/etl/dagRuns/r1":
			_, _ = io.WriteString(w, `{"dag_id":"etl","dag_run_id":"r1","state":"success"}`)
		case "/api/v2/dags/etl/dagRuns/r1/taskInstances":
			_, _ = io.WriteString(w, `{"task_instances":[{"task_id":"load","state":"failed","try_number":3}],"total_entries":1}`)
		}
	})
	for _, c := range []struct {
		uri  string
		read func(context.Context, string) (string, error)
	}{
		{"run://detail/etl/r1", readText(h.readRunDetail)},
		{"task://instances/etl/r1", readText(h.readTaskInstances)},
	} {
		text, err := c.read(context.Background(), c.uri)
		if err != nil {
			t.Fatalf("%s: %v", c.uri, err)
		}
		if strings.Contains(text, "web_url") {
			t.Errorf("%s: web_url present without a base URL: %s", c.uri, text)
		}
	}
}

// readText adapts a resource handler to "read this URI, return its text".
func readText(fn func(context.Context, *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error)) func(context.Context, string) (string, error) {
	return func(ctx context.Context, uri string) (string, error) {
		res, err := fn(ctx, readReq(uri))
		if err != nil {
			return "", err
		}
		return res.Contents[0].Text, nil
	}
}

// TestValidateUIBaseURL: the base must be an absolute http(s) URL with no query
// or fragment, since every link is built by appending a path to it. Empty is
// valid and turns links off.
func TestValidateUIBaseURL(t *testing.T) {
	for _, ok := range []string{"", "https://flow.example.com", "http://localhost:8088/", "https://example.com/flow"} {
		if err := ValidateUIBaseURL(ok); err != nil {
			t.Errorf("ValidateUIBaseURL(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"flow.example.com", "/dags", "ftp://example.com", "https://example.com/?x=1", "https://example.com/#top", "https://"} {
		if err := ValidateUIBaseURL(bad); err == nil {
			t.Errorf("ValidateUIBaseURL(%q) = nil, want an error", bad)
		}
	}
}
