//go:build integration

package storage_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/scheduler"
	"github.com/dexadata/dexaflow/internal/storage"
)

// activeRuns returns the scheduler's active runs of dagID keyed by display run
// id. The database is shared with other tests, so the read is narrowed to the
// DAG under test.
func activeRuns(t *testing.T, ctx context.Context, sched *storage.SchedulerStore, dagID string) map[string]scheduler.RunState {
	t.Helper()
	runs, err := sched.ActiveRuns(ctx)
	if err != nil {
		t.Fatalf("active runs: %v", err)
	}
	out := map[string]scheduler.RunState{}
	for _, r := range runs {
		if r.DagID == dagID {
			out[r.DisplayRunID] = r
		}
	}
	return out
}

func setPaused(t *testing.T, ctx context.Context, repo *storage.Repository, dagID string, paused bool) {
	t.Helper()
	if _, err := repo.SetPaused(ctx, "default", dagID, paused); err != nil {
		t.Fatalf("set paused=%v: %v", paused, err)
	}
}

func createRun(t *testing.T, ctx context.Context, repo *storage.Repository, dagID, runID string, state domain.DagRunState) {
	t.Helper()
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: runID, State: state, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create run %s: %v", runID, err)
	}
}

// TestActiveRunsMarksRunsOfPausedDagHeldIntegration: the scheduler's read of
// active runs keeps every queued and running run of a paused DAG and marks it
// Held, so the tick leaves a queued one waiting, as Airflow 3 does when it moves
// queued runs to running (#1705), while its live tasks still count against the
// caps. Unpausing clears the mark.
func TestActiveRunsMarksRunsOfPausedDagHeldIntegration(t *testing.T) {
	repo, sched, ctx := openRepo(t)
	dagID := fmt.Sprintf("paused_wait_%d", time.Now().UnixNano())
	registerSpec(t, repo, ctx, dagID, []domain.TaskSpec{{TaskID: "a", Type: domain.TaskTypePython}})
	setPaused(t, ctx, repo, dagID, true)
	createRun(t, ctx, repo, dagID, "queued_run", domain.DagRunStateQueued)
	createRun(t, ctx, repo, dagID, "running_run", domain.DagRunStateRunning)

	got := activeRuns(t, ctx, sched, dagID)
	if r, ok := got["queued_run"]; !ok || r.State != domain.DagRunStateQueued || !r.Held {
		t.Errorf("a queued run of a paused DAG is read and held: state=%q held=%v (present %v)", r.State, r.Held, ok)
	}
	if r, ok := got["running_run"]; !ok || r.State != domain.DagRunStateRunning || !r.Held {
		t.Errorf("a running run of a paused DAG is read and held: state=%q held=%v (present %v)", r.State, r.Held, ok)
	}

	setPaused(t, ctx, repo, dagID, false)
	got = activeRuns(t, ctx, sched, dagID)
	if len(got) != 2 || got["queued_run"].Held || got["running_run"].Held {
		t.Errorf("after unpause both runs are active and none is held: %d runs, queued held=%v, running held=%v",
			len(got), got["queued_run"].Held, got["running_run"].Held)
	}
}

// TestPausedDagManualRunStartsOnUnpauseIntegration drives the real scheduler
// tick over a manual run of a paused DAG: the run stays queued with no task
// instance materialized and nothing dispatched while the DAG is paused, and it
// starts and dispatches its root task once the DAG is unpaused (#1705).
func TestPausedDagManualRunStartsOnUnpauseIntegration(t *testing.T) {
	repo, store, ctx := openRepo(t)
	sched := scheduler.NewScheduler(store, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Millisecond)
	sched.SetDispatcher(noopDispatcher{})
	sched.SetLeading(true)
	dagID := fmt.Sprintf("paused_unpause_%d", time.Now().UnixNano())
	registerSpec(t, repo, ctx, dagID, []domain.TaskSpec{
		{TaskID: "a", Type: domain.TaskTypePython, Entrypoint: "dag:a"},
		{TaskID: "b", Type: domain.TaskTypePython, Entrypoint: "dag:b", DependsOn: []string{"a"}},
	})
	setPaused(t, ctx, repo, dagID, true)
	createRun(t, ctx, repo, dagID, "r1", domain.DagRunStateQueued)

	step := func(n int) {
		t.Helper()
		for i := 0; i < n; i++ {
			if err := sched.Step(ctx); err != nil {
				t.Fatalf("step: %v", err)
			}
		}
	}
	step(3)
	run, err := repo.GetDagRun(ctx, "default", dagID, "r1")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if run.State != domain.DagRunStateQueued {
		t.Fatalf("paused DAG: run state = %q, want queued", run.State)
	}
	tis, _, err := repo.ListTaskInstances(ctx, "default", dagID, "r1", 100, 0)
	if err != nil {
		t.Fatalf("list task instances: %v", err)
	}
	if len(tis) != 0 {
		t.Fatalf("paused DAG: %d task instances materialized, want none", len(tis))
	}

	setPaused(t, ctx, repo, dagID, false)
	step(3)
	run, err = repo.GetDagRun(ctx, "default", dagID, "r1")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if run.State != domain.DagRunStateRunning {
		t.Fatalf("after unpause: run state = %q, want running", run.State)
	}
	tis, _, err = repo.ListTaskInstances(ctx, "default", dagID, "r1", 100, 0)
	if err != nil {
		t.Fatalf("list task instances: %v", err)
	}
	states := map[string]domain.TaskState{}
	for _, ti := range tis {
		states[ti.TaskID] = ti.State
	}
	if states["a"] != domain.TaskStateQueued || states["b"] != domain.TaskStateNone {
		t.Fatalf("after unpause: task states = %v, want a queued and b none", states)
	}
}

