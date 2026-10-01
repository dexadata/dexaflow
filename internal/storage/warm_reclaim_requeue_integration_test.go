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
)

// TestRequeueForRedispatchMovesQueuedToScheduled: the ordinary path — a queued
// attempt a warm worker was handed but never ran is moved back to scheduled.
func TestRequeueForRedispatchMovesQueuedToScheduled(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)
	dagID := fmt.Sprintf("warm_requeue_queued_%d", time.Now().UnixNano())
	runUUID := seedQueuedTaskNoBind(t, repo, sched, ctx, dagID, "load")

	if err := exec.RequeueForRedispatch(ctx, runUUID, "load", 1); err != nil {
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

	if err := exec.RequeueForRedispatch(ctx, runUUID, "load", 1); err != nil {
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

	if err := exec.RequeueForRedispatch(ctx, runUUID, "load", 1); err != nil {
		t.Fatalf("RequeueForRedispatch on a terminal TI = %v, want nil (a guarded no-op is not an error)", err)
	}

	if st := taskInstanceState(t, sched, ctx, runUUID, "load"); st != domain.TaskStateFailed {
		t.Fatalf("TI 'load' = %q, want failed — a settled attempt must never be resurrected", st)
	}
}
