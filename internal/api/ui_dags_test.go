package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

type fakeLatestRuns struct {
	byDag map[string][]domain.DagRun
	gotN  int
}

func (f *fakeLatestRuns) LatestRunsForDags(_ context.Context, _ string, _ []string, perDag int) (map[string][]domain.DagRun, error) {
	f.gotN = perDag
	if f.byDag == nil {
		return map[string][]domain.DagRun{}, nil
	}
	return f.byDag, nil
}

func uiDagsServer(dags []domain.DAG, latest *fakeLatestRuns) *gin.Engine {
	return uiDagsServerWithSpecs(dags, latest, nil)
}

// uiDagsServerWithSpecs is uiDagsServer with a DagSpecReader wired, so the
// details endpoint can serve declared params. A nil reader reproduces the
// param-free wiring exactly.
func uiDagsServerWithSpecs(dags []domain.DAG, latest *fakeLatestRuns, specs DagSpecReader) *gin.Engine {
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		Dags:          &fakeDagRepo{dags: dags},
		LatestRuns:    latest,
		Specs:         specs,
	})
}

func TestUIDagsForwardsFilters(t *testing.T) {
	repo := &fakeDagRepo{dags: []domain.DAG{
		{DagID: "active_dag", IsPaused: false},
		{DagID: "paused_dag", IsPaused: true},
	}}
	srv := NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		Dags:          repo,
		LatestRuns:    &fakeLatestRuns{},
	})

	rec := authGet(srv, http.MethodGet, "/ui/dags?last_dag_run_state=failed&paused=false", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("/ui/dags = %d (%s)", rec.Code, rec.Body.String())
	}
	if repo.gotRunState != "failed" {
		t.Errorf("last_dag_run_state not forwarded: got %q", repo.gotRunState)
	}
	if repo.gotPaused == nil || *repo.gotPaused != false {
		t.Errorf("paused filter not forwarded: got %v", repo.gotPaused)
	}
	// paused=false drops the paused dag.
	var col dagWithRunsCollectionDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &col); err != nil {
		t.Fatal(err)
	}
	if len(col.Dags) != 1 || col.Dags[0].DagID != "active_dag" {
		t.Errorf("paused=false should leave only active_dag, got %+v", col.Dags)
	}
}

func TestUIDagsNoFiltersByDefault(t *testing.T) {
	repo := &fakeDagRepo{dags: []domain.DAG{{DagID: "etl"}}}
	srv := NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		Dags:          repo,
		LatestRuns:    &fakeLatestRuns{},
	})
	if rec := authGet(srv, http.MethodGet, "/ui/dags", ""); rec.Code != http.StatusOK {
		t.Fatalf("/ui/dags = %d", rec.Code)
	}
	if repo.gotRunState != "" || repo.gotPaused != nil {
		t.Errorf("absent filters should be empty/nil, got state=%q paused=%v", repo.gotRunState, repo.gotPaused)
	}
}

// uiConfigRequiredFields-style coverage: assert every spec-required field is present.
var dagWithRunsRequired = []string{
	"dag_id", "dag_display_name", "is_paused", "is_stale", "last_parsed_time",
	"last_parse_duration", "last_expired", "bundle_name", "bundle_version",
	"relative_fileloc", "fileloc", "description", "timetable_summary",
	"timetable_description", "timetable_partitioned", "tags", "max_active_tasks",
	"max_active_runs", "max_consecutive_failed_dag_runs", "has_task_concurrency_limits",
	"has_import_errors", "next_dagrun_logical_date", "next_dagrun_data_interval_start",
	"next_dagrun_data_interval_end", "next_dagrun_run_after", "allowed_run_types",
	"owners", "asset_expression", "latest_dag_runs", "pending_actions", "is_favorite",
	"file_token",
}

func TestUIDagsEmbedsLatestRunsAndFullShape(t *testing.T) {
	sched := "0 5 * * *"
	start := time.Date(2026, 5, 22, 5, 0, 0, 0, time.UTC)
	latest := &fakeLatestRuns{byDag: map[string][]domain.DagRun{
		"etl": {{DagID: "etl", RunID: "r1", State: domain.DagRunStateSuccess, LogicalDate: start, StartedAt: &start}},
	}}
	dags := []domain.DAG{{DagID: "etl", Owner: "data", Tags: []string{"x"}, Schedule: &sched, IsActive: true, MaxActiveRuns: 16}}
	rec := authGet(uiDagsServer(dags, latest), http.MethodGet, "/ui/dags", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("/ui/dags = %d (%s)", rec.Code, rec.Body.String())
	}
	var col struct {
		Dags         []map[string]json.RawMessage `json:"dags"`
		TotalEntries int                          `json:"total_entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &col); err != nil {
		t.Fatal(err)
	}
	if col.TotalEntries != 1 || len(col.Dags) != 1 {
		t.Fatalf("unexpected collection: total=%d len=%d", col.TotalEntries, len(col.Dags))
	}
	for _, f := range dagWithRunsRequired {
		if _, ok := col.Dags[0][f]; !ok {
			t.Errorf("dag missing required field %q", f)
		}
	}
	var runs []map[string]any
	if err := json.Unmarshal(col.Dags[0]["latest_dag_runs"], &runs); err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0]["run_id"] != "r1" {
		t.Errorf("latest_dag_runs not embedded: %v", runs)
	}
}

func TestUIDagsRunsLimitParam(t *testing.T) {
	latest := &fakeLatestRuns{}
	rec := authGet(uiDagsServer([]domain.DAG{{DagID: "etl"}}, latest), http.MethodGet, "/ui/dags?dag_runs_limit=5", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d", rec.Code)
	}
	if latest.gotN != 5 {
		t.Errorf("dag_runs_limit not honored: perDag=%d, want 5", latest.gotN)
	}
}

func TestUIDagsDefaultRunsLimit(t *testing.T) {
	latest := &fakeLatestRuns{}
	authGet(uiDagsServer([]domain.DAG{{DagID: "etl"}}, latest), http.MethodGet, "/ui/dags", "")
	if latest.gotN != defaultDagRunsLimit {
		t.Errorf("default dag_runs_limit = %d, want %d", latest.gotN, defaultDagRunsLimit)
	}
}
