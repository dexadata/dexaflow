package mcp

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func readReq(uri string) *mcpsdk.ReadResourceRequest {
	return &mcpsdk.ReadResourceRequest{Params: &mcpsdk.ReadResourceParams{URI: uri}}
}

func TestReadDagListResource(t *testing.T) {
	h := testHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/dags" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"dags":[{"dag_id":"etl","is_paused":false}],"total_entries":1}`)
	})
	res, err := h.readDagList(context.Background(), readReq("dag://list"))
	if err != nil {
		t.Fatalf("readDagList: %v", err)
	}
	if len(res.Contents) != 1 || !strings.Contains(res.Contents[0].Text, `"dag_id":"etl"`) {
		t.Errorf("contents = %+v, want the etl dag", res.Contents)
	}
	if res.Contents[0].URI != "dag://list" {
		t.Errorf("content URI = %q", res.Contents[0].URI)
	}
}

func TestReadRunDetailResource(t *testing.T) {
	h := testHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/dags/etl/dagRuns/r1" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"dag_id":"etl","dag_run_id":"r1","state":"success","run_type":"manual"}`)
	})
	res, err := h.readRunDetail(context.Background(), readReq("run://detail/etl/r1"))
	if err != nil {
		t.Fatalf("readRunDetail: %v", err)
	}
	if !strings.Contains(res.Contents[0].Text, `"state":"success"`) {
		t.Errorf("detail missing state: %s", res.Contents[0].Text)
	}
}

func TestReadTaskInstancesResource(t *testing.T) {
	h := testHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/dags/etl/dagRuns/r1/taskInstances" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"task_instances":[{"task_id":"load","state":"failed","try_number":2}],"total_entries":1}`)
	})
	res, err := h.readTaskInstances(context.Background(), readReq("task://instances/etl/r1"))
	if err != nil {
		t.Fatalf("readTaskInstances: %v", err)
	}
	if !strings.Contains(res.Contents[0].Text, `"task_id":"load"`) {
		t.Errorf("missing task: %s", res.Contents[0].Text)
	}
}

func TestReadTaskLogResource(t *testing.T) {
	h := testHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/dags/etl/dagRuns/r1/taskInstances/load/logs/2" {
			t.Errorf("path = %q", r.URL.Path)
		}
		// text/plain body with a real ESC byte (Go \x1b escape) — sanitizer strips it.
		_, _ = io.WriteString(w, "boot\nrunning\x1b[0m\ndone\n")
	})
	res, err := h.readTaskLog(context.Background(), readReq("log://task/etl/r1/load/2"))
	if err != nil {
		t.Fatalf("readTaskLog: %v", err)
	}
	if res.Contents[0].MIMEType != "text/plain" {
		t.Errorf("mime = %q, want text/plain", res.Contents[0].MIMEType)
	}
	if strings.Contains(res.Contents[0].Text, "\x1b") {
		t.Errorf("log resource must be sanitized: %q", res.Contents[0].Text)
	}
}

func TestReadDagSourceResource(t *testing.T) {
	h := testHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v2/dags/etl/dagVersions" {
			_, _ = io.WriteString(w, `{"dag_versions":[{"version_number":1,"is_current":true}],"total_entries":1}`)
			return
		}
		if r.URL.Path != "/api/v2/dagSources/etl" {
			t.Errorf("path = %q", r.URL.Path)
		}
		// \r is a JSON-valid control-char escape (CR, 0x0d); the sanitizer must
		// strip it while keeping the \n line breaks of the multi-line source.
		_, _ = io.WriteString(w, `{"content":"def hi():\n    print('hi')\r\n","dag_id":"etl","version_number":1}`)
	})
	res, err := h.readDagSource(context.Background(), readReq("dag://source/etl"))
	if err != nil {
		t.Fatalf("readDagSource: %v", err)
	}
	txt := res.Contents[0].Text
	if !strings.Contains(txt, "def hi()") || !strings.Contains(txt, "print") {
		t.Errorf("source content wrong: %q", txt)
	}
	if !strings.Contains(txt, "\n") {
		t.Errorf("multi-line source must keep newlines: %q", txt)
	}
	if strings.Contains(txt, "\r") {
		t.Errorf("source must be sanitized of control bytes: %q", txt)
	}
}

// TestReadDagSourceResourceServesTheCurrentVersion: after a rollback the
// newest version is not the one that runs. dag://source serves the source of
// the current version, as dag://spec serves its spec, so it asks dagSources
// for the version_number the versions listing marks current.
func TestReadDagSourceResourceServesTheCurrentVersion(t *testing.T) {
	var sourceQuery string
	h := testHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v2/dags/etl/dagVersions":
			_, _ = io.WriteString(w, `{"dag_versions":[`+
				`{"version_number":2,"is_current":false},`+
				`{"version_number":1,"is_current":true}],"total_entries":2}`)
		case "/api/v2/dagSources/etl":
			sourceQuery = r.URL.RawQuery
			if r.URL.Query().Get("version_number") == "1" {
				_, _ = io.WriteString(w, `{"content":"# v1, current","dag_id":"etl","version_number":1}`)
				return
			}
			_, _ = io.WriteString(w, `{"content":"# v2, newest","dag_id":"etl","version_number":2}`)
		default:
			t.Errorf("path = %q", r.URL.Path)
		}
	})
	res, err := h.readDagSource(context.Background(), readReq("dag://source/etl"))
	if err != nil {
		t.Fatalf("readDagSource: %v", err)
	}
	if got := res.Contents[0].Text; got != "# v1, current" {
		t.Errorf("source = %q (query %q), want the current v1's", got, sourceQuery)
	}
}

