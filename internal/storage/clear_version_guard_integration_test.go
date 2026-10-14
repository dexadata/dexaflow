//go:build integration

package storage_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
)

// A clear with run_on_latest_version re-binds the run to the DAG's current
// version. The run keeps the task instances it has, so when that version adds
// a task the task gets no task instance, and when it removes one the old task
// instance stays behind with no task to run it (#1702). Until a clear creates
// and removes task instances the way Airflow's verify_integrity does, it
// refuses such a re-bind with a conflict that names the tasks and the way out,
// and writes nothing.

type driftFixture struct {
	repo           *storage.Repository
	pg             *storage.Postgres
	dagID, runUUID string
	v2Image        string
}

// seedDriftedRun registers v1 with v1Tasks, creates run r1 with a task
// instance per task, fails every one and the run, then registers v2 with
// v2Tasks as the current version.
func seedDriftedRun(t *testing.T, prefix string, v1Tasks, v2Tasks []string) driftFixture {
	t.Helper()
	repo, sched, pg, ctx := openInfra(t)
	dagID := fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	specs := func(ids []string) []domain.TaskSpec {
		out := make([]domain.TaskSpec, 0, len(ids))
		for _, id := range ids {
			out = append(out, domain.TaskSpec{TaskID: id, Type: domain.TaskTypePython})
		}
		return out
	}
	registerSpec(t, repo, ctx, dagID, specs(v1Tasks))
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	runUUID := resolveRunUUID(t, sched, ctx, dagID)
	if err := sched.MaterializeTasks(ctx, runUUID, specs(v1Tasks)); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	for _, id := range v1Tasks {
		if err := sched.ApplyTransition(ctx, runUUID, id, domain.TaskStateFailed); err != nil {
			t.Fatalf("fail %s: %v", id, err)
		}
	}
	if _, err := pg.Pool.Exec(ctx, `UPDATE dag_runs SET state='failed', ended_at=now() WHERE id=$1::uuid`, runUUID); err != nil {
		t.Fatalf("fail run: %v", err)
	}
	v2Image := registerSecondVersion(t, repo, ctx, dagID, specs(v2Tasks))
	return driftFixture{repo: repo, pg: pg, dagID: dagID, runUUID: runUUID, v2Image: v2Image}
}

// unchanged asserts the clear wrote nothing: the run is still failed on v1,
// and every task instance is still failed.
func (f driftFixture) unchanged(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	if got := runState(t, f.pg, ctx, f.runUUID); got != "failed" {
		t.Errorf("run state = %q, want failed: a refused clear re-opened the run", got)
	}
	if got := runImage(t, f.pg, ctx, f.runUUID); got == f.v2Image {
		t.Errorf("run re-bound to %q: a refused clear moved the run", got)
	}
	var notFailed int
	if err := f.pg.Pool.QueryRow(ctx,
		`SELECT count(*) FROM task_instances WHERE dag_run_id = $1::uuid AND state <> 'failed'`, f.runUUID).Scan(&notFailed); err != nil {
		t.Fatal(err)
	}
	if notFailed != 0 {
		t.Errorf("%d task instances left failed: a refused clear reset them", notFailed)
	}
}

func assertDrift(t *testing.T, err error, wants ...string) {
	t.Helper()
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("err = %v, want a conflict", err)
	}
	var safe *domain.SafeError
	if !errors.As(err, &safe) {
		t.Fatalf("err = %v, want a message the API may show", err)
	}
	for _, want := range wants {
		if !strings.Contains(safe.ClientMessage(), want) {
			t.Errorf("message %q does not say %q", safe.ClientMessage(), want)
		}
	}
}

var rebind = domain.ClearOptions{ResetDagRun: true, RunOnLatestVersion: true}

