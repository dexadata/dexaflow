//go:build integration

package storage_test

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
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

// TestRequeueDispatchClearsQueuedAtIntegration guards the dispatch-lost reaper
// against a re-offered task: a worker re-offer must clear queued_at, so the next
// queued episode is measured from when it was queued again, not from the first
// episode. Otherwise a task kept waiting by quota backpressure for longer than
// the reaper threshold is failed as dispatch_lost the moment it is re-queued.
func TestRequeueDispatchClearsQueuedAtIntegration(t *testing.T) {
	repo, sched, ctx := openRepo(t)

	dagID := fmt.Sprintf("requeue_queued_at_%d", time.Now().UnixNano())
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
		t.Fatalf("ApplyTransition: %v", err)
	}
	if ok, err := sched.MarkQueued(ctx, runID, "load", nil); err != nil || !ok {
		t.Fatalf("MarkQueued: ok=%v err=%v", ok, err)
	}

	// The first queued episode happened ten minutes ago and backpressure has kept
	// re-offering the task since.
	conn, err := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, `UPDATE task_instances SET queued_at = now() - interval '10 minutes'
		WHERE dag_run_id = $1 AND task_id = 'load'`, runID); err != nil {
		t.Fatalf("backdate queued_at: %v", err)
	}

	next := time.Now().UTC().Truncate(time.Microsecond)
	if ok, err := sched.RequeueDispatch(ctx, runID, "load", false, next); err != nil || !ok {
		t.Fatalf("RequeueDispatch: ok=%v err=%v", ok, err)
	}
	if ok, err := sched.MarkQueued(ctx, runID, "load", &next); err != nil || !ok {
		t.Fatalf("MarkQueued after re-offer: ok=%v err=%v", ok, err)
	}

	cands, err := sched.ListStaleQueuedCandidates(ctx)
	if err != nil {
		t.Fatalf("ListStaleQueuedCandidates: %v", err)
	}
	for _, c := range cands {
		if c.DagRunID != runID || c.TaskID != "load" {
			continue
		}
		if age := time.Since(c.QueuedAt); age > time.Minute {
			t.Errorf("queued_at is %s old after a fresh re-queue, want it stamped by the second episode", age.Round(time.Second))
		}
		if executor.IsDispatchLost(c, 3*time.Minute, time.Now()) {
			t.Error("a task re-queued a moment ago is already dispatch_lost")
		}
		return
	}
	t.Fatal("re-queued task not among the stale-queued candidates")
}