func TestReadDagSpecResource(t *testing.T) {
	h := testHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/dags/etl/spec" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"dag_id":"etl","tasks":[{"task_id":"load","type":"python"}]}`)
	})
	res, err := h.readDagSpec(context.Background(), readReq("dag://spec/etl"))
	if err != nil {
		t.Fatalf("readDagSpec: %v", err)
	}
	if res.Contents[0].MIMEType != "application/json" {
		t.Errorf("mime = %q, want application/json", res.Contents[0].MIMEType)
	}
	// Returned verbatim (the compiled artifact), so the graph is intact.
	if !strings.Contains(res.Contents[0].Text, `"task_id":"load"`) {
		t.Errorf("spec content missing the task graph: %s", res.Contents[0].Text)
	}
}

// TestReadDagSpecSanitizesValues: the compiled spec is author-controlled free
// text (task descriptions, owners). A control/ANSI byte stored in a string value
// (as the JSON escape \u001b) must be stripped before the agent's JSON
// can turn it back into a live escape (D10) — the spec is not exempt just
// because it is structured.
func TestReadDagSpecSanitizesValues(t *testing.T) {
	h := testHandlers(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// A task description carrying an ANSI escape as the JSON escape \u001b —
		// valid JSON on the wire; the decoder turns it into a real ESC byte.
		_, _ = io.WriteString(w, `{"dag_id":"etl","tasks":[{"task_id":"load","description":"evil\u001b[31mred"}]}`)
	})
	res, err := h.readDagSpec(context.Background(), readReq("dag://spec/etl"))
	if err != nil {
		t.Fatalf("readDagSpec: %v", err)
	}
	txt := res.Contents[0].Text
	if strings.Contains(txt, "\x1b") {
		t.Errorf("spec must be sanitized of control bytes; got %q", txt)
	}
	if !strings.Contains(txt, `"task_id":"load"`) || !strings.Contains(txt, "red") {
		t.Errorf("sanitizing must keep the graph and surrounding text: %s", txt)
	}
}

// TestReadDagSpecNonJSON: a 200 whose body is not JSON is reported as an error,
// not forwarded verbatim as application/json.
func TestReadDagSpecNonJSON(t *testing.T) {
	h := testHandlers(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `<html>not json</html>`)
	})
	if _, err := h.readDagSpec(context.Background(), readReq("dag://spec/etl")); err == nil {
		t.Error("a non-JSON spec body should surface as an error")
	}
}

func TestReadHealthResource(t *testing.T) {
	h := testHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v2/monitor/health":
			_, _ = io.WriteString(w, `{"scheduler":{"status":"healthy"},"metadatabase":{"status":"healthy"}}`)
		case "/api/v2/monitor/executor":
			_, _ = io.WriteString(w, `{"pod_dispatch_enabled":true,"task_namespace":"leoflow","execution_modes":["kubernetes_pod"]}`)
		case "/api/v2/version":
			_, _ = io.WriteString(w, `{"version":"v0.2.0","git_version":"abc123"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	res, err := h.readHealth(context.Background(), readReq("health://control-plane"))
	if err != nil {
		t.Fatalf("readHealth: %v", err)
	}
	txt := res.Contents[0].Text
	for _, want := range []string{`"health"`, `"executor"`, `"version"`, "kubernetes_pod", "v0.2.0"} {
		if !strings.Contains(txt, want) {
			t.Errorf("health snapshot missing %q; got %s", want, txt)
		}
	}
}

// TestReadHealthDegrades: if some monitor sections fail, the snapshot still
// returns what responded (structured degradation), not a hard error.
func TestReadHealthDegrades(t *testing.T) {
	h := testHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/version" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"version":"v0.2.0"}`)
			return
		}
		w.WriteHeader(http.StatusInternalServerError) // health + executor down
	})
	res, err := h.readHealth(context.Background(), readReq("health://control-plane"))
	if err != nil {
		t.Fatalf("readHealth should degrade, not error: %v", err)
	}
	if !strings.Contains(res.Contents[0].Text, "v0.2.0") || strings.Contains(res.Contents[0].Text, `"health"`) {
		t.Errorf("degraded snapshot should carry version only; got %s", res.Contents[0].Text)
	}
}

// TestReadResourceBadURI: a URI that does not match the template (wrong segment
// count) is a clear error, not a wrong-endpoint read.
func TestReadResourceBadURI(t *testing.T) {
	h := &handlers{}
	if _, err := h.readRunDetail(context.Background(), readReq("run://detail/etl")); err == nil {
		t.Error("expected an error for a run URI missing the run_id segment")
	}
	if _, err := h.readTaskLog(context.Background(), readReq("log://task/etl/r1/load/notanint")); err == nil {
		t.Error("expected an error for a non-integer try_number")
	}
}