func TestClearOntoAVersionThatAddsATaskIsRefusedIntegration(t *testing.T) {
	f := seedDriftedRun(t, "drift_add", []string{"a"}, []string{"a", "b"})
	ctx := t.Context()
	assertDrift(t, f.repo.CheckClearVersion(ctx, "default", f.dagID, []string{"r1"}, rebind),
		`adds tasks [b]`, "without run_on_latest_version", "trigger a new run")
	f.unchanged(t)
	_, err := f.repo.ClearTaskInstances(ctx, "default", f.dagID, "r1", nil, false, rebind)
	assertDrift(t, err, `adds tasks [b]`)
	_, err = f.repo.ClearTaskInstances(ctx, "default", f.dagID, "r1", []string{"a"}, true, rebind)
	assertDrift(t, err, `adds tasks [b]`)
	f.unchanged(t)
}

func TestClearOntoAVersionThatRemovesATaskIsRefusedIntegration(t *testing.T) {
	f := seedDriftedRun(t, "drift_rm", []string{"a", "b", "c"}, []string{"a", "d"})
	ctx := t.Context()
	assertDrift(t, f.repo.CheckClearVersion(ctx, "default", f.dagID, []string{"r1"}, rebind),
		`adds tasks [d]`, `removes tasks [b c]`)
	_, err := f.repo.ClearTaskInstances(ctx, "default", f.dagID, "r1", nil, true, rebind)
	assertDrift(t, err, `removes tasks [b c]`)
	f.unchanged(t)
}

// The way out the message names: the same clear without
// run_on_latest_version keeps the run on v1 and applies.
func TestClearWithoutRebindIgnoresTheTaskSetIntegration(t *testing.T) {
	f := seedDriftedRun(t, "drift_pin", []string{"a"}, []string{"a", "b"})
	ctx := t.Context()
	pin := domain.ClearOptions{ResetDagRun: true}
	if err := f.repo.CheckClearVersion(ctx, "default", f.dagID, []string{"r1"}, pin); err != nil {
		t.Fatalf("CheckClearVersion without re-bind: %v", err)
	}
	if n, err := f.repo.ClearTaskInstances(ctx, "default", f.dagID, "r1", nil, true, pin); err != nil || n != 1 {
		t.Fatalf("clear without re-bind: n=%d err=%v", n, err)
	}
	if got := runImage(t, f.pg, ctx, f.runUUID); got == f.v2Image {
		t.Errorf("run re-bound to %q without run_on_latest_version", got)
	}
}

// A version with the same tasks re-binds as before.
func TestClearOntoAVersionWithTheSameTasksRebindsIntegration(t *testing.T) {
	f := seedDriftedRun(t, "drift_same", []string{"a", "b"}, []string{"b", "a"})
	ctx := t.Context()
	if err := f.repo.CheckClearVersion(ctx, "default", f.dagID, []string{"r1"}, rebind); err != nil {
		t.Fatalf("CheckClearVersion: %v", err)
	}
	if n, err := f.repo.ClearTaskInstances(ctx, "default", f.dagID, "r1", nil, true, rebind); err != nil || n != 2 {
		t.Fatalf("clear: n=%d err=%v", n, err)
	}
	if got := runImage(t, f.pg, ctx, f.runUUID); got != f.v2Image {
		t.Errorf("run image = %q, want %q", got, f.v2Image)
	}
}

// A run whose task instances were never materialized gets them from the
// version it is bound to, and the scheduler may already have read that
// version's tasks when the clear re-binds the run. So such a run is compared
// by the tasks of its own version: refused when the current version's differ,
// re-bound when they match, and a materialization from the old version's
// spec after the re-bind then still gives the run the current version's tasks.
func TestClearOfARunWithoutTaskInstancesComparesItsVersionIntegration(t *testing.T) {
	f := seedDriftedRun(t, "drift_empty", []string{"a"}, []string{"a", "b"})
	ctx := t.Context()
	if _, err := f.pg.Pool.Exec(ctx, `DELETE FROM task_instances WHERE dag_run_id = $1::uuid`, f.runUUID); err != nil {
		t.Fatal(err)
	}
	assertDrift(t, f.repo.CheckClearVersion(ctx, "default", f.dagID, []string{"r1"}, rebind), "adds tasks [b]")
	_, err := f.repo.ClearTaskInstances(ctx, "default", f.dagID, "r1", nil, true, rebind)
	assertDrift(t, err, "adds tasks [b]")
	if got := runImage(t, f.pg, ctx, f.runUUID); got == f.v2Image {
		t.Errorf("run re-bound to %q although its version's tasks differ", got)
	}
}

