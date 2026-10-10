package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
)

func stubsServer() *gin.Engine {
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
	})
}

func TestUIStubsReturnSchemaValidEmpties(t *testing.T) {
	srv := stubsServer()

	// Collections: {total_entries:0, <field>:[]}.
	collections := map[string]string{
		"/ui/calendar/etl": "dag_runs",
		"/ui/backfills":    "backfills",
		"/ui/teams":        "teams",
	}
	for path, field := range collections {
		rec := authGet(srv, http.MethodGet, path, "")
		if rec.Code != http.StatusOK {
			t.Errorf("%s = %d, want 200", path, rec.Code)
			continue
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if string(body["total_entries"]) != "0" {
			t.Errorf("%s total_entries = %s", path, body["total_entries"])
		}
		if string(body[field]) != "[]" {
			t.Errorf("%s %s = %s, want []", path, field, body[field])
		}
	}

	// hook_meta is the connection-type catalog (not empty); see TestConnectionHookMeta.

	// dag_stats carries its four required counts.
	rec := authGet(srv, http.MethodGet, "/ui/dashboard/dag_stats", "")
	var stats map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &stats); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"active_dag_count", "failed_dag_count", "running_dag_count", "queued_dag_count"} {
		if _, ok := stats[f]; !ok {
			t.Errorf("dag_stats missing %q", f)
		}
	}

	// historical metrics: state-count objects present, not the catch-all {}.
	rec = authGet(srv, http.MethodGet, "/ui/dashboard/historical_metrics_data", "")
	var hist struct {
		TaskInstanceStates map[string]int `json:"task_instance_states"`
		StateCountLimit    *int           `json:"state_count_limit"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &hist); err != nil {
		t.Fatal(err)
	}
	if _, ok := hist.TaskInstanceStates["success"]; !ok || hist.StateCountLimit == nil {
		t.Errorf("historical_metrics_data not schema-valid: %s", rec.Body.String())
	}

	// dependencies graph: edges + nodes arrays.
	rec = authGet(srv, http.MethodGet, "/ui/dependencies", "")
	var graph struct {
		Edges []any `json:"edges"`
		Nodes []any `json:"nodes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &graph); err != nil {
		t.Fatalf("dependencies: %v (%s)", err, rec.Body.String())
	}

	// next_run_assets is the emptyObject() endpoint — pinning the bare `{}`
	// shape the SPA expects for the "asset triggers" widget (returning an
	// empty array instead would break the SPA's typed parser).
	rec = authGet(srv, http.MethodGet, "/ui/next_run_assets/etl", "")
	if rec.Code != http.StatusOK {
		t.Errorf("next_run_assets = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "{}" {
		t.Errorf("next_run_assets body = %q, want '{}' (bare object, not collection)", rec.Body.String())
	}
}

// TestZeroTaskInstanceStateCount asserts the helper returns every Airflow
// 3.2.1 state name at zero. The dashboard's "states by count" widget breaks
// silently if a state name is renamed or dropped (the front end iterates
// known keys).
func TestZeroTaskInstanceStateCount(t *testing.T) {
	got := zeroTaskInstanceStateCount()
	required := []string{
		"no_status", "removed", "scheduled", "queued", "running", "success",
		"restarting", "failed", "up_for_retry", "up_for_reschedule",
		"upstream_failed", "skipped", "deferred",
	}
	for _, k := range required {
		v, ok := got[k]
		if !ok {
			t.Errorf("missing state counter %q", k)
			continue
		}
		if v != 0 {
			t.Errorf("counter %q = %v, want 0", k, v)
		}
	}
}

func TestUIStubWritesStill501(t *testing.T) {
	// A write to an unimplemented /ui path degrades to 501 (via NoRoute), even
	// where a GET stub exists.
	rec := authGet(stubsServer(), http.MethodPost, "/ui/backfills", "{}")
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("POST /ui/backfills = %d, want 501", rec.Code)
	}
}
