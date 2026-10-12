package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

// The embedded SPA is the Airflow 3.3 UI. These tests pin the fields and
// endpoints it reads that the 3.2 UI did not.

// The 3.3 TaskInstanceState gained awaiting_input; the dashboard iterates it.
func TestTaskInstanceStateCountsCarryAwaitingInput(t *testing.T) {
	if _, ok := zeroTaskInstanceStateCount()["awaiting_input"]; !ok {
		t.Error("zero state count has no awaiting_input")
	}
	if _, ok := tiStatesObject(nil)["awaiting_input"]; !ok {
		t.Error("state count object has no awaiting_input")
	}
}

// Dexaflow counts states exactly, so the 3.3 lower-bound flags are false and
// the dashboard shows no "N+" counts.
func TestHistoricalMetricsReportsExactCounts(t *testing.T) {
	reader := fakeStatsReader{hist: domain.HistoricalMetrics{TIStates: map[string]int{"success": 3}}}
	w := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet,
		"/ui/dashboard/historical_metrics_data?start_date=2026-05-01T00:00:00Z", http.NoBody)
	newDashboardEngine(reader).ServeHTTP(w, req)
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	for _, k := range []string{"dag_run_counts_are_lower_bounds", "task_instance_counts_are_lower_bounds"} {
		if v, ok := got[k]; !ok || v != false {
			t.Errorf("%s = %v, want false", k, v)
		}
	}
}

// The grid shades a run that has a note; has_note is required on GridRunsResponse.
func TestGridRunsCarryHasNote(t *testing.T) {
	at := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)
	runs := []domain.DagRun{
		{DagID: "etl", RunID: "noted", LogicalDate: at, Note: "checked by ops"},
		{DagID: "etl", RunID: "plain", LogicalDate: at.Add(-time.Hour)},
	}
	rec := authGet(viewsServer(runs, nil), http.MethodGet, "/ui/grid/runs/etl", "")
	var cols []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &cols); err != nil || len(cols) != 2 {
		t.Fatalf("grid runs = %s", rec.Body.String())
	}
	got := map[any]any{cols[0]["run_id"]: cols[0]["has_note"], cols[1]["run_id"]: cols[1]["has_note"]}
	if got["noted"] != true || got["plain"] != false {
		t.Errorf("has_note = %v, want noted:true plain:false", got)
	}
}

// partition_date is required on the 3.3 DAGRunResponse, and the run Details
// view renders a Partition Date row unless it is null.
func TestDagRunPartitionDateIsNull(t *testing.T) {
	b, err := json.Marshal(toDagRunDTO(domain.DagRun{DagID: "etl", RunID: "r1"}))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if v, ok := got["partition_date"]; !ok || string(v) != "null" {
		t.Errorf("partition_date = %s (present %v), want null", v, ok)
	}
}

// The 3.3 run and DAG pages read deadline alerts and run duration stats.
// Dexaflow has neither, so each answers its schema's empty value rather than
// the catch-all {}.
func TestAirflow33UIStubs(t *testing.T) {
	srv := stubsServer()
	for path, want := range map[string]string{
		"/ui/dags/etl/deadlineAlerts":       `{"deadline_alerts":[],"total_entries":0}`,
		"/ui/dags/etl/dagRuns/r1/deadlines": `{"deadlines":[],"total_entries":0}`,
		"/ui/dags/etl/dagRuns/r1/stats":     `{"duration":null}`,
	} {
		rec := authGet(srv, http.MethodGet, path, "")
		if rec.Code != http.StatusOK {
			t.Errorf("%s = %d, want 200", path, rec.Code)
			continue
		}
		var got, exp any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		_ = json.Unmarshal([]byte(want), &exp)
		gb, _ := json.Marshal(got)
		eb, _ := json.Marshal(exp)
		if !bytes.Equal(gb, eb) {
			t.Errorf("%s = %s, want %s", path, gb, eb)
		}
	}
}

// The 3.3 Clear dialogs treat a run with no dag_versions as one carried over
// from Airflow 2: they force "Run with latest bundle version" on and disable
// it, so the operator could no longer choose. A run pinned to a registered
// version names that version.
func TestGetDagRunNamesItsPinnedVersion(t *testing.T) {
	created := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	srv := NewServer(Dependencies{
		Logger: discardLogger(), Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter: auth.NewRateLimiter(100, time.Minute), CORSOrigins: []string{"*"}, TokenTTLSecs: 3600,
		DagRuns: &fakeRunRepo{runs: []domain.DagRun{
			{DagID: "etl", RunID: "pinned", Version: "v1.0.0"},
			{DagID: "etl", RunID: "unknown", Version: "gone"},
		}},
		DagVersions: &fakeVersionLister{versions: []domain.DagVersion{
			{ID: "id2", VersionNumber: 2, Version: "v1.1.0", CreatedAt: created},
			{ID: "id1", VersionNumber: 1, Version: "v1.0.0", CreatedAt: created},
		}},
	})
	versionsOf := func(run string) []map[string]any {
		t.Helper()
		rec := authGet(srv, http.MethodGet, "/api/v2/dags/etl/dagRuns/"+run, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d (%s)", run, rec.Code, rec.Body.String())
		}
		var got struct {
			DagVersions []map[string]any `json:"dag_versions"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got.DagVersions
	}
	v := versionsOf("pinned")
	if len(v) != 1 || v[0]["version_number"] != float64(1) || v[0]["bundle_version"] != "v1.0.0" || v[0]["id"] != "id1" {
		t.Errorf("pinned run dag_versions = %v, want version 1 (v1.0.0)", v)
	}
	if v := versionsOf("unknown"); len(v) != 0 {
		t.Errorf("a run pinned to an unregistered label = %v, want no versions", v)
	}
}
