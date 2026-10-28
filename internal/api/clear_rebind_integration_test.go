//go:build integration

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
)

// clearGuardFixture is a DAG whose run r1 failed on version 1, with version 2
// registered since, behind the API over a real repository.
type clearGuardFixture struct {
	srv     *gin.Engine
	pg      *storage.Postgres
	repo    *storage.Repository
	dagID   string
	runUUID string
}

func clearGuardServer(repo *storage.Repository, tenant string) *gin.Engine {
	return newTestServer(Dependencies{
		Observability: ObservabilityDeps{
			Logger: discardLogger(),
		},
		Server: ServerDeps{
			CORSOrigins: []string{"*"},
		},
		Auth: AuthDeps{
			Authenticator: &fakeAuthn{user: &auth.User{ID: "u-" + tenant, TenantID: tenant, Roles: []string{"admin"}}},
			RateLimiter:   auth.NewRateLimiter(1000, time.Minute),
			TokenTTLSecs:  3600,
		},
		Stores: StoreDeps{
			Dags:        repo,
			DagRuns:     repo,
			Tasks:       repo,
			Versions:    repo,
			Specs:       repo,
			DagVersions: repo,
		},
	})
}

func newClearGuardFixture(t *testing.T, v1Tasks, v2Tasks []string) clearGuardFixture {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must point at a migrated database for integration tests")
	}
	ctx := context.Background()
	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: url})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pg.Close)
	repo := storage.NewRepository(pg)
	f := clearGuardFixture{srv: clearGuardServer(repo, "default"), pg: pg, repo: repo,
		dagID: fmt.Sprintf("api_clearguard_%d", time.Now().UnixNano())}
	f.register(t, "v1", v1Tasks)
	if _, err = repo.CreateDagRun(ctx, "default", f.dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err = pg.Pool.QueryRow(ctx, `SELECT r.id::text FROM dag_runs r JOIN dags d ON d.id = r.dag_id
		WHERE d.dag_id = $1 AND r.run_id = 'r1'`, f.dagID).Scan(&f.runUUID); err != nil {
		t.Fatal(err)
	}
	sched := storage.NewSchedulerStore(pg)
	specs := make([]domain.TaskSpec, 0, len(v1Tasks))
	for _, id := range v1Tasks {
		specs = append(specs, domain.TaskSpec{TaskID: id, Type: domain.TaskTypePython})
	}
	if err = sched.MaterializeTasks(ctx, f.runUUID, specs); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	for _, id := range v1Tasks {
		if err = sched.ApplyTransition(ctx, f.runUUID, id, domain.TaskStateFailed); err != nil {
			t.Fatalf("fail %s: %v", id, err)
		}
	}
	if _, err = pg.Pool.Exec(ctx, `UPDATE dag_runs SET state = 'failed', ended_at = now() WHERE id = $1::uuid`, f.runUUID); err != nil {
		t.Fatal(err)
	}
	f.register(t, "v2", v2Tasks)
	return f
}

func (f clearGuardFixture) register(t *testing.T, version string, tasks []string) {
	t.Helper()
	parts := make([]string, 0, len(tasks))
	for _, id := range tasks {
		parts = append(parts, fmt.Sprintf(`{"task_id":%q,"type":"python","entrypoint":"dag:%s"}`, id, id))
	}
	spec := fmt.Sprintf(`{"schema_version":"1.0","dag_id":%q,"dag_version":%q,"image":"img:%s","tasks":[%s]}`,
		f.dagID, version, version, strings.Join(parts, ","))
	if rec := authGet(f.srv, http.MethodPost, "/api/v2/dags/"+f.dagID+"/versions", spec); rec.Code != http.StatusCreated {
		t.Fatalf("register %s = %d (%s)", version, rec.Code, rec.Body.String())
	}
}

// state reads the run's state, the image of its version and how many of its
// task instances are no longer failed.
func (f clearGuardFixture) state(t *testing.T) (run, image string, reset int) {
	t.Helper()
	if err := f.pg.Pool.QueryRow(context.Background(), `
		SELECT r.state::text, v.image_reference,
		       (SELECT count(*) FROM task_instances ti WHERE ti.dag_run_id = r.id AND ti.state <> 'failed')
		  FROM dag_runs r JOIN dag_versions v ON v.id = r.dag_version_id
		 WHERE r.id = $1::uuid`, f.runUUID).Scan(&run, &image, &reset); err != nil {
		t.Fatal(err)
	}
	return run, image, reset
}

