package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"
)

// The run page's Clear button posts to /dagRuns/{dag_run_id}/clear, with the
// Airflow DAGRunClearBody: a dry run first (the affected task instances), then
// the clear itself (the run). Dexaflow answered 404, so the dialog listed
// nothing and its Confirm stayed disabled.
const runClearPath = "/api/v2/dags/etl/dagRuns/r1/clear"

func affectedIDs(t *testing.T, body []byte) string {
	t.Helper()
	var got struct {
		TaskInstances []struct {
			TaskID string `json:"task_id"`
		} `json:"task_instances"`
		TotalEntries int `json:"total_entries"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	ids := make([]string, 0, len(got.TaskInstances))
	for _, ti := range got.TaskInstances {
		ids = append(ids, ti.TaskID)
	}
	if got.TotalEntries != len(ids) {
		t.Errorf("total_entries = %d for %d instances", got.TotalEntries, len(ids))
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

// Airflow's DAGRunClearBody defaults dry_run to true and only_failed to false:
// an unflagged request previews every task instance of the run and clears nothing.
func TestClearDagRunUnflaggedPreviewsTheWholeRun(t *testing.T) {
	srv, tasks := clearDefaultsServer(t)
	rec := authGet(srv, http.MethodPost, runClearPath, `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear run = %d (%s)", rec.Code, rec.Body.String())
	}
	if got := affectedIDs(t, rec.Body.Bytes()); got != "extract,load" {
		t.Errorf("previewed %q, want extract,load", got)
	}
	if tasks.clearCalled {
		t.Error("an unflagged run clear executed; Airflow's default is a dry run")
	}
}

func TestClearDagRunOnlyFailedPreview(t *testing.T) {
	srv, _ := clearDefaultsServer(t)
	rec := authGet(srv, http.MethodPost, runClearPath, `{"dry_run":true,"only_failed":true,"only_new":false,"run_on_latest_version":null}`)
	if got := affectedIDs(t, rec.Body.Bytes()); got != "extract" {
		t.Errorf("previewed %q, want extract", got)
	}
}

// The confirmed clear resets the run's task instances and answers the run.
func TestClearDagRunExecutes(t *testing.T) {
	srv, tasks := clearDefaultsServer(t)
	rec := authGet(srv, http.MethodPost, runClearPath, `{"dry_run":false,"only_failed":true,"run_on_latest_version":true,"note":"retry after fix"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear run = %d (%s)", rec.Code, rec.Body.String())
	}
	if !tasks.clearCalled || !tasks.gotOnlyFailed {
		t.Errorf("clear called=%v onlyFailed=%v, want a failed-only clear", tasks.clearCalled, tasks.gotOnlyFailed)
	}
	if !tasks.lastClearOpts.ResetDagRun || !tasks.lastClearOpts.RunOnLatestVersion {
		t.Errorf("opts = %+v, want the run reset onto the latest version", tasks.lastClearOpts)
	}
	var run map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil || run["dag_run_id"] != "r1" {
		t.Errorf("answer = %s, want the DAGRunResponse of r1", rec.Body.String())
	}
}

// A run that does not exist is a 404, before anything is cleared.
func TestClearDagRunUnknownRun(t *testing.T) {
	srv, tasks := clearDefaultsServer(t)
	rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/dagRuns/nope/clear", `{"dry_run":false}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("clear of an unknown run = %d, want 404", rec.Code)
	}
	if tasks.clearCalled {
		t.Error("cleared task instances of a run that does not exist")
	}
}

// only_new clears the tasks a newer DAG version added; Dexaflow cannot tell
// them apart yet, so it refuses instead of clearing more than was asked.
func TestClearDagRunRefusesOnlyNew(t *testing.T) {
	srv, tasks := clearDefaultsServer(t)
	rec := authGet(srv, http.MethodPost, runClearPath, `{"dry_run":false,"only_new":true}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("only_new = %d, want 400", rec.Code)
	}
	if tasks.clearCalled {
		t.Error("an only_new clear executed")
	}
}
