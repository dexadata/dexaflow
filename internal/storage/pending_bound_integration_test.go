//go:build integration

// Package storage_test: the pod reconciler's Pending bounds (#1609) fail an
// attempt only before it started, since they decide from a cache snapshot of a
// Pending pod that may have been placed and started since.
package storage_test

import "testing"

// TestFailPendingTaskOnlyBeforeTheStart: a queued attempt is failed, as infra
// when asked and confirmed at once; a running one, another attempt epoch or a
// terminal row is left alone, and the call says so.
func TestFailPendingTaskOnlyBeforeTheStart(t *testing.T) {
	f := seedStaleHeartbeat(t, "pending_bound_running")
	try, epoch := f.tryNumber(t), f.attemptEpoch(t)

	applied, err := f.exec.FailPendingTask(f.ctx, f.tiID, try, epoch, "unschedulable: late", false)
	if err != nil {
		t.Fatalf("FailPendingTask (running): %v", err)
	}
	if applied {
		t.Error("a bound reported a settle on a running attempt")
	}
	f.wantState(t, "after a bound on a running attempt", "running")

	f.setState(t, "queued")
	if applied, err = f.exec.FailPendingTask(f.ctx, f.tiID, try, epoch+1, "unschedulable: stale", false); err != nil || applied {
		t.Fatalf("FailPendingTask (stale epoch): applied=%v err=%v", applied, err)
	}
	f.wantState(t, "after a stale-epoch bound", "queued")

	if applied, err = f.exec.FailPendingTask(f.ctx, f.tiID, try, epoch, "image_pull_failed: 429", true); err != nil || !applied {
		t.Fatalf("FailPendingTask (queued, infra): applied=%v err=%v", applied, err)
	}
	f.wantState(t, "after the infra bound of a queued attempt", "failed")
	if k := f.failureKind(t); k == nil || *k != "infra" {
		t.Fatalf("last_failure_kind = %v, want infra", k)
	}
	if f.confirmedAt(t) == nil {
		t.Fatal("the bound's infra settle must be confirmed at once")
	}
	if ok, err := f.sched.ResetForInfraReplace(f.ctx, f.runUUID, "t"); err != nil || !ok {
		t.Fatalf("the infra bound must re-place: ok=%v err=%v", ok, err)
	}
	if got := f.tryNumber(t); got != try {
		t.Errorf("the infra re-place must keep try_number %d, got %d", try, got)
	}
}

// TestFailPendingTaskAsATaskFailure: an unschedulable bound fails a queued
// attempt with no failure kind, a task failure under its retries.
func TestFailPendingTaskAsATaskFailure(t *testing.T) {
	f := seedStaleHeartbeat(t, "pending_bound_task")
	f.setState(t, "queued")
	try, epoch := f.tryNumber(t), f.attemptEpoch(t)

	applied, err := f.exec.FailPendingTask(f.ctx, f.tiID, try, epoch, "unschedulable: no node", false)
	if err != nil || !applied {
		t.Fatalf("FailPendingTask: applied=%v err=%v", applied, err)
	}
	f.wantState(t, "after the unschedulable bound", "failed")
	if k := f.failureKind(t); k != nil {
		t.Errorf("a task failure must keep no failure kind, got %q", *k)
	}
	if f.confirmedAt(t) != nil {
		t.Error("a task failure must not be infra-confirmed")
	}
	if applied, err = f.exec.FailPendingTask(f.ctx, f.tiID, try, epoch, "unschedulable: again", false); err != nil || applied {
		t.Fatalf("a second bound on the failed row: applied=%v err=%v", applied, err)
	}
}