func (f clearGuardFixture) assertUntouched(t *testing.T) {
	t.Helper()
	if run, image, reset := f.state(t); run != "failed" || image != "img:v1" || reset != 0 {
		t.Errorf("after a clear that must not apply: run %s on %s with %d task instances reset, want failed on img:v1 with 0", run, image, reset)
	}
}

// taskStates lists the run's task instances as task=state, by task id.
func (f clearGuardFixture) taskStates(t *testing.T) string {
	t.Helper()
	var got string
	if err := f.pg.Pool.QueryRow(context.Background(), `SELECT string_agg(task_id || '=' || state::text, ' ' ORDER BY task_id)
		FROM task_instances WHERE dag_run_id = $1::uuid`, f.runUUID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	return got
}

// clearRequests are the two routes the UI clears a run through, as dry run
// and as the clear itself.
func (f clearGuardFixture) clearRequests(latest bool) map[string][2]string {
	out := map[string][2]string{}
	for _, dry := range []bool{true, false} {
		for route, req := range f.routes(latest, dry) {
			out[fmt.Sprintf("%s dry_run=%v", route, dry)] = req
		}
	}
	return out
}

// routes is one request per clear route.
func (f clearGuardFixture) routes(latest, dry bool) map[string][2]string {
	return map[string][2]string{
		"clearTaskInstances": {"/api/v2/dags/" + f.dagID + "/clearTaskInstances",
			fmt.Sprintf(`{"dag_run_id":"r1","only_failed":true,"run_on_latest_version":%v,"dry_run":%v}`, latest, dry)},
		"dagRuns clear": {"/api/v2/dags/" + f.dagID + "/dagRuns/r1/clear",
			fmt.Sprintf(`{"only_failed":true,"run_on_latest_version":%v,"dry_run":%v}`, latest, dry)},
	}
}

// A finished run moves to a version with other tasks through either route:
// the added task gets a task instance and the dropped one is removed (ADR
// 0086). The dry run changes nothing and names no run as kept.
func TestClearMovesAFinishedRunWithItsTaskSetIntegration(t *testing.T) {
	for _, route := range []string{"clearTaskInstances", "dagRuns clear"} {
		t.Run(route, func(t *testing.T) {
			f := newClearGuardFixture(t, []string{"a", "b"}, []string{"a", "c"})
			dry := f.routes(true, true)[route]
			rec := authGet(f.srv, http.MethodPost, dry[0], dry[1])
			if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "run_on_latest_version_not_applied") {
				t.Fatalf("dry run = %d (%s), want 200 without a kept run", rec.Code, rec.Body.String())
			}
			f.assertUntouched(t)
			req := f.routes(true, false)[route]
			rec = authGet(f.srv, http.MethodPost, req[0], req[1])
			if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "run_on_latest_version_not_applied") {
				t.Fatalf("clear = %d (%s), want 200 without a kept run", rec.Code, rec.Body.String())
			}
			if run, image, _ := f.state(t); run != "queued" || image != "img:v2" {
				t.Errorf("run %s on %s, want queued on img:v2", run, image)
			}
			if got := f.taskStates(t); got != "a=none b=removed c=none" {
				t.Errorf("task states = %q, want a=none b=removed c=none", got)
			}
		})
	}
}