// TestWarmTargetsSkipQueuedRunsOfPausedDagIntegration: a queued run waiting on
// a paused DAG does not keep warm workers for its version, since none of its
// tasks can be dispatched; unpausing brings the target back (#1705).
func TestWarmTargetsSkipQueuedRunsOfPausedDagIntegration(t *testing.T) {
	repo, sched, ctx := openRepo(t)
	sched.SetWarmExecution(config.ExecutionSection{WarmPoolsEnabled: true, MinIdleWorkers: 1, MaxPoolSize: 8}, nil, domain.WarmPolicy{})
	dagID := fmt.Sprintf("paused_warm_%d", time.Now().UnixNano())
	image := fmt.Sprintf("paused-warm-img-%d:v1", time.Now().UnixNano())
	registerWarmSpec(t, repo, ctx, dagID, image, 2)
	setPaused(t, ctx, repo, dagID, true)
	createRun(t, ctx, repo, dagID, "r1", domain.DagRunStateQueued)

	hasTarget := func() bool {
		t.Helper()
		targets, err := sched.ActiveWarmTargets(ctx)
		if err != nil {
			t.Fatalf("ActiveWarmTargets: %v", err)
		}
		for _, tgt := range targets {
			if tgt.Image == image {
				return true
			}
		}
		return false
	}
	if hasTarget() {
		t.Fatal("paused DAG: a waiting queued run must not keep a warm target")
	}
	setPaused(t, ctx, repo, dagID, false)
	if !hasTarget() {
		t.Fatal("after unpause: the queued run's version needs its warm target")
	}
}

// taskStates returns the task instance states of a run keyed by task id.
func taskStates(t *testing.T, ctx context.Context, repo *storage.Repository, dagID, runID string) map[string]domain.TaskState {
	t.Helper()
	tis, _, err := repo.ListTaskInstances(ctx, "default", dagID, runID, 100, 0)
	if err != nil {
		t.Fatalf("list task instances of %s: %v", runID, err)
	}
	out := map[string]domain.TaskState{}
	for _, ti := range tis {
		out[ti.TaskID] = ti.State
	}
	return out
}

