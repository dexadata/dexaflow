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
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u-" + tenant, TenantID: tenant, Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(1000, time.Minute),
		CORSOrigins:   []string{"*"},
		TokenTTLSecs:  3600,
		Dags:          repo,
		DagRuns:       repo,
		Tasks:         repo,
		Versions:      repo,
		Specs:         repo,
		DagVersions:   repo,
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
		t.Errorf("after a refused clear: run %s on %s with %d task instances reset, want failed on img:v1 with 0", run, image, reset)
	}
}

// clearRequests are the two routes the UI clears a run through, as dry run
// and as the clear itself.
func (f clearGuardFixture) clearRequests(latest bool) map[string][2]string {
	tiPath := "/api/v2/dags/" + f.dagID + "/clearTaskInstances"
	runPath := "/api/v2/dags/" + f.dagID + "/dagRuns/r1/clear"
	out := map[string][2]string{}
	for _, dry := range []bool{true, false} {
		out[fmt.Sprintf("clearTaskInstances dry_run=%v", dry)] = [2]string{tiPath,
			fmt.Sprintf(`{"dag_run_id":"r1","only_failed":true,"run_on_latest_version":%v,"dry_run":%v}`, latest, dry)}
		out[fmt.Sprintf("dagRuns clear dry_run=%v", dry)] = [2]string{runPath,
			fmt.Sprintf(`{"only_failed":true,"run_on_latest_version":%v,"dry_run":%v}`, latest, dry)}
	}
	return out
}

func TestClearOntoAVersionWithOtherTasksIsRefusedIntegration(t *testing.T) {
	cases := []struct {
		name   string
		v1, v2 []string
		detail string
	}{
		{"the version adds a task", []string{"a"}, []string{"a", "b"}, "adds tasks [b]"},
		{"the version removes a task", []string{"a", "b"}, []string{"a"}, "removes tasks [b]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newClearGuardFixture(t, tc.v1, tc.v2)
			for name, req := range f.clearRequests(true) {
				rec := authGet(f.srv, http.MethodPost, req[0], req[1])
				if rec.Code != http.StatusConflict {
					t.Fatalf("%s = %d (%s), want 409", name, rec.Code, rec.Body.String())
				}
				var p Problem
				if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
					t.Fatalf("%s: decode: %v", name, err)
				}
				for _, want := range []string{tc.detail, `version "v2"`, "without run_on_latest_version", "trigger a new run"} {
					if !strings.Contains(p.Detail, want) {
						t.Errorf("%s: detail %q does not say %q", name, p.Detail, want)
					}
				}
				f.assertUntouched(t)
			}
		})
	}
}

// The way out the refusal names: without run_on_latest_version both routes
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

// A version with the same tasks re-binds as before.
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
func TestClearGuardIsTenantScopedIntegration(t *testing.T) {
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
// run_on_latest_version, before any version is compared.
func TestClearGuardUnknownRunIsNotFoundIntegration(t *testing.T) {
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
