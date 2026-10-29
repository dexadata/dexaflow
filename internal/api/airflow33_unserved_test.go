package api

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/ui"
)

// The Airflow 3.3 UI calls endpoints Dexaflow does not serve yet: the bulk
// actions of the runs table, task group marking, partition clearing, the
// asset and task state stores and the partitioned run views. Each one must
// degrade the way the other unserved surfaces do (ADR 0018, "hide the
// uncovered, never break"): a write answers 501 with a detail the UI shows
// as a toast, a list read answers its schema's empty value. A 404 "API route
// not found" reads as a broken server, and a catch-all {} is not the shape
// the view reads.

// unservedDetail is the detail every unserved Airflow 3.3 call answers with.
const unservedDetail = "not available in Dexaflow yet"

// Every call below appears in the embedded 3.3.2 bundle (internal/ui/assets),
// with the body the UI sends where it sends one.
var airflow33UnservedCalls = []struct{ method, path, body string }{
	// Runs table, bulk "Clear Dag Runs": the dialog previews with a dry run
	// against dag_id "~", then confirms.
	{http.MethodPost, "/api/v2/dags/~/clearDagRuns", `{"dag_runs":[{"dag_id":"etl","dag_run_id":"r1"}],"dry_run":true,"only_failed":false}`},
	{http.MethodPost, "/api/v2/dags/etl/clearDagRuns", `{"dag_runs":[{"dag_id":"etl","dag_run_id":"r1"}],"dry_run":false}`},
	// Runs table, bulk "Mark Dag Runs as..." and "Delete Dag Runs".
	{http.MethodPatch, "/api/v2/dags/~/dagRuns", `{"actions":[{"action":"delete","entities":[{"dag_id":"etl","dag_run_id":"r1"}]}]}`},
	{http.MethodPost, "/api/v2/dags/etl/clearPartitions", `{"dry_run":true}`},
	// Mark a task group as success or failed, preview and confirm.
	{http.MethodPatch, "/api/v2/dags/etl/dagRuns/r1/taskGroupInstances/grp/dry_run", `{"new_state":"success"}`},
	{http.MethodPatch, "/api/v2/dags/etl/dagRuns/r1/taskGroupInstances/grp", `{"new_state":"success"}`},
	// Asset state store.
	{http.MethodDelete, "/api/v2/assets/1/state-store", ""},
	{http.MethodGet, "/api/v2/assets/1/state-store/k", ""},
	{http.MethodPut, "/api/v2/assets/1/state-store/k", `{"value":1}`},
	{http.MethodDelete, "/api/v2/assets/1/state-store/k", ""},
	// Task state store.
	{http.MethodDelete, "/api/v2/dags/etl/dagRuns/r1/taskInstances/extract/state-store", ""},
	{http.MethodGet, "/api/v2/dags/etl/dagRuns/r1/taskInstances/extract/state-store/k", ""},
	{http.MethodPut, "/api/v2/dags/etl/dagRuns/r1/taskInstances/extract/state-store/k", `{"value":1}`},
	{http.MethodPatch, "/api/v2/dags/etl/dagRuns/r1/taskInstances/extract/state-store/k", `{"value":1}`},
	{http.MethodDelete, "/api/v2/dags/etl/dagRuns/r1/taskInstances/extract/state-store/k", ""},
	// A pending partitioned run has no empty form: there is none to show.
	{http.MethodGet, "/ui/pending_partitioned_dag_run/etl?partition_key=p1", ""},
	// Issue #1926: actions of the embedded UI.
	// Task Instances bulk PATCH:
	{http.MethodPatch, "/api/v2/dags/etl/dagRuns/r1/taskInstances", `{"actions":[{"action":"clear","entities":[{"dag_id":"etl","dag_run_id":"r1","task_id":"extract"}]}]}`},
	{http.MethodPatch, "/api/v2/dags/~/dagRuns/~/taskInstances", `{"actions":[{"action":"clear","entities":[{"dag_id":"etl","dag_run_id":"r1","task_id":"extract"}]}]}`},
	// Task Instance DELETE:
	{http.MethodDelete, "/api/v2/dags/etl/dagRuns/r1/taskInstances/extract", ""},
	// Variables bulk PATCH:
	{http.MethodPatch, "/api/v2/variables", `{"actions":[{"action":"delete","entities":["k1"]}]}`},
	// Connections bulk PATCH:
	{http.MethodPatch, "/api/v2/connections", `{"actions":[{"action":"delete","entities":["c1"]}]}`},
	// XCom entries POST, DELETE and PATCH:
	{http.MethodPost, "/api/v2/dags/etl/dagRuns/r1/taskInstances/extract/xcomEntries", `{"key":"k1","value":"v1"}`},
	{http.MethodDelete, "/api/v2/dags/etl/dagRuns/r1/taskInstances/extract/xcomEntries/k1", ""},
	{http.MethodPatch, "/api/v2/dags/etl/dagRuns/r1/taskInstances/extract/xcomEntries/k1", `{"value":"v2"}`},
	// Reparse DAG file PUT:
	{http.MethodPut, "/api/v2/parseDagFile/tok123", ""},
	// Queued asset events DELETE:
	{http.MethodDelete, "/api/v2/dags/etl/assets/queuedEvents", ""},
	{http.MethodDelete, "/api/v2/assets/1/queuedEvents", ""},
	{http.MethodDelete, "/api/v2/dags/etl/assets/1/queuedEvents", ""},
}

