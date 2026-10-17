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

// A clear with run_on_latest_version moves a finished run to the DAG's current
// version together with its task set (ADR 0086, #1894): the task instances of
// the tasks the version adds are created, the ones of the tasks it drops are
// marked removed, and a removed one whose task returns runs again. A run that
// has not finished, or that still has a queued or running task instance,
// keeps its own version, as in Airflow 3.2.

type driftFixture struct {
	repo           *storage.Repository
	pg             *storage.Postgres
	dagID, runUUID string
	v2Image        string
}

func pythonTasks(ids []string) []domain.TaskSpec {
	out := make([]domain.TaskSpec, 0, len(ids))
	for _, id := range ids {
		out = append(out, domain.TaskSpec{TaskID: id, Type: domain.TaskTypePython})
	}
	return out
}

// seedDriftedRun registers v1 with v1Tasks, creates run r1 with a task
// instance per task, fails every one and the run, then registers v2 with
// v2Tasks as the current version.
func seedDriftedRun(t *testing.T, prefix string, v1Tasks []string, v2Tasks []domain.TaskSpec) driftFixture {
	t.Helper()
	repo, sched, pg, ctx := openInfra(t)
	dagID := fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	registerSpec(t, repo, ctx, dagID, pythonTasks(v1Tasks))
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	runUUID := resolveRunUUID(t, sched, ctx, dagID)
	if err := sched.MaterializeTasks(ctx, runUUID, pythonTasks(v1Tasks)); err != nil {
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
	v2Image := registerSecondVersion(t, repo, ctx, dagID, v2Tasks)
	return driftFixture{repo: repo, pg: pg, dagID: dagID, runUUID: runUUID, v2Image: v2Image}
}

// tiRow is one task instance as the assertions read it.
type tiRow struct {
	state              string
	try, maxTries      int
	pool               string
	poolSlots          int
	operator           string
	attemptEpochBumped bool
}

// tis reads every task instance of the run, keyed by task id.
func (f driftFixture) tis(t *testing.T) map[string]tiRow {
	t.Helper()
	rows, err := f.pg.Pool.Query(t.Context(), `SELECT task_id, state::text, try_number, max_tries,
		       coalesce(pool, ''), pool_slots, operator, attempt_epoch > 0
		  FROM task_instances WHERE dag_run_id = $1::uuid`, f.runUUID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]tiRow{}
	for rows.Next() {
		var id string
		var r tiRow
		if err := rows.Scan(&id, &r.state, &r.try, &r.maxTries, &r.pool, &r.poolSlots, &r.operator, &r.attemptEpochBumped); err != nil {
			t.Fatal(err)
		}
		out[id] = r
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func (f driftFixture) states(t *testing.T) string {
	t.Helper()
	tis := f.tis(t)
	parts := make([]string, 0, len(tis))
	for _, id := range []string{"a", "b", "c", "d"} {
		if r, ok := tis[id]; ok {
			parts = append(parts, id+"="+r.state)
		}
	}
	return strings.Join(parts, " ")
}

func (f driftFixture) setRun(t *testing.T, state string) {
	t.Helper()
	if _, err := f.pg.Pool.Exec(t.Context(), `UPDATE dag_runs SET state=$2::dag_run_state WHERE id=$1::uuid`, f.runUUID, state); err != nil {
		t.Fatal(err)
	}
}

func (f driftFixture) setTask(t *testing.T, taskID, state string) {
	t.Helper()
	if _, err := f.pg.Pool.Exec(t.Context(), `UPDATE task_instances SET state=$3::task_state
		WHERE dag_run_id=$1::uuid AND task_id=$2`, f.runUUID, taskID, state); err != nil {
		t.Fatal(err)
	}
}

func (f driftFixture) onV2(t *testing.T) bool {
	t.Helper()
	return runImage(t, f.pg, t.Context(), f.runUUID) == f.v2Image
}

var rebind = domain.ClearOptions{ResetDagRun: true, RunOnLatestVersion: true}

// A task the current version adds gets a task instance in the clear, with the
// retries, pool and size that version gives it, so the re-bound run runs it
// and can finish (#1702: the run used to wait for it forever).
func TestClearRebindCreatesTheAddedTasksIntegration(t *testing.T) {
	v2 := []domain.TaskSpec{
		{TaskID: "a", Type: domain.TaskTypePython},
		{TaskID: "b", Type: domain.TaskTypePython, Retries: intPtr(2), Pool: "etl", PoolSlots: 3},
	}
	f := seedDriftedRun(t, "rebind_add", []string{"a"}, v2)
	ctx := t.Context()
	n, err := f.repo.ClearTaskInstances(ctx, "default", f.dagID, "r1", nil, true, rebind)
	if err != nil || n != 1 {
		t.Fatalf("clear: n=%d err=%v, want the failed a reset", n, err)
	}
	if !f.onV2(t) {
		t.Error("the finished run was not moved to the current version")
	}
	if got := runState(t, f.pg, ctx, f.runUUID); got != "queued" {
		t.Errorf("run state = %q, want queued", got)
	}
	tis := f.tis(t)
	want := tiRow{state: "none", try: 1, maxTries: 3, pool: "etl", poolSlots: 3, operator: "python"}
	if got := tis["b"]; got != want {
		t.Errorf("added task b = %+v, want %+v", got, want)
	}
	if got := tis["a"]; got.state != "none" || got.try != 2 {
		t.Errorf("cleared task a = %+v, want none on try 2", got)
	}
}

// A task the current version drops is marked removed, not reset, and a task
// it adds is created; the tasks both versions declare are cleared as asked.
func TestClearRebindMarksTheDroppedTasksRemovedIntegration(t *testing.T) {
	f := seedDriftedRun(t, "rebind_rm", []string{"a", "b", "c"}, pythonTasks([]string{"a", "d"}))
	ctx := t.Context()
	n, err := f.repo.ClearTaskInstances(ctx, "default", f.dagID, "r1", nil, true, rebind)
	if err != nil || n != 1 {
		t.Fatalf("clear: n=%d err=%v, want only a reset", n, err)
	}
	if got := f.states(t); got != "a=none b=removed c=removed d=none" {
		t.Errorf("task states = %q, want a=none b=removed c=removed d=none", got)
	}
	if !f.onV2(t) {
		t.Error("run not moved to the current version")
	}
}

// A task named in the clear that the current version dropped is marked
// removed and not reset: the version has nothing to run for it. With
// only_failed the empty list left after dropping it must not turn into
// "every failed task" (a stays failed).
func TestClearRebindDoesNotResetANamedDroppedTaskIntegration(t *testing.T) {
	for _, onlyFailed := range []bool{true, false} {
		t.Run(fmt.Sprintf("only_failed=%v", onlyFailed), func(t *testing.T) {
			f := seedDriftedRun(t, "rebind_named", []string{"a", "b"}, pythonTasks([]string{"a"}))
			ctx := t.Context()
			n, err := f.repo.ClearTaskInstances(ctx, "default", f.dagID, "r1", []string{"b"}, onlyFailed, rebind)
			if err != nil || n != 0 {
				t.Fatalf("clear: n=%d err=%v, want nothing reset", n, err)
			}
			if got := f.states(t); got != "a=failed b=removed" {
				t.Errorf("task states = %q, want a=failed b=removed", got)
			}
			if tis := f.tis(t); tis["b"].try != 1 {
				t.Errorf("b try = %d, want 1: a removed task was reset", tis["b"].try)
			}
		})
	}
}

// A removed task instance whose task a later version declares again runs
// again in the re-bound run, as a new try.
func TestClearRebindRestoresARemovedTaskIntegration(t *testing.T) {
	f := seedDriftedRun(t, "rebind_back", []string{"a", "b"}, pythonTasks([]string{"a"}))
	ctx := t.Context()
	if _, err := f.repo.ClearTaskInstances(ctx, "default", f.dagID, "r1", nil, true, rebind); err != nil {
		t.Fatalf("first clear: %v", err)
	}
	f.setTask(t, "a", "success")
	f.setRun(t, "success")
	spec := domain.DAGSpec{SchemaVersion: "1.0", DagID: f.dagID, DagVersion: "v3", Image: "img:v3",
		Tasks: []domain.TaskSpec{{TaskID: "a", Type: domain.TaskTypePython}, {TaskID: "b", Type: domain.TaskTypePython, Retries: intPtr(4)}}}
	hash, err := spec.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	if created, rerr := f.repo.RegisterDagVersion(ctx, "default", spec, hash); rerr != nil || !created {
		t.Fatalf("register v3: created=%v err=%v", created, rerr)
	}
	n, err := f.repo.ClearTaskInstances(ctx, "default", f.dagID, "r1", nil, true, rebind)
	if err != nil || n != 1 {
		t.Fatalf("second clear: n=%d err=%v, want b brought back", n, err)
	}
	b := f.tis(t)["b"]
	if b.state != "none" || b.try != 2 || b.maxTries != 6 || !b.attemptEpochBumped {
		t.Errorf("restored b = %+v, want none on try 2 with v3's 4 retries and a new attempt epoch", b)
	}
	if got := f.tis(t)["a"].state; got != "success" {
		t.Errorf("a = %q, want success kept", got)
	}
	if got := runImage(t, f.pg, ctx, f.runUUID); got != "img:v3" {
		t.Errorf("run image = %q, want img:v3", got)
	}
}

// A finished run without task instances gets the current version's in the
// clear, so the scheduler, which materializes only a run that has none,
// never builds them from the version the run left.
func TestClearRebindOfARunWithoutTaskInstancesCreatesThemIntegration(t *testing.T) {
	f := seedDriftedRun(t, "rebind_empty", []string{"a"}, pythonTasks([]string{"a", "b"}))
	ctx := t.Context()
	if _, err := f.pg.Pool.Exec(ctx, `DELETE FROM task_instances WHERE dag_run_id = $1::uuid`, f.runUUID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.ClearTaskInstances(ctx, "default", f.dagID, "r1", nil, true, rebind); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := f.states(t); got != "a=none b=none" {
		t.Errorf("task states = %q, want a=none b=none", got)
	}
	if !f.onV2(t) {
		t.Error("run not moved to the current version")
	}
}

// A version with the same tasks re-binds a finished run and creates nothing.
func TestClearOntoAVersionWithTheSameTasksRebindsIntegration(t *testing.T) {
	f := seedDriftedRun(t, "rebind_same", []string{"a", "b"}, pythonTasks([]string{"b", "a"}))
	ctx := t.Context()
	if n, err := f.repo.ClearTaskInstances(ctx, "default", f.dagID, "r1", nil, true, rebind); err != nil || n != 2 {
		t.Fatalf("clear: n=%d err=%v", n, err)
	}
	if !f.onV2(t) {
		t.Error("run not moved to the current version")
	}
	if got := f.states(t); got != "a=none b=none" {
		t.Errorf("task states = %q", got)
	}
}

// Without run_on_latest_version the run keeps v1 and its task set.
func TestClearWithoutRebindKeepsTheTaskSetIntegration(t *testing.T) {
	f := seedDriftedRun(t, "rebind_pin", []string{"a"}, pythonTasks([]string{"a", "b"}))
	ctx := t.Context()
	pin := domain.ClearOptions{ResetDagRun: true}
	if n, err := f.repo.ClearTaskInstances(ctx, "default", f.dagID, "r1", nil, true, pin); err != nil || n != 1 {
		t.Fatalf("clear without re-bind: n=%d err=%v", n, err)
	}
	if f.onV2(t) {
		t.Error("run re-bound without run_on_latest_version")
	}
	if got := f.states(t); got != "a=none" {
		t.Errorf("task states = %q, want only a=none", got)
	}
}

// run_on_latest_version re-binds only together with reset_dag_runs, as in
// Airflow 3.2, where the re-bind happens while the run is reset.
func TestClearWithoutResetDagRunDoesNotRebindIntegration(t *testing.T) {
	f := seedDriftedRun(t, "rebind_noreset", []string{"a"}, pythonTasks([]string{"a", "b"}))
	ctx := t.Context()
	tasksOnly := domain.ClearOptions{ResetDagRun: false, RunOnLatestVersion: true}
	kept, err := f.repo.RunsKeepingVersion(ctx, "default", f.dagID, []string{"r1"}, tasksOnly)
	if err != nil || len(kept) != 0 {
		t.Fatalf("RunsKeepingVersion without reset_dag_runs = %v, %v; want nothing to report", kept, err)
	}
	if n, err := f.repo.ClearTaskInstances(ctx, "default", f.dagID, "r1", nil, true, tasksOnly); err != nil || n != 1 {
		t.Fatalf("clear without reset_dag_runs: n=%d err=%v", n, err)
	}
	if f.onV2(t) {
		t.Error("run re-bound without reset_dag_runs")
	}
	if got := f.states(t); got != "a=none" {
		t.Errorf("task states = %q, want only a=none", got)
	}
}

// A run that has not finished keeps its version, as in Airflow 3.2: the clear
// applies on that version, with that version's retries, and the report names
// the run and why.
func TestClearOfAnUnfinishedRunKeepsItsVersionIntegration(t *testing.T) {
	v2 := []domain.TaskSpec{{TaskID: "a", Type: domain.TaskTypePython, Retries: intPtr(5)}, {TaskID: "b", Type: domain.TaskTypePython}}
	for _, state := range []string{"running", "queued"} {
		t.Run(state, func(t *testing.T) {
			f := seedDriftedRun(t, "keep_"+state, []string{"a"}, v2)
			ctx := t.Context()
			f.setRun(t, state)
			kept, err := f.repo.RunsKeepingVersion(ctx, "default", f.dagID, []string{"r1"}, rebind)
			if err != nil {
				t.Fatal(err)
			}
			if len(kept) != 1 || kept[0].RunID != "r1" || !strings.Contains(kept[0].Reason, "the run is "+state) {
				t.Errorf("kept = %+v, want r1 because the run is %s", kept, state)
			}
			if n, err := f.repo.ClearTaskInstances(ctx, "default", f.dagID, "r1", nil, true, rebind); err != nil || n != 1 {
				t.Fatalf("clear: n=%d err=%v", n, err)
			}
			if f.onV2(t) {
				t.Error("an unfinished run was moved to the current version")
			}
			if got := f.states(t); got != "a=none" {
				t.Errorf("task states = %q, want a=none and no task instance for v2's b", got)
			}
			if a := f.tis(t)["a"]; a.maxTries != 2 {
				t.Errorf("a max_tries = %d, want 2: the budget of v1, the version the run keeps", a.maxTries)
			}
			if got := runState(t, f.pg, ctx, f.runUUID); got != "queued" {
				t.Errorf("run state = %q, want queued", got)
			}
		})
	}
}

// A finished run with a task instance still queued or running (marked failed
// or success while it ran) keeps its version too: the live attempt stays on
// the version its image was built for.
func TestClearOfAFinishedRunWithALiveTaskKeepsItsVersionIntegration(t *testing.T) {
	for _, state := range []string{"running", "queued"} {
		t.Run(state, func(t *testing.T) {
			f := seedDriftedRun(t, "keep_live_"+state, []string{"a", "b"}, pythonTasks([]string{"a"}))
			ctx := t.Context()
			f.setTask(t, "b", state)
			kept, err := f.repo.RunsKeepingVersion(ctx, "default", f.dagID, []string{"r1"}, rebind)
			if err != nil {
				t.Fatal(err)
			}
			if len(kept) != 1 || !strings.Contains(kept[0].Reason, fmt.Sprintf(`task "b" is %s`, state)) {
				t.Errorf("kept = %+v, want r1 because b is %s", kept, state)
			}
			if _, err := f.repo.ClearTaskInstances(ctx, "default", f.dagID, "r1", []string{"a"}, true, rebind); err != nil {
				t.Fatalf("clear: %v", err)
			}
			if f.onV2(t) {
				t.Error("a run with a live attempt was moved to the current version")
			}
			if got := f.states(t); got != "a=none b="+state {
				t.Errorf("task states = %q, want a=none b=%s", got, state)
			}
		})
	}
}

// One request over several runs reports only the runs that keep their
// version: a finished run on v1 moves, a running one stays, a run already on
// the current version has nothing to report.
func TestRunsKeepingVersionReportsOnlyTheRunsThatStayIntegration(t *testing.T) {
	f := seedDriftedRun(t, "keep_many", []string{"a"}, pythonTasks([]string{"a", "b"}))
	ctx := t.Context()
	for i, id := range []string{"r2", "r3"} {
		if _, err := f.repo.CreateDagRun(ctx, "default", f.dagID, domain.DagRun{
			RunID: id, State: domain.DagRunStateRunning, RunType: "manual",
			LogicalDate: time.Now().UTC().Add(time.Duration(i+1) * time.Hour),
		}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	// r3 is running on v1; r2 is running on the current v2.
	if _, err := f.pg.Pool.Exec(ctx, `UPDATE dag_runs r3 SET dag_version_id = r1.dag_version_id
		  FROM dag_runs r1
		 WHERE r1.id = $1::uuid AND r3.dag_id = r1.dag_id AND r3.run_id = 'r3'`, f.runUUID); err != nil {
		t.Fatal(err)
	}
	kept, err := f.repo.RunsKeepingVersion(ctx, "default", f.dagID, []string{"r1", "r2", "r3"}, rebind)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 || kept[0].RunID != "r3" {
		t.Errorf("kept = %+v, want only r3", kept)
	}
	if _, err := f.repo.RunsKeepingVersion(ctx, "default", f.dagID, []string{"r2", "nope"}, rebind); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("an unknown run: err = %v, want not found", err)
	}
}

// Another tenant finds neither the DAG nor the run.
func TestRunsKeepingVersionIsTenantScopedIntegration(t *testing.T) {
	f := seedDriftedRun(t, "keep_tenant", []string{"a"}, pythonTasks([]string{"a", "b"}))
	ctx := t.Context()
	other := fmt.Sprintf("keep_other_%d", time.Now().UnixNano())
	if _, err := f.pg.Pool.Exec(ctx, `INSERT INTO tenants (name, display_name) VALUES ($1, $1)`, other); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.RunsKeepingVersion(ctx, other, f.dagID, []string{"r1"}, rebind); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("RunsKeepingVersion as another tenant: err = %v, want not found", err)
	}
	if _, err := f.repo.ClearTaskInstances(ctx, other, f.dagID, "r1", nil, true, rebind); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("ClearTaskInstances as another tenant: err = %v, want not found", err)
	}
	if f.onV2(t) || f.states(t) != "a=failed" {
		t.Errorf("another tenant's clear changed the run: on v2=%v states=%q", f.onV2(t), f.states(t))
	}
}

// A task instance a re-bind left removed stays removed when a later clear
// without run_on_latest_version names it: the run's version does not declare
// the task, so a reset to none would leave a task instance nothing ever plans
// on a finished run, and the run would never count as settled again (never
// retained, never collected). With only_failed the empty list left after
// dropping it must not turn into "every failed task" (a stays failed).
func TestClearWithoutRebindLeavesARemovedTaskRemovedIntegration(t *testing.T) {
	for _, onlyFailed := range []bool{false, true} {
		t.Run(fmt.Sprintf("only_failed=%v", onlyFailed), func(t *testing.T) {
			f := seedDriftedRun(t, "stay_removed", []string{"a", "b"}, pythonTasks([]string{"a"}))
			ctx := t.Context()
			if _, err := f.repo.ClearTaskInstances(ctx, "default", f.dagID, "r1", []string{"a"}, true, rebind); err != nil {
				t.Fatalf("re-binding clear: %v", err)
			}
			f.setTask(t, "a", "failed")
			f.setRun(t, "failed")
			pin := domain.ClearOptions{ResetDagRun: true}
			n, err := f.repo.ClearTaskInstances(ctx, "default", f.dagID, "r1", []string{"b"}, onlyFailed, pin)
			if err != nil || n != 0 {
				t.Fatalf("clear of the removed b: n=%d err=%v, want nothing reset", n, err)
			}
			if got := f.states(t); got != "a=failed b=removed" {
				t.Errorf("task states = %q, want a=failed b=removed", got)
			}
		})
	}
}