// TestClearedRunOfPausedDagKeepsItsPoolSlotIntegration: a clear on a running
// run of a paused DAG puts the run back in queued while another of its tasks is
// still running. The run waits for the unpause, but the running task still
// holds its pool slot (ADR 0053, ADR 0066), so a task of another DAG in the same
// one-slot pool is not admitted past the cap. Once the running task ends, the
// slot frees and the other DAG's task is admitted.
func TestClearedRunOfPausedDagKeepsItsPoolSlotIntegration(t *testing.T) {
	repo, store, ctx := openRepo(t)
	suffix := time.Now().UnixNano()
	pool := fmt.Sprintf("held_pool_%d", suffix)
	if err := repo.SetPool(ctx, "default", domain.Pool{Name: pool, Slots: 1}); err != nil {
		t.Fatalf("SetPool: %v", err)
	}
	sched := scheduler.NewScheduler(store, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Millisecond)
	sched.SetDispatcher(noopDispatcher{})
	sched.EnablePools()
	sched.SetLeading(true)

	heldDag := fmt.Sprintf("held_clear_%d", suffix)
	registerSpec(t, repo, ctx, heldDag, []domain.TaskSpec{
		{TaskID: "a", Type: domain.TaskTypePython, Entrypoint: "dag:a", Pool: pool},
		{TaskID: "b", Type: domain.TaskTypePython, Entrypoint: "dag:b"},
	})
	createRun(t, ctx, repo, heldDag, "r1", domain.DagRunStateRunning)
	runUUID := resolveRunUUID(t, store, ctx, heldDag)
	spec := []domain.TaskSpec{
		{TaskID: "a", Type: domain.TaskTypePython, Pool: pool},
		{TaskID: "b", Type: domain.TaskTypePython},
	}
	if err := store.MaterializeTasks(ctx, runUUID, spec); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	for task, st := range map[string]domain.TaskState{"a": domain.TaskStateRunning, "b": domain.TaskStateFailed} {
		if err := store.ApplyTransition(ctx, runUUID, task, st); err != nil {
			t.Fatalf("set %s %s: %v", task, st, err)
		}
	}
	setPaused(t, ctx, repo, heldDag, true)
	if _, err := repo.ClearTaskInstances(ctx, "default", heldDag, "r1", []string{"b"}, false, domain.ClearOptions{ResetDagRun: true}); err != nil {
		t.Fatalf("clear b: %v", err)
	}
	held, ok := activeRuns(t, ctx, store, heldDag)["r1"]
	if !ok || held.State != domain.DagRunStateQueued || !held.Held || held.States["a"] != domain.TaskStateRunning {
		t.Fatalf("cleared run of a paused DAG: state=%q held=%v tasks=%v (present %v), want queued, held, a running",
			held.State, held.Held, held.States, ok)
	}

	otherDag := fmt.Sprintf("held_other_%d", suffix)
	registerSpec(t, repo, ctx, otherDag, []domain.TaskSpec{{TaskID: "c", Type: domain.TaskTypePython, Entrypoint: "dag:c", Pool: pool}})
	createRun(t, ctx, repo, otherDag, "o1", domain.DagRunStateQueued)

	step := func(n int) {
		t.Helper()
		for i := 0; i < n; i++ {
			if err := sched.Step(ctx); err != nil {
				t.Fatalf("step: %v", err)
			}
		}
	}
	step(3)
	if st := taskStates(t, ctx, repo, otherDag, "o1")["c"]; st == domain.TaskStateQueued || st == domain.TaskStateRunning {
		t.Fatalf("the held run's running task holds the only slot of %s: other DAG's task c = %q, want it held", pool, st)
	}
	if got := taskStates(t, ctx, repo, heldDag, "r1"); got["a"] != domain.TaskStateRunning || got["b"] == domain.TaskStateQueued {
		t.Fatalf("held run: task states = %v, want a running and b not dispatched", got)
	}
	if run, err := repo.GetDagRun(ctx, "default", heldDag, "r1"); err != nil || run.State != domain.DagRunStateQueued {
		t.Fatalf("held run state = %+v err=%v, want queued until the unpause", run, err)
	}

	if err := store.ApplyTransition(ctx, runUUID, "a", domain.TaskStateSuccess); err != nil {
		t.Fatalf("finish a: %v", err)
	}
	step(3)
	if st := taskStates(t, ctx, repo, otherDag, "o1")["c"]; st != domain.TaskStateQueued {
		t.Fatalf("after the held run's task ends the slot frees: other DAG's task c = %q, want queued", st)
	}
}

// TestWarmTargetsKeepHeldRunWithLiveTaskIntegration: a queued run waiting on a
// paused DAG keeps the warm target of its version while one of its tasks is
// still live (queued or running), so the reconcile does not drop the pool under
// it; once no task is live the target goes (#1705).
func TestWarmTargetsKeepHeldRunWithLiveTaskIntegration(t *testing.T) {
	repo, store, ctx := openRepo(t)
	store.SetWarmExecution(config.ExecutionSection{WarmPoolsEnabled: true, MinIdleWorkers: 1, MaxPoolSize: 8}, nil, domain.WarmPolicy{})
	dagID := fmt.Sprintf("held_warm_%d", time.Now().UnixNano())
	image := fmt.Sprintf("held-warm-img-%d:v1", time.Now().UnixNano())
	registerWarmSpec(t, repo, ctx, dagID, image, 2)
	createRun(t, ctx, repo, dagID, "r1", domain.DagRunStateQueued)
	runUUID := resolveRunUUID(t, store, ctx, dagID)
	if err := store.MaterializeTasks(ctx, runUUID, []domain.TaskSpec{{TaskID: "a", Type: domain.TaskTypePython}}); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if err := store.ApplyTransition(ctx, runUUID, "a", domain.TaskStateRunning); err != nil {
		t.Fatalf("set a running: %v", err)
	}
	setPaused(t, ctx, repo, dagID, true)

	hasTarget := func() bool {
		t.Helper()
		targets, err := store.ActiveWarmTargets(ctx)
		if err != nil {
			t.Fatalf("ActiveWarmTargets: %v", err)
		}
		for _, tgt := range targets {
			if tgt.Image == image {
				return true
			}
		}
		return false
	}
	if !hasTarget() {
		t.Fatal("held run with a running task must keep its version's warm target")
	}
	if err := store.ApplyTransition(ctx, runUUID, "a", domain.TaskStateSuccess); err != nil {
		t.Fatalf("finish a: %v", err)
	}
	if hasTarget() {
		t.Fatal("held run with no live task must not keep a warm target")
	}
}