func TestAirflow33UnservedCallsAnswer501(t *testing.T) {
	srv, _ := clearDefaultsServer(t)
	for _, call := range airflow33UnservedCalls {
		rec := authGet(srv, call.method, call.path, call.body)
		if rec.Code != http.StatusNotImplemented {
			t.Errorf("%s %s = %d (%s), want 501", call.method, call.path, rec.Code, rec.Body.String())
			continue
		}
		var p Problem
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || !strings.Contains(p.Detail, unservedDetail) {
			t.Errorf("%s %s detail = %q (%v), want it to say %q", call.method, call.path, p.Detail, err, unservedDetail)
		}
	}
}

// A state store PATCH shares the catch-all under taskInstances/{task_id}
// with Mark as. It must never reach the mark: a body that names a state
// still answers 501 and changes nothing.
func TestTaskStateStorePatchNeverMarksTheTask(t *testing.T) {
	srv, tasks := clearDefaultsServer(t)
	rec := authGet(srv, http.MethodPatch,
		"/api/v2/dags/etl/dagRuns/r1/taskInstances/extract/state-store/k", `{"new_state":"success"}`)
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("state store PATCH = %d (%s), want 501", rec.Code, rec.Body.String())
	}
	if tasks.setState != "" {
		t.Errorf("a state store PATCH marked %s as %s", tasks.setTaskID, tasks.setState)
	}
}

// An XCom PATCH shares the catch-all under taskInstances/{task_id} with Mark
// as. It must never reach the mark: a body that names a state still answers 501
// and changes nothing.
func TestTaskXComPatchNeverMarksTheTask(t *testing.T) {
	srv, tasks := clearDefaultsServer(t)
	rec := authGet(srv, http.MethodPatch,
		"/api/v2/dags/etl/dagRuns/r1/taskInstances/extract/xcomEntries/k", `{"new_state":"success"}`)
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("xcom PATCH = %d (%s), want 501", rec.Code, rec.Body.String())
	}
	if tasks.setState != "" {
		t.Errorf("an xcom PATCH marked %s as %s", tasks.setTaskID, tasks.setState)
	}
}

// The list reads answer their schema's empty collection, so the views render
// an empty state.
func TestAirflow33UnservedListsAnswerEmpty(t *testing.T) {
	srv, _ := clearDefaultsServer(t)
	for path, want := range map[string]string{
		"/api/v2/assets/1/state-store":                                  `{"asset_state_store":[],"total_entries":0}`,
		"/api/v2/dags/etl/dagRuns/r1/taskInstances/extract/state-store": `{"task_state_store":[],"total_entries":0}`,
		"/ui/partitioned_dag_runs?dag_id=etl":                           `{"partitioned_dag_runs":[],"total":0}`,
		"/api/v2/dags/etl/dagRuns/r1/upstreamAssetEvents":               `{"asset_events":[],"total_entries":0}`,
		"/api/v2/dags/etl/assets/queuedEvents":                          `{"queued_events":[],"total_entries":0}`,
		"/api/v2/assets/1/queuedEvents":                                 `{"queued_events":[],"total_entries":0}`,
		"/api/v2/dags/etl/assets/1/queuedEvents":                        `{"queued_events":[],"total_entries":0}`,
	} {
		rec := authGet(srv, http.MethodGet, path, "")
		if rec.Code != http.StatusOK {
			t.Errorf("%s = %d (%s), want 200", path, rec.Code, rec.Body.String())
			continue
		}
		if got := compactJSON(t, rec.Body.Bytes()); got != compactJSON(t, []byte(want)) {
			t.Errorf("%s = %s, want %s", path, got, want)
		}
	}
}

// The stubs stay behind authentication like every other API route.
func TestAirflow33UnservedCallsNeedAToken(t *testing.T) {
	srv, _ := clearDefaultsServer(t)
	for _, call := range airflow33UnservedCalls {
		req := httptest.NewRequestWithContext(context.Background(), call.method, call.path, strings.NewReader(call.body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a token = %d, want 401", call.method, call.path, rec.Code)
		}
	}
}

// compactJSON re-encodes a JSON document so two documents compare by value.
func compactJSON(t *testing.T, b []byte) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	out, _ := json.Marshal(v)
	return string(out)
}

