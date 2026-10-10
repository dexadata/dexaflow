package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

type fakeDagRepo struct {
	dags        []domain.DAG
	gotRunState string
	gotPaused   *bool
	cleared     []string
}

func (f *fakeDagRepo) ListDags(context.Context, string, int, int) ([]domain.DAG, int, error) {
	return f.dags, len(f.dags), nil
}

func (f *fakeDagRepo) GetDag(_ context.Context, _, dagID string) (domain.DAG, error) {
	for _, d := range f.dags {
		if d.DagID == dagID {
			return d, nil
		}
	}
	return domain.DAG{}, ErrNotFound
}

func (f *fakeDagRepo) SetPaused(_ context.Context, _, dagID string, paused bool) (domain.DAG, error) {
	for _, d := range f.dags {
		if d.DagID == dagID {
			d.IsPaused = paused
			return d, nil
		}
	}
	return domain.DAG{}, ErrNotFound
}

func (f *fakeDagRepo) ListDagsFiltered(_ context.Context, _, runState string, paused *bool, _, _ int) ([]domain.DAG, int, error) {
	f.gotRunState = runState
	f.gotPaused = paused
	out := []domain.DAG{}
	for _, d := range f.dags {
		if paused != nil && d.IsPaused != *paused {
			continue
		}
		out = append(out, d)
	}
	return out, len(out), nil
}

func (f *fakeDagRepo) DeleteDag(_ context.Context, _, dagID string) error {
	for i, d := range f.dags {
		if d.DagID == dagID {
			f.dags = append(f.dags[:i], f.dags[i+1:]...)
			return nil
		}
	}
	return ErrNotFound
}

func (f *fakeDagRepo) ClearDagHistory(_ context.Context, _, dagID string) error {
	for _, d := range f.dags {
		if d.DagID == dagID {
			f.cleared = append(f.cleared, dagID) // keeps the DAG, records the clear
			return nil
		}
	}
	return ErrNotFound
}

type fakeRunRepo struct{ runs []domain.DagRun }

func (f *fakeRunRepo) ListDagRuns(context.Context, string, string, int, int) ([]domain.DagRun, int, error) {
	return f.runs, len(f.runs), nil
}
func (f *fakeRunRepo) GetDagRun(_ context.Context, _, _, runID string) (domain.DagRun, error) {
	for _, r := range f.runs {
		if r.RunID == runID {
			return r, nil
		}
	}
	return domain.DagRun{}, ErrNotFound
}
func (f *fakeRunRepo) SetDagRunState(_ context.Context, _, _, runID, state string) error {
	for i := range f.runs {
		if f.runs[i].RunID == runID {
			f.runs[i].State = domain.DagRunState(state)
		}
	}
	return nil
}
func (f *fakeRunRepo) DeleteDagRun(_ context.Context, _, _, runID string) error {
	for i := range f.runs {
		if f.runs[i].RunID == runID {
			f.runs = append(f.runs[:i], f.runs[i+1:]...)
			return nil
		}
	}
	return domain.ErrNotFound
}
func (f *fakeRunRepo) CreateDagRun(_ context.Context, _, dagID string, run domain.DagRun) (domain.DagRun, error) {
	run.DagID = dagID
	return run, nil
}

type fakeTaskRepo struct {
	lastClearOpts domain.ClearOptions
	clearCalled   bool
	tis           []domain.TaskInstance
	gotOnlyFailed bool
	setState      string
	setTaskID     string
}

func (f *fakeTaskRepo) ListTaskInstances(context.Context, string, string, string, int, int) ([]domain.TaskInstance, int, error) {
	return f.tis, len(f.tis), nil
}

// ListTaskInstanceAttempts returns the same TIs as ListTaskInstances filtered
// to the requested task — the fake has no history awareness; integration
// tests cover the multi-attempt UNION (see internal/storage TestLimaBug3).
func (f *fakeTaskRepo) ListTaskInstanceAttempts(_ context.Context, _, _, _, taskID string) ([]domain.TaskInstance, error) {
	out := make([]domain.TaskInstance, 0, len(f.tis))
	for _, ti := range f.tis {
		if ti.TaskID == taskID {
			out = append(out, ti)
		}
	}
	return out, nil
}
func (f *fakeTaskRepo) ClearTaskInstances(_ context.Context, _, _, _ string, _ []string, onlyFailed bool, opts domain.ClearOptions) (int, error) {
	f.lastClearOpts = opts
	f.clearCalled = true
	f.gotOnlyFailed = onlyFailed
	return len(f.tis), nil
}
func (f *fakeTaskRepo) SetTaskInstanceState(_ context.Context, _, _, _, taskID, state string) error {
	f.setTaskID, f.setState = taskID, state
	return nil
}

func dagOnlyServer(repo DagRepository) *gin.Engine {
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		Dags:          repo,
	})
}

