package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

// The list reads answer their schema's empty collection, so the views render
// an empty state.
func TestAirflow33UnservedListsAnswerEmpty(t *testing.T) {
	srv, _ := clearDefaultsServer(t)
	for path, want := range map[string]string{
		"/api/v2/assets/1/state-store":                                  `{"asset_state_store":[],"total_entries":0}`,
		"/api/v2/dags/etl/dagRuns/r1/taskInstances/extract/state-store": `{"task_state_store":[],"total_entries":0}`,
		"/ui/partitioned_dag_runs?dag_id=etl":                           `{"partitioned_dag_runs":[],"total":0}`,
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
