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

// activeRunIDs returns the display run ids of dagID among the scheduler's
// active runs. The database is shared with other tests, so the read is
// narrowed to the DAG under test.
func activeRunIDs(t *testing.T, ctx context.Context, sched *storage.SchedulerStore, dagID string) map[string]domain.DagRunState {
	t.Helper()
	runs, err := sched.ActiveRuns(ctx)
	if err != nil {
		t.Fatalf("active runs: %v", err)
	}
	out := map[string]domain.DagRunState{}
	for _, r := range runs {
		if r.DagID == dagID {
			out[r.DisplayRunID] = r.State
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

// TestActiveRunsHoldsQueuedRunsOfPausedDagIntegration: the scheduler's read of
// active runs leaves out a queued run of a paused DAG, as Airflow 3 does when it
// moves queued runs to running (#1705). A running run of the same paused DAG is
// still read, so its tasks finish, and unpausing makes the queued run visible.
func TestActiveRunsHoldsQueuedRunsOfPausedDagIntegration(t *testing.T) {
	repo, sched, ctx := openRepo(t)
	dagID := fmt.Sprintf("paused_wait_%d", time.Now().UnixNano())
	registerSpec(t, repo, ctx, dagID, []domain.TaskSpec{{TaskID: "a", Type: domain.TaskTypePython}})
	setPaused(t, ctx, repo, dagID, true)
	createRun(t, ctx, repo, dagID, "queued_run", domain.DagRunStateQueued)
	createRun(t, ctx, repo, dagID, "running_run", domain.DagRunStateRunning)

	got := activeRunIDs(t, ctx, sched, dagID)
	if _, ok := got["queued_run"]; ok {
		t.Errorf("a queued run of a paused DAG must wait: active runs = %v", got)
	}
	if got["running_run"] != domain.DagRunStateRunning {
		t.Errorf("a running run of a paused DAG keeps running: active runs = %v", got)
	}

	setPaused(t, ctx, repo, dagID, false)
	got = activeRunIDs(t, ctx, sched, dagID)
	if got["queued_run"] != domain.DagRunStateQueued || got["running_run"] != domain.DagRunStateRunning {
		t.Errorf("after unpause both runs are active: active runs = %v", got)
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
	sched.SetWarmExecution(config.ExecutionSection{WarmPoolsEnabled: true, MinIdleWorkers: 1, MaxPoolSize: 8}, nil)
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