func TestClearIncludePastFutureFansAcrossRuns(t *testing.T) {
	now := time.Now().UTC()
	runs := &fakeRunRepo{runs: []domain.DagRun{
		{DagID: "etl", RunID: "past", LogicalDate: now.Add(-2 * time.Hour)},
		{DagID: "etl", RunID: "cur", LogicalDate: now},
		{DagID: "etl", RunID: "future", LogicalDate: now.Add(2 * time.Hour)},
	}}
	// fakeTaskRepo returns its single TI for any run, so the affected count equals
	// the number of target runs the clear fans out to.
	tasks := &fakeTaskRepo{tis: []domain.TaskInstance{{TaskID: "extract", State: domain.TaskStateFailed}}}
	srv := NewServer(Dependencies{
		Logger: discardLogger(), Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter: auth.NewRateLimiter(100, time.Minute), CORSOrigins: []string{"*"}, TokenTTLSecs: 3600,
		Tasks: tasks, DagRuns: runs,
	})
	count := func(extra string) int {
		body := `{"dag_run_id":"cur","task_ids":["extract"],"dry_run":true` + extra + `}`
		rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/clearTaskInstances", body)
		var got struct {
			TotalEntries int `json:"total_entries"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		return got.TotalEntries
	}
	if n := count(""); n != 1 {
		t.Errorf("no flags: %d target runs, want 1 (cur only)", n)
	}
	if n := count(`,"include_past":true`); n != 2 {
		t.Errorf("include_past: %d, want 2 (cur+past)", n)
	}
	if n := count(`,"include_future":true`); n != 2 {
		t.Errorf("include_future: %d, want 2 (cur+future)", n)
	}
	if n := count(`,"include_past":true,"include_future":true`); n != 3 {
		t.Errorf("past+future: %d, want 3 (all runs)", n)
	}
}

// TestExpandClearTasksDispatch covers the wrapper that decides whether to
// expand seed task_ids along the DAG topology when the UI's Clear dialog has
// "include upstream/downstream" ticked. It is the gate between the public
// clear handler and expandTaskIDs; a regression here silently drops the flags
// or fails open into expanding when the spec lookup hiccups, both of which
// surface to the user as confusing partial clears.
func TestExpandClearTasksDispatch(t *testing.T) {
	tasks := []domain.TaskSpec{
		{TaskID: "a"},
		{TaskID: "b", DependsOn: []string{"a"}},
		{TaskID: "c", DependsOn: []string{"b"}},
	}
	specs := &fakeSpecReader{spec: domain.DAGSpec{Tasks: tasks}}
	mkCtx := func() *gin.Context {
		c := &gin.Context{}
		c.Request = httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/dags/etl/clearTaskInstances", http.NoBody)
		c.Params = gin.Params{{Key: "dag_id", Value: "etl"}}
		return c
	}

	t.Run("specs nil → seeds untouched", func(t *testing.T) {
		got := expandClearTasks(mkCtx(), nil, clearRequest{
			TaskIDs: clearTaskIDs{"b"}, IncludeUpstream: true, IncludeDownstream: true,
		})
		if len(got) != 1 || got[0] != "b" {
			t.Errorf("specs=nil: got %v, want [b]", got)
		}
	})

	t.Run("empty seeds → empty seeds (clear-whole-run)", func(t *testing.T) {
		got := expandClearTasks(mkCtx(), specs, clearRequest{IncludeUpstream: true, IncludeDownstream: true})
		if len(got) != 0 {
			t.Errorf("empty seeds: got %v, want []", got)
		}
	})

	t.Run("no flags → seeds returned unchanged (no DAG lookup)", func(t *testing.T) {
		got := expandClearTasks(mkCtx(), specs, clearRequest{TaskIDs: []string{"b"}})
		if len(got) != 1 || got[0] != "b" {
			t.Errorf("no flags: got %v, want [b]", got)
		}
	})

	t.Run("spec lookup error → seeds untouched (fail safe)", func(t *testing.T) {
		broken := &fakeSpecReader{err: errors.New("postgres briefly unreachable")}
		got := expandClearTasks(mkCtx(), broken, clearRequest{
			TaskIDs: []string{"b"}, IncludeDownstream: true,
		})
		if len(got) != 1 || got[0] != "b" {
			t.Errorf("spec error: got %v, want [b] (fail-safe seed-only)", got)
		}
	})

	t.Run("happy path → fans out downstream of seed", func(t *testing.T) {
		got := expandClearTasks(mkCtx(), specs, clearRequest{
			TaskIDs: []string{"a"}, IncludeDownstream: true,
		})
		set := map[string]bool{}
		for _, id := range got {
			set[id] = true
		}
		for _, id := range []string{"a", "b", "c"} {
			if !set[id] {
				t.Errorf("downstream(a) missing %s: %v", id, got)
			}
		}
	})
}

func TestExpandTaskIDs(t *testing.T) {
	// a -> b -> c (c depends on b, b depends on a), plus a sibling d off a.
	tasks := []domain.TaskSpec{
		{TaskID: "a"},
		{TaskID: "b", DependsOn: []string{"a"}},
		{TaskID: "c", DependsOn: []string{"b"}},
		{TaskID: "d", DependsOn: []string{"a"}},
	}
	set := func(ids []string) map[string]bool {
		m := map[string]bool{}
		for _, id := range ids {
			m[id] = true
		}
		return m
	}
	// seeds only.
	if got := set(expandTaskIDs(tasks, []string{"b"}, false, false)); len(got) != 1 || !got["b"] {
		t.Errorf("seeds-only = %v, want {b}", got)
	}
	// downstream of a = a,b,c,d (transitive).
	got := set(expandTaskIDs(tasks, []string{"a"}, false, true))
	for _, id := range []string{"a", "b", "c", "d"} {
		if !got[id] {
			t.Errorf("downstream(a) missing %s: %v", id, got)
		}
	}
	// upstream of c = c,b,a (transitive), not d.
	got = set(expandTaskIDs(tasks, []string{"c"}, true, false))
	if !got["a"] || !got["b"] || !got["c"] || got["d"] {
		t.Errorf("upstream(c) = %v, want {a,b,c}", got)
	}
}

func TestMarkDagRunState(t *testing.T) {
	runs := &fakeRunRepo{runs: []domain.DagRun{{DagID: "etl", RunID: "r1", State: domain.DagRunStateRunning}}}
	srv := NewServer(Dependencies{
		Logger: discardLogger(), Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter: auth.NewRateLimiter(100, time.Minute), CORSOrigins: []string{"*"}, TokenTTLSecs: 3600,
		DagRuns: runs,
	})
	rec := authGet(srv, http.MethodPatch, "/api/v2/dags/etl/dagRuns/r1", `{"state":"failed"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("mark run = %d (%s)", rec.Code, rec.Body.String())
	}
	if runs.runs[0].State != domain.DagRunStateFailed {
		t.Errorf("run state = %q, want failed", runs.runs[0].State)
	}
	if rec := authGet(srv, http.MethodPatch, "/api/v2/dags/etl/dagRuns/r1", `{"state":"banana"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid run state = %d, want 400", rec.Code)
	}
}

func TestMarkTaskInstanceState(t *testing.T) {
	tasks := &fakeTaskRepo{tis: []domain.TaskInstance{{TaskID: "extract", RunID: "r1", State: domain.TaskStateFailed}}}
	srv := NewServer(Dependencies{
		Logger: discardLogger(), Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter: auth.NewRateLimiter(100, time.Minute), CORSOrigins: []string{"*"}, TokenTTLSecs: 3600,
		Tasks: tasks, DagRuns: &fakeRunRepo{runs: []domain.DagRun{{DagID: "etl", RunID: "r1"}}},
	})
	// Real PATCH applies the state.
	rec := authGet(srv, http.MethodPatch, "/api/v2/dags/etl/dagRuns/r1/taskInstances/extract/-1", `{"new_state":"success"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("mark = %d (%s)", rec.Code, rec.Body.String())
	}
	if tasks.setTaskID != "extract" || tasks.setState != "success" {
		t.Errorf("expected set extract->success, got %q->%q", tasks.setTaskID, tasks.setState)
	}
	// dry_run must NOT apply (the recorded state stays from the prior call).
	tasks.setState = ""
	rec = authGet(srv, http.MethodPatch, "/api/v2/dags/etl/dagRuns/r1/taskInstances/extract/dry_run", `{"new_state":"failed"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("dry_run = %d (%s)", rec.Code, rec.Body.String())
	}
	if tasks.setState != "" {
		t.Errorf("dry_run must not change state, but set %q", tasks.setState)
	}
	// An invalid state is rejected.
	if rec := authGet(srv, http.MethodPatch, "/api/v2/dags/etl/dagRuns/r1/taskInstances/extract/-1", `{"new_state":"banana"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid new_state = %d, want 400", rec.Code)
	}
}

func TestClearTaskInstancesDryRun(t *testing.T) {
	tasks := &fakeTaskRepo{tis: []domain.TaskInstance{
		{TaskID: "extract", RunID: "r1", State: domain.TaskStateFailed},
		{TaskID: "load", RunID: "r1", State: domain.TaskStateSuccess},
	}}
	srv := NewServer(Dependencies{
		Logger: discardLogger(), Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter: auth.NewRateLimiter(100, time.Minute), CORSOrigins: []string{"*"}, TokenTTLSecs: 3600,
		Tasks: tasks, DagRuns: &fakeRunRepo{runs: []domain.DagRun{{DagID: "etl", RunID: "r1"}}},
	})
	rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/clearTaskInstances", `{"dag_run_id":"r1","only_failed":true,"dry_run":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("dry_run clear = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		TaskInstances []map[string]any `json:"task_instances"`
		TotalEntries  int              `json:"total_entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// Only the failed task is affected, and dry_run did not actually clear.
	if got.TotalEntries != 1 || got.TaskInstances[0]["task_id"] != "extract" {
		t.Errorf("dry_run affected = %+v, want [extract]", got.TaskInstances)
	}
}

func authedServer() *gin.Engine {
	admin := &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}
	sched := "0 5 * * *"
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: admin},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		TokenTTLSecs:  3600,
		Dags:          &fakeDagRepo{dags: []domain.DAG{{DagID: "etl", Owner: "data", Schedule: &sched, Tags: []string{"x"}}}},
		DagRuns:       &fakeRunRepo{runs: []domain.DagRun{{DagID: "etl", RunID: "r1", State: domain.DagRunStateQueued}}},
		Tasks:         &fakeTaskRepo{tis: []domain.TaskInstance{{TaskID: "extract", RunID: "r1", State: domain.TaskStateQueued}}},
	})
}

func authGet(srv *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequestWithContext(context.Background(), method, path, http.NoBody)
	} else {
		r = httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Authorization", "Bearer token")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	return rec
}

func TestDeleteDagRun(t *testing.T) {
	admin := &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}
	runs := &fakeRunRepo{runs: []domain.DagRun{{DagID: "etl", RunID: "r1", State: domain.DagRunStateSuccess}}}
	srv := NewServer(Dependencies{
		Logger: discardLogger(), Authenticator: &fakeAuthn{user: admin},
		RateLimiter: auth.NewRateLimiter(100, time.Minute), CORSOrigins: []string{"*"}, TokenTTLSecs: 3600,
		DagRuns: runs,
	})
	// The UI's "delete run" must succeed (204), not 404 as "API route not found".
	rec := authGet(srv, http.MethodDelete, "/api/v2/dags/etl/dagRuns/r1", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE existing run = %d, want 204; body=%q", rec.Code, rec.Body.String())
	}
	if len(runs.runs) != 0 {
		t.Errorf("run was not removed: %+v", runs.runs)
	}
	// A missing run is a clean 404, not a silent 204.
	if rec := authGet(srv, http.MethodDelete, "/api/v2/dags/etl/dagRuns/nope", ""); rec.Code != http.StatusNotFound {
		t.Errorf("DELETE missing run = %d, want 404", rec.Code)
	}
}

func TestListDagRunsStateFilter(t *testing.T) {
	// The "failed runs" widget filters with ?state=failed; the handler must honor
	// it (it previously ignored the filter and returned every run).
	admin := &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}
	srv := NewServer(Dependencies{
		Logger: discardLogger(), Authenticator: &fakeAuthn{user: admin},
		RateLimiter: auth.NewRateLimiter(100, time.Minute), CORSOrigins: []string{"*"}, TokenTTLSecs: 3600,
		DagRuns: &fakeRunRepo{runs: []domain.DagRun{
			{DagID: "etl", RunID: "r1", State: domain.DagRunStateSuccess},
			{DagID: "etl", RunID: "r2", State: domain.DagRunStateFailed},
			{DagID: "etl", RunID: "r3", State: domain.DagRunStateSuccess},
		}},
	})
	decode := func(rec *httptest.ResponseRecorder) int {
		var got struct {
			TotalEntries int `json:"total_entries"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return got.TotalEntries
	}
	if n := decode(authGet(srv, http.MethodGet, "/api/v2/dags/etl/dagRuns?state=failed", "")); n != 1 {
		t.Errorf("state=failed total = %d, want 1", n)
	}
	if n := decode(authGet(srv, http.MethodGet, "/api/v2/dags/etl/dagRuns?state=success", "")); n != 2 {
		t.Errorf("state=success total = %d, want 2", n)
	}
	if n := decode(authGet(srv, http.MethodGet, "/api/v2/dags/etl/dagRuns", "")); n != 3 {
		t.Errorf("no filter total = %d, want 3", n)
	}
}

func TestDeleteDagClearsHistoryByDefault(t *testing.T) {
	// The trash button (plain DELETE) clears history but KEEPS the DAG (ADR 0020),
	// because a GitOps DAG would not reload after a destructive delete.
	repo := &fakeDagRepo{dags: []domain.DAG{{DagID: "etl"}}}
	srv := dagOnlyServer(repo)
	rec := authGet(srv, http.MethodDelete, "/api/v2/dags/etl", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("clear = %d (%s)", rec.Code, rec.Body.String())
	}
	if len(repo.cleared) != 1 || repo.cleared[0] != "etl" {
		t.Errorf("expected a clear of etl, got %v", repo.cleared)
	}
	// The DAG still exists, so a second clear also succeeds (not 404).
	if rec := authGet(srv, http.MethodDelete, "/api/v2/dags/etl", ""); rec.Code != http.StatusNoContent {
		t.Errorf("second clear = %d, want 204 (dag still registered)", rec.Code)
	}
	if len(repo.dags) != 1 {
		t.Errorf("clear must keep the DAG registered, got %d dags", len(repo.dags))
	}
}

func TestDeleteDagDeregisterRemoves(t *testing.T) {
	repo := &fakeDagRepo{dags: []domain.DAG{{DagID: "etl"}}}
	srv := dagOnlyServer(repo)
	rec := authGet(srv, http.MethodDelete, "/api/v2/dags/etl?deregister=true", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("deregister = %d (%s)", rec.Code, rec.Body.String())
	}
	if len(repo.dags) != 0 {
		t.Errorf("deregister must remove the DAG, got %d dags", len(repo.dags))
	}
	// Now it is gone -> 404.
	if rec := authGet(srv, http.MethodDelete, "/api/v2/dags/etl?deregister=true", ""); rec.Code != http.StatusNotFound {
		t.Errorf("deregister missing dag = %d, want 404", rec.Code)
	}
}

func TestListDags(t *testing.T) {
	rec := authGet(authedServer(), http.MethodGet, "/api/v2/dags", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list dags = %d (%s)", rec.Code, rec.Body.String())
	}
	var col dagCollectionDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &col); err != nil {
		t.Fatal(err)
	}
	if col.TotalEntries != 1 || len(col.Dags) != 1 || col.Dags[0].DagID != "etl" {
		t.Errorf("unexpected collection: %+v", col)
	}
	if col.Dags[0].ScheduleInterval == nil || col.Dags[0].ScheduleInterval.Value != "0 5 * * *" {
		t.Errorf("schedule interval not translated: %+v", col.Dags[0].ScheduleInterval)
	}
}

func TestGetDagFoundAndNotFound(t *testing.T) {
	srv := authedServer()
	if rec := authGet(srv, http.MethodGet, "/api/v2/dags/etl", ""); rec.Code != http.StatusOK {
		t.Errorf("get etl = %d, want 200", rec.Code)
	}
	if rec := authGet(srv, http.MethodGet, "/api/v2/dags/missing", ""); rec.Code != http.StatusNotFound {
		t.Errorf("get missing = %d, want 404", rec.Code)
	}
}

func TestPatchDagPause(t *testing.T) {
	rec := authGet(authedServer(), http.MethodPatch, "/api/v2/dags/etl", `{"is_paused":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch = %d", rec.Code)
	}
	var d dagDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if !d.IsPaused {
		t.Error("is_paused should be true after patch")
	}
}

func TestDagRunsListAndCreate(t *testing.T) {
	srv := authedServer()
	if rec := authGet(srv, http.MethodGet, "/api/v2/dags/etl/dagRuns", ""); rec.Code != http.StatusOK {
		t.Errorf("list runs = %d", rec.Code)
	}
	rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/dagRuns", `{"dag_run_id":"manual__1"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create run = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	var run dagRunDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if run.DagRunID != "manual__1" || run.State != "queued" || run.RunType != "manual" {
		t.Errorf("unexpected run: %+v", run)
	}
	// dag_versions is a required array the run view maps over; a missing/nil one
	// crashes the UI with "undefined.map".
	if run.DagVersions == nil {
		t.Error("dag run response must include dag_versions (array), got nil")
	}
}

func TestTaskInstancesWildcardRunReturnsEmpty(t *testing.T) {
	// The overview polls dagRuns/~/taskInstances (all runs); must degrade to an
	// empty collection (200), not 404.
	rec := authGet(authedServer(), http.MethodGet, "/api/v2/dags/etl/dagRuns/~/taskInstances", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("~/taskInstances = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"total_entries":0`) {
		t.Errorf("~/taskInstances should be empty, got %s", rec.Body.String())
	}
}

func TestDagRunsWildcardReturnsEmptyCollection(t *testing.T) {
	// The UI home polls /api/v2/dags/~/dagRuns (all DAGs). It must degrade to an
	// empty collection (200), not 404 — "~" is not a real DAG.
	rec := authGet(authedServer(), http.MethodGet, "/api/v2/dags/~/dagRuns", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("~/dagRuns = %d, want 200", rec.Code)
	}
	var col dagRunCollectionDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &col); err != nil {
		t.Fatal(err)
	}
	if col.TotalEntries != 0 || len(col.DagRuns) != 0 {
		t.Errorf("~/dagRuns should be empty, got %+v", col)
	}
}

func TestDagRunCreateGeneratesRunID(t *testing.T) {
	rec := authGet(authedServer(), http.MethodPost, "/api/v2/dags/etl/dagRuns", `{}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create run with empty body = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	var run dagRunDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(run.DagRunID, "manual__") {
		t.Errorf("an unspecified dag_run_id should be auto-generated, got %q", run.DagRunID)
	}
}

func TestTaskInstancesAndClear(t *testing.T) {
	srv := authedServer()
	if rec := authGet(srv, http.MethodGet, "/api/v2/dags/etl/dagRuns/r1/taskInstances", ""); rec.Code != http.StatusOK {
		t.Errorf("list task instances = %d", rec.Code)
	}
	rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/clearTaskInstances", `{"task_ids":["extract"],"dag_run_id":"r1"}`)
	if rec.Code != http.StatusOK {
		t.Errorf("clear = %d, want 200", rec.Code)
	}
}

// TestClearAcceptsAirflowTupleTaskIDs is a CONTRACT test against the REAL Airflow
// 3.2 UI payload: the SPA sends task_ids as [task_id, map_index] tuples (captured
// from the bundle as task_ids:[[...]]), not plain strings. The clear endpoint must
// accept that shape. The prior []string DTO 400'd on it — and the older tests used
// our own invented `["extract"]` shape, so they never caught the mismatch (issue
// #98). This test posts the real shape, so it fails if the contract breaks again.
func TestClearAcceptsAirflowTupleTaskIDs(t *testing.T) {
	srv := authedServer()
	// Real Airflow shape: each task_ids element is a [task_id, map_index] tuple.
	rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/clearTaskInstances",
		`{"dag_run_id":"r1","task_ids":[["extract",-1]],"dry_run":true}`)
	if rec.Code == http.StatusBadRequest {
		t.Fatalf("the real Airflow tuple payload must bind, not 400: %s", rec.Body.String())
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("clear (tuple task_ids) = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	// Backward compatibility: plain-string task_ids still bind.
	if rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/clearTaskInstances",
		`{"dag_run_id":"r1","task_ids":["extract"]}`); rec.Code != http.StatusOK {
		t.Errorf("string task_ids should still bind, got %d", rec.Code)
	}
}

// TestTaskInstanceActionMapIndexSubresources is a CONTRACT test against the real
// Airflow 3.2 SPA, captured live from a browser: right after a Clear, the UI
// requests the attempts list as "{map_index}/tries" (e.g. "-1/tries"). The
// catch-all only knew "tries"/"tries/{n}", so "-1/tries" fell through to
// strconv.Atoi and 400'd with the misleading "map_index must be an integer".
// This asserts the real URL shape works and that a genuinely unknown action gets
// a clear 404 — never that misleading 400.
func TestTaskInstanceActionMapIndexSubresources(t *testing.T) {
	srv := authedServer()
	base := "/api/v2/dags/etl/dagRuns/r1/taskInstances/extract"

	// The exact shape the SPA sends after a Clear (was a 400 in the wild).
	if rec := authGet(srv, http.MethodGet, base+"/-1/tries", ""); rec.Code != http.StatusOK {
		t.Errorf("GET {map_index}/tries = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	// The bare map_index still returns the single instance (the fake's TI is at
	// map_index 0).
	if rec := authGet(srv, http.MethodGet, base+"/0", ""); rec.Code != http.StatusOK {
		t.Errorf("GET {map_index} = %d, want 200", rec.Code)
	}
	// A genuinely unknown action must NOT masquerade as a map_index error.
	rec := authGet(srv, http.MethodGet, base+"/dependencies", "")
	if strings.Contains(rec.Body.String(), "map_index must be an integer") {
		t.Errorf("unknown action misreported as a map_index error: %s", rec.Body.String())
	}
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown action = %d, want 404", rec.Code)
	}
}

// TestHandleRepoErrorClientCancel guards the ti_summaries 500 regression: a
// client that aborted (the UI supersedes in-flight grid requests) must map to 499
// (client closed), not a 500 server fault.
//
// `gone` is what distinguishes the scenario from the mechanism, and the
// distinction is the whole of #1071. These cases used to run with a healthy
// request context and assert 499 purely from the error's chain — which is also
// what a pgconn connect timeout looks like, so a database outage was reported to
// the tenant as their own disconnect. A client that went away has a done
// context; a dead database does not.
func TestHandleRepoErrorClientCancel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name string
		err  error
		gone bool // the caller really did hang up: its request context is done
		want int
	}{
		{"client canceled", context.Canceled, true, statusClientClosedRequest},
		{"deadline exceeded", context.DeadlineExceeded, true, statusClientClosedRequest},
		{"wrapped cancel", errors.Join(errors.New("task instances for runs"), context.Canceled), true, statusClientClosedRequest},
		// The same error values with the caller still connected: a database that
		// cannot be reached, which is a server fault and must alert as one.
		{"unreachable database (client still there)", context.DeadlineExceeded, false, http.StatusInternalServerError},
		{"connect timeout wrapped by the driver", fmt.Errorf("failed to connect to `user=leoflow database=leoflow`: timeout: %w", context.DeadlineExceeded), false, http.StatusInternalServerError},
		{"not found", ErrNotFound, false, http.StatusNotFound},
		{"conflict (duplicate run)", domain.ErrConflict, false, http.StatusConflict},
		{"wrapped conflict", fmt.Errorf("creating dag run: %w", domain.ErrConflict), false, http.StatusConflict},
		{"real server error", errors.New("db exploded"), false, http.StatusInternalServerError},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		ctx, cancel := context.WithCancel(context.Background())
		if tc.gone {
			cancel()
		}
		c.Request = httptest.NewRequestWithContext(ctx, http.MethodGet, "/x", http.NoBody)
		handleRepoError(c, tc.err)
		cancel()
		if w.Code != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, w.Code, tc.want)
		}
	}
}

func TestClearOnlyFailedForwarded(t *testing.T) {
	admin := &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}
	repo := &fakeTaskRepo{tis: []domain.TaskInstance{{TaskID: "extract", RunID: "r1", State: domain.TaskStateFailed}}}
	srv := NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: admin},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		Tasks:         repo,
	})

	// dry_run is explicit here because an omitted one previews (#1137), and a
	// preview never reaches the repository, so every assertion below would be
	// vacuous without it.
	rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/clearTaskInstances",
		`{"dag_run_id":"r1","only_failed":true,"dry_run":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear only_failed = %d (%s)", rec.Code, rec.Body.String())
	}
	if !repo.gotOnlyFailed {
		t.Error("only_failed=true not forwarded to repository")
	}

	// This half used to assert the opposite, locking leoflow's divergence from
	// Airflow in place: an omitted only_failed cleared every named task instance,
	// successful ones included. Airflow 3.2.1 declares only_failed=True, and the
	// assertion is inverted rather than deleted so the default stays pinned.
	repo.gotOnlyFailed = false // ensure the next call actually flips it
	rec = authGet(srv, http.MethodPost, "/api/v2/dags/etl/clearTaskInstances",
		`{"task_ids":["extract"],"dag_run_id":"r1","dry_run":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear default = %d", rec.Code)
	}
	if !repo.gotOnlyFailed {
		t.Error("only_failed should default to true when omitted, as Airflow does")
	}
}

func TestXComStub(t *testing.T) {
	rec := authGet(authedServer(), http.MethodGet, "/api/v2/xcoms/etl/r1/extract/key", "")
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("xcom stub = %d, want 501", rec.Code)
	}
}

func TestListDagsLinkHeader(t *testing.T) {
	admin := &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}
	srv := NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: admin},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		Dags:          &fakeDagRepo{dags: []domain.DAG{{DagID: "a"}, {DagID: "b"}}},
	})
	rec := authGet(srv, http.MethodGet, "/api/v2/dags?limit=1&offset=0", "")
	if link := rec.Header().Get("Link"); !strings.Contains(link, `rel="next"`) {
		t.Errorf("Link = %q, want a next relation", link)
	}
}

// TestTaskInstanceDTOCarriesFailureReason locks the new, ADDITIVE field: a failed
// attempt whose agent never streamed a line must still tell an operator why,
// without them needing cluster access.
func TestTaskInstanceDTOCarriesFailureReason(t *testing.T) {
	const reason = "the control plane rejected this pod's projected ServiceAccount token."
	dto := toTaskInstanceDTO(domain.TaskInstance{
		TaskID: "extract", DagID: "etl", RunID: "r1", MapIndex: -1,
		State: domain.TaskStateFailed, FailureReason: reason,
	})
	if dto.FailureReason == nil {
		t.Fatal("a failed instance with a recorded cause must expose failure_reason")
	}
	if *dto.FailureReason != reason {
		t.Errorf("failure_reason = %q, want %q", *dto.FailureReason, reason)
	}
}

// TestTaskInstanceDTOFailureReasonNullWhenUnknown keeps the field honest: a
// healthy instance, or a failure nobody observed, must serialize null rather
// than an invented cause.
func TestTaskInstanceDTOFailureReasonNullWhenUnknown(t *testing.T) {
	dto := toTaskInstanceDTO(domain.TaskInstance{
		TaskID: "extract", State: domain.TaskStateSuccess,
	})
	if dto.FailureReason != nil {
		t.Errorf("failure_reason = %q, want null for an instance with no recorded cause", *dto.FailureReason)
	}
}

// TestTaskInstanceResponseStaysAirflowCompatible is the contract tripwire: the
// new field must be purely additive. Every field the Airflow 3.2.x
// TaskInstanceResponse requires has to remain present with its existing name, or
// the SPA breaks on a data contract it cannot negotiate.
func TestTaskInstanceResponseStaysAirflowCompatible(t *testing.T) {
	dto := toTaskInstanceDTO(domain.TaskInstance{
		TaskID: "extract", DagID: "etl", RunID: "r1", MapIndex: -1,
		State: domain.TaskStateFailed, FailureReason: "boom",
	})
	raw, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"id", "task_id", "dag_id", "dag_run_id", "map_index", "logical_date",
		"start_date", "end_date", "duration", "state", "try_number", "max_tries",
		"task_display_name", "dag_display_name", "hostname", "unixname", "pool",
		"pool_slots", "queue", "priority_weight", "operator", "queued_when",
		"pid", "executor", "executor_config", "note", "rendered_fields",
		"rendered_map_index", "trigger", "triggerer_job", "dag_version",
	} {
		if _, ok := got[required]; !ok {
			t.Errorf("Airflow-required field %q disappeared from the task instance response", required)
		}
	}
	if _, ok := got["failure_reason"]; !ok {
		t.Error("failure_reason must be present on the response")
	}
}

// clearSrv builds a server whose task repo records the ClearOptions it received,
// so the tests below assert what the handler decided rather than what it printed.
func clearSrv(t *testing.T) (*gin.Engine, *fakeTaskRepo) {
	t.Helper()
	tasks := &fakeTaskRepo{tis: []domain.TaskInstance{
		{TaskID: "extract", RunID: "r1", State: domain.TaskStateFailed},
	}}
	srv := NewServer(Dependencies{
		Logger: discardLogger(), Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter: auth.NewRateLimiter(100, time.Minute), CORSOrigins: []string{"*"}, TokenTTLSecs: 3600,
		Tasks: tasks, DagRuns: &fakeRunRepo{runs: []domain.DagRun{{DagID: "etl", RunID: "r1"}}},
	})
	return srv, tasks
}

// TestClearRunOnLatestVersion: which version a cleared run re-executes is now a
// request-level decision, separate from whether the run is re-opened.
//
// Before this, the two were one boolean: re-opening a run always re-bound it to
// the DAG's current version, so there was no way to re-run a task against the
// image that produced it — "clear last week's task" always ran today's code.
// Apache Airflow separates them the same way and calls the second
// `run_on_latest_version`.
func TestClearRunOnLatestVersion(t *testing.T) {
	cases := []struct {
		name string
		body string
		want domain.ClearOptions
	}{{
		// Airflow's default: a clear reproduces the attempt it is clearing. Testing
		// a fix is a new run, or an explicit run_on_latest_version=true.
		name: "omitted pins the run, as Airflow does",
		body: `{"dag_run_id":"r1","dry_run":false}`,
		want: domain.ClearOptions{ResetDagRun: true, RunOnLatestVersion: false},
	}, {
		name: "false pins the run to the version it was created with",
		body: `{"dag_run_id":"r1","run_on_latest_version":false,"dry_run":false}`,
		want: domain.ClearOptions{ResetDagRun: true, RunOnLatestVersion: false},
	}, {
		name: "true is explicit and unchanged",
		body: `{"dag_run_id":"r1","run_on_latest_version":true,"dry_run":false}`,
		want: domain.ClearOptions{ResetDagRun: true, RunOnLatestVersion: true},
	}, {
		// The two decisions are independent: not re-opening the run says nothing
		// about which version a later re-open would use.
		name: "it is independent of reset_dag_runs",
		body: `{"dag_run_id":"r1","reset_dag_runs":false,"run_on_latest_version":false,"dry_run":false}`,
		want: domain.ClearOptions{ResetDagRun: false, RunOnLatestVersion: false},
	}}

	// Every body carries dry_run=false: these cases assert what reaches the
	// repository as ClearOptions, and since #1137 an omitted dry_run previews and
	// reaches nothing. The premise check below catches that, which is how this
	// showed up.
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, tasks := clearSrv(t)
			rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/clearTaskInstances", c.body)
			if rec.Code != http.StatusOK {
				t.Fatalf("clear = %d (%s)", rec.Code, rec.Body.String())
			}
			if !tasks.clearCalled {
				t.Fatal("the repository was never asked to clear; the assertion below would be vacuous")
			}
			if tasks.lastClearOpts != c.want {
				t.Errorf("ClearOptions = %+v, want %+v", tasks.lastClearOpts, c.want)
			}
		})
	}
}

// TestClearDryRunDecidesNothing: a preview must not reach the repository at all.
// Recording the options makes it possible to assert that, rather than inferring
// it from an unchanged state field.
func TestClearDryRunDecidesNothing(t *testing.T) {
	srv, tasks := clearSrv(t)
	rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/clearTaskInstances",
		`{"dag_run_id":"r1","dry_run":true,"run_on_latest_version":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("dry_run = %d (%s)", rec.Code, rec.Body.String())
	}
	if tasks.clearCalled {
		t.Error("dry_run reached the repository")
	}
}

// TestDagRunDTOCarriesBundleVersion: the run's pinned version must reach the
// wire as bundle_version.
//
// This is not cosmetic. The embedded SPA renders the clear dialog's "Run with
// latest bundle version" checkbox only when the run's bundle_version differs
// from the DAG's AND the run's is neither null nor empty:
//
//	ee = F !== I && I !== null && I !== ``     (F = the DAG's, I = the run's)
//
// leoflow sent null, so the control never rendered. With the server now honoring
// run_on_latest_version, a null here means the operator cannot ask for the
// current version through any interface at all — there is no CLI clear either.
func TestDagRunDTOCarriesBundleVersion(t *testing.T) {
	t.Run("a pinned version is serialized", func(t *testing.T) {
		dto := toDagRunDTO(domain.DagRun{DagID: "etl", RunID: "r1", Version: "v1.2.3"})
		if dto.BundleVersion == nil {
			t.Fatal("bundle_version is null; the SPA hides the version control when it is")
		}
		if *dto.BundleVersion != "v1.2.3" {
			t.Errorf("bundle_version = %q, want the run's pinned version", *dto.BundleVersion)
		}
	})
	t.Run("an unresolvable version stays null rather than empty", func(t *testing.T) {
		// The SPA treats "" the same as null, so an empty string would be a
		// pointless non-null. A run whose version row is gone is the real case.
		dto := toDagRunDTO(domain.DagRun{DagID: "etl", RunID: "r1"})
		if dto.BundleVersion != nil {
			t.Errorf("bundle_version = %q, want null", *dto.BundleVersion)
		}
	})
}

// TestDagDTOCarriesCurrentVersion: the other half of the comparison. With only
// the run's version populated the checkbox would render unconditionally, because
// a null on the DAG side always differs from a non-null run version.
func TestDagDTOCarriesCurrentVersion(t *testing.T) {
	dto := toDagWithRunsDTO(domain.DAG{DagID: "etl", CurrentVersion: "v2.0.0"}, nil)
	if dto.BundleVersion == nil {
		t.Fatal("bundle_version is null on the DAG; the clear dialog then always offers the toggle")
	}
	if *dto.BundleVersion != "v2.0.0" {
		t.Errorf("bundle_version = %q, want the DAG's current version", *dto.BundleVersion)
	}
}

// TestTaskInstanceDagVersionCarriesTheRunsPinnedLabel: the single-task clear
// dialog — the ordinary "my task failed, clear it" path — gates its version
// control on
//
//	he !== ge && ge !== null && ge !== ''
//
// where he is the DAG's current label and ge is the task instance's
// dag_version.bundle_version. The literal that builds that nested object omitted
// BundleVersion, so ge was null and the control never rendered.
//
// The label must come from the RUN's pinned version, not from the DAG's latest.
// Filling it from the latest makes he === ge, so the control stays hidden — the
// same invisible outcome as null, reached a different way, and the obvious fix.
func TestTaskInstanceDagVersionCarriesTheRunsPinnedLabel(t *testing.T) {
	runs := &fakeRunRepo{runs: []domain.DagRun{{DagID: "etl", RunID: "r1", Version: "v1-pinned"}}}
	tasks := &fakeTaskRepo{tis: []domain.TaskInstance{
		{TaskID: "extract", RunID: "r1", State: domain.TaskStateFailed},
	}}
	srv := NewServer(Dependencies{
		Logger: discardLogger(), Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter: auth.NewRateLimiter(100, time.Minute), CORSOrigins: []string{"*"}, TokenTTLSecs: 3600,
		Tasks: tasks, DagRuns: runs,
		DagVersions: &fakeVersionLister{versions: []domain.DagVersion{
			// The DAG's latest is deliberately different from the run's pinned
			// label: if the DTO followed this instead, the two would match and the
			// dialog would hide the control.
			{ID: "v-uuid", VersionNumber: 2, CreatedAt: time.Now().UTC(), Version: "v2-latest"},
		}},
	})
	rec := authGet(srv, http.MethodGet, "/api/v2/dags/etl/dagRuns/r1/taskInstances", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("taskInstances = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		TaskInstances []struct {
			DagVersion *struct {
				BundleVersion *string `json:"bundle_version"`
			} `json:"dag_version"`
		} `json:"task_instances"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.TaskInstances) == 0 {
		t.Fatal("no task instances returned; the assertions below would be vacuous")
	}
	dv := got.TaskInstances[0].DagVersion
	if dv == nil || dv.BundleVersion == nil {
		t.Fatalf("dag_version.bundle_version is null; the single-task clear dialog hides its version control: %s", rec.Body.String())
	}
	if *dv.BundleVersion == "v2-latest" {
		t.Fatal("dag_version.bundle_version followed the DAG's LATEST version; it must be the run's pinned one, or it equals the DAG's and the control stays hidden anyway")
	}
	if *dv.BundleVersion != "v1-pinned" {
		t.Errorf("dag_version.bundle_version = %q, want the run's pinned v1-pinned", *dv.BundleVersion)
	}

	// The clear endpoint builds its own affected/dry-run payload through a SECOND
	// resolver (resolveRunContextFor, which takes the run from the body rather
	// than the path). It is a separate copy of the same two lines, and mutating
	// only that copy left the suite green — an untested duplicate of code this
	// test exists to prove matters.
	rec = authGet(srv, http.MethodPost, "/api/v2/dags/etl/clearTaskInstances",
		`{"dag_run_id":"r1","task_ids":["extract"],"dry_run":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear dry_run = %d (%s)", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.TaskInstances) == 0 {
		t.Fatal("the clear preview returned nothing; the assertion below would be vacuous")
	}
	dv = got.TaskInstances[0].DagVersion
	if dv == nil || dv.BundleVersion == nil {
		t.Fatalf("the clear payload's dag_version.bundle_version is null: %s", rec.Body.String())
	}
	if *dv.BundleVersion != "v1-pinned" {
		t.Errorf("clear payload dag_version.bundle_version = %q, want v1-pinned", *dv.BundleVersion)
	}
}

// pauseRecorder records SetPaused calls.
type pauseRecorder struct {
	fakeDagRepo
	calls int
}

func (p *pauseRecorder) SetPaused(ctx context.Context, tenant, dagID string, paused bool) (domain.DAG, error) {
	p.calls++
	return p.fakeDagRepo.SetPaused(ctx, tenant, dagID, paused)
}

// TestPatchDagWithoutIsPausedIsRefused: a PATCH that does not say whether to
// pause must not be read as is_paused=false, which would unpause a scheduled
// DAG (and maybe start a catch-up) for a request that asked for something
// else. The route grants only pause and unpause, so anything else is a 400.
func TestPatchDagWithoutIsPausedIsRefused(t *testing.T) {
	for _, body := range []string{`{}`, `{"is_paused":null}`, `{"description":"x"}`} {
		repo := &pauseRecorder{fakeDagRepo: fakeDagRepo{dags: []domain.DAG{{DagID: "etl", IsPaused: true}}}}
		srv := NewServer(Dependencies{
			Logger:        discardLogger(),
			Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
			RateLimiter:   auth.NewRateLimiter(100, time.Minute),
			Dags:          repo,
		})

		rec := authGet(srv, http.MethodPatch, "/api/v2/dags/etl", body)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("PATCH %s = %d (%s), want 400", body, rec.Code, rec.Body.String())
		}
		if repo.calls != 0 {
			t.Errorf("PATCH %s called SetPaused %d times, want 0", body, repo.calls)
		}
	}
}
