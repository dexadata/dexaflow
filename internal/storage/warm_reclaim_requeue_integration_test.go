//go:build integration

// Package storage_test — the state guard on the warm reclaim re-placement
// (ADR 0058 N1d-c, H2).
//
// RequeueForRedispatch moves a reclaimed (queued, never-ran) attempt back to
// 'scheduled' so the planner re-admits and re-dispatches it. The guard lives in
// the UPDATE's WHERE clause (state='queued', bounded to the exact attempt), so a
// fake-store unit test cannot prove it: only real Postgres shows that a running
// or a terminal attempt is left untouched (0 rows, benign). Three cases lock the
// contract — a queued attempt is re-placed, a running one is a no-op, a terminal
// one is a no-op — so fast re-placement can never disturb a live or settled TI.
package storage_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
)

// TestRequeueForRedispatchMovesQueuedToScheduled: the ordinary path — a queued
// attempt a warm worker was handed but never ran is moved back to scheduled.
func TestRequeueForRedispatchMovesQueuedToScheduled(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)
	dagID := fmt.Sprintf("warm_requeue_queued_%d", time.Now().UnixNano())
	runUUID := seedQueuedTaskNoBind(t, repo, sched, ctx, dagID, "load")

	if err := exec.RequeueForRedispatch(ctx, runUUID, "load", 1, 0); err != nil {
		t.Fatalf("RequeueForRedispatch on a queued TI = %v, want nil", err)
	}

	if st := taskInstanceState(t, sched, ctx, runUUID, "load"); st != domain.TaskStateScheduled {
		t.Fatalf("TI 'load' = %q, want scheduled — a reclaimed queued attempt must be re-admitted for re-dispatch", st)
	}
}

// TestRequeueForRedispatchNoOpOnRunningTI: a running attempt is live — the
// guarded UPDATE matches zero rows and the TI stays running, so a stray reclaim
// can never yank a running attempt out from under itself.
func TestRequeueForRedispatchNoOpOnRunningTI(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)
	dagID := fmt.Sprintf("warm_requeue_running_%d", time.Now().UnixNano())
	runUUID := seedRunningTask(t, repo, sched, ctx, dagID, "load")

	if err := exec.RequeueForRedispatch(ctx, runUUID, "load", 1, 0); err != nil {
		t.Fatalf("RequeueForRedispatch on a running TI = %v, want nil (a guarded no-op is not an error)", err)
	}

	if st := taskInstanceState(t, sched, ctx, runUUID, "load"); st != domain.TaskStateRunning {
		t.Fatalf("TI 'load' = %q, want running — a running attempt must never be disturbed", st)
	}
}

// TestRequeueForRedispatchNoOpOnTerminalTI: a settled attempt is terminal — the
// guarded UPDATE matches zero rows and the TI stays failed, so a late reclaim
// never resurrects an abandoned attempt.
func TestRequeueForRedispatchNoOpOnTerminalTI(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)
	dagID := fmt.Sprintf("warm_requeue_terminal_%d", time.Now().UnixNano())
	runUUID := seedRunningTask(t, repo, sched, ctx, dagID, "load")

	if err := sched.ApplyTransition(ctx, runUUID, "load", domain.TaskStateFailed); err != nil {
		t.Fatalf("ApplyTransition to failed: %v", err)
	}

	if err := exec.RequeueForRedispatch(ctx, runUUID, "load", 1, 0); err != nil {
		t.Fatalf("RequeueForRedispatch on a terminal TI = %v, want nil (a guarded no-op is not an error)", err)
	}

	if st := taskInstanceState(t, sched, ctx, runUUID, "load"); st != domain.TaskStateFailed {
		t.Fatalf("TI 'load' = %q, want failed — a settled attempt must never be resurrected", st)
	}
}

// TestRequeueForRedispatchRestartsDispatchLostClock: a reclaimed attempt that
// sat queued past the dispatch-lost threshold is re-placed and queued again. The
// re-dispatched attempt must start a fresh queued episode: RequeueForRedispatch
// clears queued_at, so MarkQueued stamps now() instead of keeping the first
// episode's stamp, and the dispatch-lost reaper does not fail the attempt the
// moment it is queued again (#1604).
func TestRequeueForRedispatchRestartsDispatchLostClock(t *testing.T) {
	pg, repo, sched, exec, ctx := openExecPG(t)
	dagID := fmt.Sprintf("warm_requeue_queued_at_%d", time.Now().UnixNano())
	runUUID := seedQueuedTaskNoBind(t, repo, sched, ctx, dagID, "load")

	// The first queued episode began 4 minutes ago, past the 3 minute threshold.
	if _, err := pg.Pool.Exec(ctx,
		`UPDATE task_instances SET queued_at = now() - interval '4 minutes' WHERE dag_run_id = $1::uuid AND task_id = 'load'`,
		runUUID); err != nil {
		t.Fatalf("backdating queued_at: %v", err)
	}
	var epoch int
	if err := pg.Pool.QueryRow(ctx,
		`SELECT attempt_epoch FROM task_instances WHERE dag_run_id = $1::uuid AND task_id = 'load'`,
		runUUID).Scan(&epoch); err != nil {
		t.Fatalf("reading attempt_epoch: %v", err)
	}

	if err := exec.RequeueForRedispatch(ctx, runUUID, "load", 1, epoch); err != nil {
		t.Fatalf("RequeueForRedispatch: %v", err)
	}
	if st := taskInstanceState(t, sched, ctx, runUUID, "load"); st != domain.TaskStateScheduled {
		t.Fatalf("TI 'load' = %q after the re-place, want scheduled", st)
	}
	if ok, err := sched.MarkQueued(ctx, runUUID, "load", nil); err != nil || !ok {
		t.Fatalf("MarkQueued after the re-place = (%v, %v), want (true, nil)", ok, err)
	}

	// Read the stamp straight from the row: the stale-queued list is capped to
	// the oldest rows, so other tests' leftovers could hide this one. The age is
	// measured against the database's own clock, which stamped queued_at, so a
	// database clock that drifts from the test process cannot fail it (#2104).
	var queuedAt, dbNow time.Time
	if err := pg.Pool.QueryRow(ctx,
		`SELECT queued_at, now() FROM task_instances WHERE dag_run_id = $1::uuid AND task_id = 'load'`,
		runUUID).Scan(&queuedAt, &dbNow); err != nil {
		t.Fatalf("reading queued_at: %v", err)
	}
	if age := dbNow.Sub(queuedAt); age > time.Minute {
		t.Fatalf("re-dispatched attempt queued_at age = %s, want a fresh stamp: the first episode's queued_at was kept", age.Round(time.Second))
	}
	c := executor.StaleQueuedCandidate{DagRunID: runUUID, TaskID: "load", QueuedAt: queuedAt}
	if executor.IsDispatchLost(c, executor.DefaultReaperConfig().DispatchLostThreshold, dbNow) {
		t.Fatal("a reclaimed attempt queued again a moment ago is already dispatch-lost")
	}
}
