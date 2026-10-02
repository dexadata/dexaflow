//go:build integration

package storage_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
)

// TestBufferedQueuedWriteGuardIntegration proves the SQL behind buffered
// dispatch's two writers against real Postgres: the scheduler's queued write
// only lands on the scheduled slot it planned, so a worker that already failed
// or re-offered the task (or an agent that already reported) is never
// overwritten, and a worker's re-offer moves a queued task back to scheduled
// with its backoff and, when counted, one more dispatch attempt.
func TestBufferedQueuedWriteGuardIntegration(t *testing.T) {
	repo, sched, ctx := openRepo(t)

	dagID := fmt.Sprintf("buffered_guard_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{{TaskID: "load", Type: domain.TaskTypePython}}
	registerSpec(t, repo, ctx, dagID, tasks)
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual",
		LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateDagRun: %v", err)
	}
	runID := resolveRunUUID(t, sched, ctx, dagID)
	if err := sched.MaterializeTasks(ctx, runID, tasks); err != nil {
		t.Fatalf("MaterializeTasks: %v", err)
	}
	if err := sched.ApplyTransition(ctx, runID, "load", domain.TaskStateScheduled); err != nil {
		t.Fatalf("to scheduled: %v", err)
	}

	// The plain case: the planned slot is still there, the write lands.
	if ok, err := sched.MarkQueued(ctx, runID, "load", nil); err != nil || !ok {
		t.Fatalf("MarkQueued on the planned slot = %v, %v; want true", ok, err)
	}
	if st := taskInstanceState(t, sched, ctx, runID, "load"); st != domain.TaskStateQueued {
		t.Fatalf("state = %q, want queued", st)
	}

	// A worker re-offers the queued task after a transient failure.
	nextAt := time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
	if ok, err := sched.RequeueDispatch(ctx, runID, "load", true, nextAt); err != nil || !ok {
		t.Fatalf("RequeueDispatch = %v, %v; want true", ok, err)
	}
	if st := taskInstanceState(t, sched, ctx, runID, "load"); st != domain.TaskStateScheduled {
		t.Fatalf("after re-offer state = %q, want scheduled", st)
	}
	attempts, active, err := sched.DispatchAttempts(ctx, runID, "load")
	if err != nil || !active || attempts != 1 {
		t.Fatalf("DispatchAttempts = %d, %v, %v; want 1, true", attempts, active, err)
	}

	// The scheduler's write for the attempt the worker just re-offered was
	// planned against the old slot: it must not land.
	if ok, err := sched.MarkQueued(ctx, runID, "load", nil); err != nil || ok {
		t.Fatalf("MarkQueued on a re-offered row = %v, %v; want false", ok, err)
	}
	if st := taskInstanceState(t, sched, ctx, runID, "load"); st != domain.TaskStateScheduled {
		t.Fatalf("a stale queued write moved the re-offered row to %q", st)
	}
	// The next planned dispatch, against the new slot, does.
	if ok, err := sched.MarkQueued(ctx, runID, "load", &nextAt); err != nil || !ok {
		t.Fatalf("MarkQueued on the new slot = %v, %v; want true", ok, err)
	}

	// A worker fails the task before the scheduler writes queued: the write
	// must leave it failed.
	if err := sched.MarkTaskDispatchFailed(ctx, runID, "load", "dispatch_failed: test"); err != nil {
		t.Fatalf("MarkTaskDispatchFailed: %v", err)
	}
	if ok, err := sched.MarkQueued(ctx, runID, "load", &nextAt); err != nil || ok {
		t.Fatalf("MarkQueued on a failed row = %v, %v; want false", ok, err)
	}
	if _, active, err := sched.DispatchAttempts(ctx, runID, "load"); err != nil || active {
		t.Fatalf("DispatchAttempts on a failed row: active = %v, %v; want false", active, err)
	}
}