// A run still running keeps its version through either route, dry run
// included: the clear applies on v1 and the response says why the option was
// not applied.
func TestClearOfARunningRunReportsTheVersionKeptIntegration(t *testing.T) {
	f := newClearGuardFixture(t, []string{"a"}, []string{"a", "b"})
	if _, err := f.pg.Pool.Exec(context.Background(), `UPDATE dag_runs SET state = 'running', ended_at = NULL WHERE id = $1::uuid`, f.runUUID); err != nil {
		t.Fatal(err)
	}
	for _, dry := range []bool{true, false} {
		for route, req := range f.routes(true, dry) {
			rec := authGet(f.srv, http.MethodPost, req[0], req[1])
			if rec.Code != http.StatusOK {
				t.Fatalf("%s dry_run=%v = %d (%s)", route, dry, rec.Code, rec.Body.String())
			}
			var got struct {
				NotApplied []struct {
					DagRunID string `json:"dag_run_id"`
					Reason   string `json:"reason"`
				} `json:"run_on_latest_version_not_applied"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if len(got.NotApplied) != 1 || got.NotApplied[0].DagRunID != "r1" || !strings.Contains(got.NotApplied[0].Reason, "the run is") {
				t.Errorf("%s dry_run=%v: run_on_latest_version_not_applied = %+v (%s)", route, dry, got.NotApplied, rec.Body.String())
			}
		}
	}
	if _, image, _ := f.state(t); image != "img:v1" {
		t.Errorf("run moved to %s while it was running", image)
	}
	if got := f.taskStates(t); got != "a=none" {
		t.Errorf("task states = %q, want a=none and no task instance for v2's b", got)
	}
}

// Without run_on_latest_version both routes
// preview and clear, and the run stays on the version it was created with.
func TestClearWithoutRebindStillAppliesIntegration(t *testing.T) {
	f := newClearGuardFixture(t, []string{"a"}, []string{"a", "b"})
	for name, req := range f.clearRequests(false) {
		if rec := authGet(f.srv, http.MethodPost, req[0], req[1]); rec.Code != http.StatusOK {
			t.Fatalf("%s = %d (%s), want 200", name, rec.Code, rec.Body.String())
		}
	}
	if run, image, reset := f.state(t); run != "queued" || image != "img:v1" || reset != 1 {
		t.Errorf("run %s on %s with %d reset, want queued on img:v1 with 1", run, image, reset)
	}
}

// A finished run moves to a version with the same tasks and keeps them.
func TestClearOntoAVersionWithTheSameTasksRebindsIntegration(t *testing.T) {
	f := newClearGuardFixture(t, []string{"a", "b"}, []string{"b", "a"})
	rec := authGet(f.srv, http.MethodPost, "/api/v2/dags/"+f.dagID+"/dagRuns/r1/clear",
		`{"only_failed":true,"run_on_latest_version":true,"dry_run":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear = %d (%s)", rec.Code, rec.Body.String())
	}
	if run, image, reset := f.state(t); run != "queued" || image != "img:v2" || reset != 2 {
		t.Errorf("run %s on %s with %d reset, want queued on img:v2 with 2", run, image, reset)
	}
}

// Another tenant reaches neither the check nor the clear.
func TestClearVersionIsTenantScopedIntegration(t *testing.T) {
	f := newClearGuardFixture(t, []string{"a"}, []string{"a"})
	other := fmt.Sprintf("clearguard_other_%d", time.Now().UnixNano())
	if _, err := f.pg.Pool.Exec(context.Background(), `INSERT INTO tenants (name, display_name) VALUES ($1, $1)`, other); err != nil {
		t.Fatal(err)
	}
	srv := clearGuardServer(f.repo, other)
	for name, req := range f.clearRequests(true) {
		if rec := authGet(srv, http.MethodPost, req[0], req[1]); rec.Code != http.StatusNotFound {
			t.Errorf("%s as another tenant = %d (%s), want 404", name, rec.Code, rec.Body.String())
		}
	}
	f.assertUntouched(t)
}

// An unknown dag_run_id answers 404 on both routes, as without
// run_on_latest_version, before anything is cleared.
func TestClearVersionUnknownRunIsNotFoundIntegration(t *testing.T) {
	f := newClearGuardFixture(t, []string{"a"}, []string{"a", "b"})
	for _, req := range [][2]string{
		{"/api/v2/dags/" + f.dagID + "/clearTaskInstances", `{"dag_run_id":"nope","run_on_latest_version":true,"dry_run":true}`},
		{"/api/v2/dags/" + f.dagID + "/clearTaskInstances", `{"dag_run_id":"nope","run_on_latest_version":true,"dry_run":false}`},
		{"/api/v2/dags/" + f.dagID + "/dagRuns/nope/clear", `{"run_on_latest_version":true,"dry_run":true}`},
		{"/api/v2/dags/" + f.dagID + "/dagRuns/nope/clear", `{"run_on_latest_version":true,"dry_run":false}`},
	} {
		if rec := authGet(f.srv, http.MethodPost, req[0], req[1]); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d (%s), want 404", req[0], req[1], rec.Code, rec.Body.String())
		}
	}
	f.assertUntouched(t)
}