// TestEmbeddedBundleCallsAreRoutedOrExplicitlyUnreached parses the embedded
// UI bundle for all method/URL pairs and asserts that every single call is
// either routed (does not answer 404) or explicitly pinned in unreachedCalls.
// Future Airflow UI bumps fail loudly if a new unrouted call appears.
func TestEmbeddedBundleCallsAreRoutedOrExplicitlyUnreached(t *testing.T) {
	srv := newFullScopeServer(t)

	// Collect all (method, url) calls from embedded JS files.
	bundleFS := ui.Assets()
	entries, err := fs.ReadDir(bundleFS, "assets")
	if err != nil {
		t.Fatalf("read embedded assets dir: %v", err)
	}

	callRe := regexp.MustCompile(`method:\x60([A-Z]+)\x60,url:\x60([^\x60]+)\x60`)
	type apiCall struct{ method, url string }
	foundCalls := map[apiCall]bool{}

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".js") {
			continue
		}
		data, err := fs.ReadFile(bundleFS, "assets/"+entry.Name())
		if err != nil {
			t.Fatalf("read asset file %s: %v", entry.Name(), err)
		}
		matches := callRe.FindAllStringSubmatch(string(data), -1)
		for _, m := range matches {
			foundCalls[apiCall{method: m[1], url: m[2]}] = true
		}
	}

	// Replacement map for path parameters to test routing.
	paramReplacements := map[string]string{
		"{dag_id}":          "etl",
		"{dag_run_id}":      "r1",
		"{run_id}":          "r1",
		"{task_id}":         "extract",
		"{group_id}":        "grp1",
		"{asset_id}":        "1",
		"{asset_alias_id}":  "1",
		"{connection_id}":   "c1",
		"{variable_key}":    "k1",
		"{pool_name}":       "p1",
		"{event_log_id}":    "1",
		"{import_error_id}": "1",
		"{version_number}":  "1",
		"{file_token}":      "tok1",
		"{map_index}":       "-1",
		"{try_number}":      "1",
		"{task_try_number}": "1",
		"{section}":         "core",
		"{option}":          "dags_folder",
		"{backfill_id}":     "1",
		"{xcom_key}":        "k1",
		"{key}":             "k1",
	}

	// Unreached calls that answer 404 because they are unreachable from menus
	// advertised by Dexaflow (e.g. backfills, asset writes, queueing probe).
	// This exact set is pinned so that any new unrouted call introduced by a
	// future UI bump fails loudly.
	unreachedCalls := map[apiCall]bool{
		{http.MethodPost, "/api/v2/assets/events"}:                  true,
		{http.MethodPost, "/api/v2/assets/{asset_id}/materialize"}:  true,
		{http.MethodGet, "/api/v2/assets/{asset_id}"}:               true,
		{http.MethodGet, "/api/v2/assets/aliases"}:                  true,
		{http.MethodGet, "/api/v2/assets/aliases/{asset_alias_id}"}: true,
		// The generated client defines these, but no hook or screen of the
		// bundle calls them (#1931), so they are not stubbed.
		{http.MethodGet, "/api/v2/backfills"}:                                              true,
		{http.MethodPost, "/api/v2/connections/defaults"}:                                  true,
		{http.MethodPost, "/api/v2/backfills"}:                                             true,
		{http.MethodPost, "/api/v2/backfills/dry_run"}:                                     true,
		{http.MethodGet, "/api/v2/backfills/{backfill_id}"}:                                true,
		{http.MethodPut, "/api/v2/backfills/{backfill_id}/pause"}:                          true,
		{http.MethodPut, "/api/v2/backfills/{backfill_id}/unpause"}:                        true,
		{http.MethodPut, "/api/v2/backfills/{backfill_id}/cancel"}:                         true,
		{http.MethodPost, "/api/v2/connections/enqueue-test"}:                              true,
		{http.MethodGet, "/api/v2/dags/{dag_id}/dagRuns/{dag_run_id}/wait"}:                true,
		{http.MethodPost, "/api/v2/dags/{dag_id}/dagRuns/list"}:                            true,
		{http.MethodPost, "/api/v2/dags/{dag_id}/dagRuns/{dag_run_id}/taskInstances/list"}: true,
		{http.MethodGet, "/api/v2/config/section/{section}/option/{option}"}:               true,
		{http.MethodGet, "/api/v2/eventLogs/{event_log_id}"}:                               true,
		{http.MethodGet, "/api/v2/importErrors/{import_error_id}"}:                         true,
		{http.MethodPatch, "/api/v2/dags"}:                                                 true,
		{http.MethodPatch, "/api/v2/pools"}:                                                true,
	}

	for call := range foundCalls {
		resolvedPath := call.url
		for placeholder, val := range paramReplacements {
			resolvedPath = strings.ReplaceAll(resolvedPath, placeholder, val)
		}
		req := httptest.NewRequestWithContext(context.Background(), call.method, resolvedPath, strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer engine")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		isUnrouted := rec.Code == http.StatusNotFound && strings.Contains(rec.Body.String(), "API route not found")
		isPinned := unreachedCalls[call]

		if isUnrouted && !isPinned {
			t.Errorf("new unrouted call in embedded bundle: %s %s (resolved %s) answers 404 API route not found",
				call.method, call.url, resolvedPath)
		} else if !isUnrouted && isPinned {
			t.Errorf("call %s %s is now routed (%d) but still pinned in unreachedCalls",
				call.method, call.url, rec.Code)
		}
	}
}
