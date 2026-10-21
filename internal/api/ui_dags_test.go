package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
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
	return newTestServer(Dependencies{
		Observability: ObservabilityDeps{
			Logger: discardLogger(),
		},
		Server: ServerDeps{
			CORSOrigins: []string{"*"},
		},
		Auth: AuthDeps{
			Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
			RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		},
		Stores: StoreDeps{
			Dags:       &fakeDagRepo{dags: dags},
			LatestRuns: latest,
			Specs:      specs,
		},
	})
}

func TestUIDagsForwardsFilters(t *testing.T) {
	repo := &fakeDagRepo{dags: []domain.DAG{
		{DagID: "active_dag", IsPaused: false},
		{DagID: "paused_dag", IsPaused: true},
	}}
	srv := newTestServer(Dependencies{
		Observability: ObservabilityDeps{
			Logger: discardLogger(),
		},
		Server: ServerDeps{
			CORSOrigins: []string{"*"},
		},
		Auth: AuthDeps{
			Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
			RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		},
		Stores: StoreDeps{
			Dags:       repo,
			LatestRuns: &fakeLatestRuns{},
		},
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
	srv := newTestServer(Dependencies{
		Observability: ObservabilityDeps{
			Logger: discardLogger(),
		},
		Server: ServerDeps{
			CORSOrigins: []string{"*"},
		},
		Auth: AuthDeps{
			Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
			RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		},
		Stores: StoreDeps{
			Dags:       repo,
			LatestRuns: &fakeLatestRuns{},
		},
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

func TestUIDagsSearchAndSortFilters(t *testing.T) {
	repo := &fakeDagRepo{dags: []domain.DAG{
		{DagID: "alpha_dag", Owner: "alice"},
		{DagID: "beta_dag", Owner: "bob"},
		{DagID: "gamma_dag", Owner: "alice"},
	}}
	srv := NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		Dags:          repo,
		LatestRuns:    &fakeLatestRuns{},
	})

	rec := authGet(srv, http.MethodGet, "/ui/dags?dag_id_pattern=alpha&order_by=-dag_id&owners=alice", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("/ui/dags = %d", rec.Code)
	}
	if repo.gotFilter.DagIDPattern != "alpha" {
		t.Errorf("expected DagIDPattern 'alpha', got %q", repo.gotFilter.DagIDPattern)
	}
	if repo.gotFilter.OrderBy != "-dag_id" {
		t.Errorf("expected OrderBy '-dag_id', got %q", repo.gotFilter.OrderBy)
	}
	if len(repo.gotFilter.Owners) != 1 || repo.gotFilter.Owners[0] != "alice" {
		t.Errorf("expected Owners ['alice'], got %v", repo.gotFilter.Owners)
	}

	var col dagWithRunsCollectionDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &col); err != nil {
		t.Fatal(err)
	}
	if len(col.Dags) != 1 || col.Dags[0].DagID != "alpha_dag" {
		t.Errorf("expected alpha_dag, got %+v", col.Dags)
	}
}

func TestUIDagsTagFilters(t *testing.T) {
	repo := &fakeDagRepo{dags: []domain.DAG{
		{DagID: "d1", Tags: []string{"finance", "daily"}},
		{DagID: "d2", Tags: []string{"finance", "hourly"}},
		{DagID: "d3", Tags: []string{"marketing"}},
	}}
	srv := NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		Dags:          repo,
		LatestRuns:    &fakeLatestRuns{},
	})

	// Any mode
	rec := authGet(srv, http.MethodGet, "/ui/dags?tags=finance,marketing&tags_match_mode=any", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("/ui/dags = %d", rec.Code)
	}
	var col dagWithRunsCollectionDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &col); err != nil {
		t.Fatal(err)
	}
	if len(col.Dags) != 3 {
		t.Errorf("expected 3 DAGs matching tags 'finance' or 'marketing', got %d", len(col.Dags))
	}

	// All mode
	recAll := authGet(srv, http.MethodGet, "/ui/dags?tags=finance,daily&tags_match_mode=all", "")
	if recAll.Code != http.StatusOK {
		t.Fatalf("/ui/dags = %d", recAll.Code)
	}
	var colAll dagWithRunsCollectionDTO
	if err := json.Unmarshal(recAll.Body.Bytes(), &colAll); err != nil {
		t.Fatal(err)
	}
	if len(colAll.Dags) != 1 || colAll.Dags[0].DagID != "d1" {
		t.Errorf("expected only d1 matching both tags, got %+v", colAll.Dags)
	}
}

func TestListDagTagsEndpoint(t *testing.T) {
	repo := &fakeDagRepo{dags: []domain.DAG{
		{DagID: "d1", Tags: []string{"alpha", "beta"}},
		{DagID: "d2", Tags: []string{"beta", "gamma"}},
	}}
	srv := NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		Dags:          repo,
	})

	rec := authGet(srv, http.MethodGet, "/api/v2/dagTags", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v2/dagTags = %d", rec.Code)
	}
	// Airflow's DAGTagCollectionResponse: tags are plain strings, which the
	// UI's tag filter maps to {label, value}; objects render "[object Object]".
	var resp struct {
		Tags         []string `json:"tags"`
		TotalEntries int      `json:"total_entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("tags must be a list of strings: %v (%s)", err, rec.Body.String())
	}
	if resp.TotalEntries != 3 || strings.Join(resp.Tags, ",") != "alpha,beta,gamma" {
		t.Errorf("expected alpha,beta,gamma, got tags=%v total=%d", resp.Tags, resp.TotalEntries)
	}

	for _, tc := range []struct{ query, want string }{
		{"tag_name_pattern=et", "beta"},
		{"tag_name_prefix_pattern=g", "gamma"},
		{"tag_name_prefix_pattern=et", ""},
	} {
		rec := authGet(srv, http.MethodGet, "/api/v2/dagTags?"+tc.query, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/v2/dagTags?%s = %d", tc.query, rec.Code)
		}
		var got struct {
			Tags []string `json:"tags"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if strings.Join(got.Tags, ",") != tc.want {
			t.Errorf("?%s = %v, want %q", tc.query, got.Tags, tc.want)
		}
	}
}