func TestClearOfARunWithoutTaskInstancesRebindsOnTheSameTasksIntegration(t *testing.T) {
	f := seedDriftedRun(t, "drift_empty_same", []string{"a", "b"}, []string{"b", "a"})
	ctx := t.Context()
	if _, err := f.pg.Pool.Exec(ctx, `DELETE FROM task_instances WHERE dag_run_id = $1::uuid`, f.runUUID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.ClearTaskInstances(ctx, "default", f.dagID, "r1", nil, true, rebind); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := runImage(t, f.pg, ctx, f.runUUID); got != f.v2Image {
		t.Errorf("run image = %q, want %q", got, f.v2Image)
	}
	// The scheduler read the run on v1 before the clear and materializes now.
	v1 := []domain.TaskSpec{{TaskID: "a", Type: domain.TaskTypePython}, {TaskID: "b", Type: domain.TaskTypePython}}
	if err := storage.NewSchedulerStore(f.pg).MaterializeTasks(ctx, f.runUUID, v1); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	var ids string
	if err := f.pg.Pool.QueryRow(ctx, `SELECT string_agg(task_id, ',' ORDER BY task_id) FROM task_instances
		WHERE dag_run_id = $1::uuid`, f.runUUID).Scan(&ids); err != nil {
		t.Fatal(err)
	}
	if ids != "a,b" {
		t.Errorf("task instances after a late materialization = %q, want the current version's a,b", ids)
	}
}

// One request names several runs (include_past, include_future): the
// version's spec is read once, every run is compared, and the refusal says
// how many of the runs differ.
func TestCheckClearVersionCountsTheRunsThatDifferIntegration(t *testing.T) {
	f := seedDriftedRun(t, "drift_many", []string{"a"}, []string{"a", "b"})
	ctx := t.Context()
	if _, err := f.repo.CreateDagRun(ctx, "default", f.dagID, domain.DagRun{
		RunID: "r2", State: domain.DagRunStateFailed, RunType: "manual", LogicalDate: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatalf("create r2: %v", err)
	}
	// r2 was created on v2, so only r1 differs.
	assertDrift(t, f.repo.CheckClearVersion(ctx, "default", f.dagID, []string{"r1", "r2"}, rebind),
		"1 of the 2 runs", `run "r1"`, "adds tasks [b]")
	if err := f.repo.CheckClearVersion(ctx, "default", f.dagID, []string{"r2"}, rebind); err != nil {
		t.Errorf("r2 is on the current version: %v", err)
	}
	if err := f.repo.CheckClearVersion(ctx, "default", f.dagID, []string{"r2", "nope"}, rebind); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("an unknown run: err = %v, want not found", err)
	}
	f.unchanged(t)
}

// Another tenant finds neither the DAG nor the run.
func TestCheckClearVersionIsTenantScopedIntegration(t *testing.T) {
	f := seedDriftedRun(t, "drift_tenant", []string{"a"}, []string{"a", "b"})
	ctx := t.Context()
	other := fmt.Sprintf("drift_other_%d", time.Now().UnixNano())
	if _, err := f.pg.Pool.Exec(ctx, `INSERT INTO tenants (name, display_name) VALUES ($1, $1)`, other); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.CheckClearVersion(ctx, other, f.dagID, []string{"r1"}, rebind); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("CheckClearVersion as another tenant: err = %v, want not found", err)
	}
	f.unchanged(t)
}
