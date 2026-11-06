//go:build integration

// Package storage_test: the pod reconciler's Pending bounds (#1609) fail an
// attempt only before it started, since they decide from a cache snapshot of a
// Pending pod that may have been placed and started since.
package storage_test

import (
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/executor"
)

// TestFailPendingTaskOnlyBeforeTheStart: a queued attempt is failed, as infra
// when asked and confirmed at once; a running one, another attempt epoch or a
// terminal row is left alone, and the call says so.
func TestFailPendingTaskOnlyBeforeTheStart(t *testing.T) {
	f := seedStaleHeartbeat(t, "pending_bound_running")
	try, epoch := f.tryNumber(t), f.attemptEpoch(t)

	got, err := f.exec.FailPendingTask(f.ctx, f.tiID, try, epoch, "unschedulable: late", false)
	if err != nil {
		t.Fatalf("FailPendingTask (running): %v", err)
	}
	if got != executor.PendingStarted {
		t.Errorf("a bound on a running attempt = %v, want PendingStarted", got)
	}
	f.wantState(t, "after a bound on a running attempt", "running")

	f.setState(t, "queued")
	if got, err = f.exec.FailPendingTask(f.ctx, f.tiID, try, epoch+1, "unschedulable: stale", false); err != nil || got != executor.PendingGone {
		t.Fatalf("FailPendingTask (stale epoch) = %v, %v; want PendingGone", got, err)
	}
	f.wantState(t, "after a stale-epoch bound", "queued")

	if got, err = f.exec.FailPendingTask(f.ctx, f.tiID, try, epoch, "image_pull_failed: 429", true); err != nil || got != executor.PendingSettled {
		t.Fatalf("FailPendingTask (queued, infra) = %v, %v; want PendingSettled", got, err)
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

	got, err := f.exec.FailPendingTask(f.ctx, f.tiID, try, epoch, "unschedulable: no node", false)
	if err != nil || got != executor.PendingSettled {
		t.Fatalf("FailPendingTask = %v, %v; want PendingSettled", got, err)
	}
	f.wantState(t, "after the unschedulable bound", "failed")
	if k := f.failureKind(t); k != nil {
		t.Errorf("a task failure must keep no failure kind, got %q", *k)
	}
	if f.confirmedAt(t) != nil {
		t.Error("a task failure must not be infra-confirmed")
	}
	if got, err = f.exec.FailPendingTask(f.ctx, f.tiID, try, epoch, "unschedulable: again", false); err != nil || got != executor.PendingGone {
		t.Fatalf("a second bound on the failed row = %v, %v; want PendingGone", got, err)
	}
}

// TestFailPendingTaskRacingAStart: the agent's start commits while the
// bound's statement waits on the row. The update re-checks the row and
// matches nothing, but the statement's own read still saw it queued; the
// call must say the attempt may be live, never that it is gone, or the
// reconciler deletes a running task's pod (#1609 review).
func TestFailPendingTaskRacingAStart(t *testing.T) {
	f := seedStaleHeartbeat(t, "pending_bound_race")
	f.setState(t, "queued")
	try, epoch := f.tryNumber(t), f.attemptEpoch(t)

	tx, err := f.pg.Pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(f.ctx) }()
	if _, err := tx.Exec(f.ctx, "UPDATE task_instances SET state = 'running' WHERE id = $1::uuid", f.tiID); err != nil {
		t.Fatal(err)
	}
	type result struct {
		got executor.PendingSettle
		err error
	}
	done := make(chan result, 1)
	go func() {
		got, err := f.exec.FailPendingTask(f.ctx, f.tiID, try, epoch, "image_pull_failed: late", true)
		done <- result{got, err}
	}()
	time.Sleep(300 * time.Millisecond)
	if err := tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	r := <-done
	if r.err != nil {
		t.Fatalf("FailPendingTask: %v", r.err)
	}
	if r.got != executor.PendingStarted {
		t.Errorf("a bound racing a start = %v, want PendingStarted", r.got)
	}
	f.wantState(t, "after a bound that raced the start", "running")
}

// TestFailPendingTaskOnARowNotYetQueued: an attempt whose queued write has
// not landed (state none) is not failed by a bound, and its pod is left.
func TestFailPendingTaskOnARowNotYetQueued(t *testing.T) {
	f := seedStaleHeartbeat(t, "pending_bound_none")
	f.setState(t, "none")
	try, epoch := f.tryNumber(t), f.attemptEpoch(t)
	got, err := f.exec.FailPendingTask(f.ctx, f.tiID, try, epoch, "image_pull_failed: early", true)
	if err != nil || got != executor.PendingStarted {
		t.Fatalf("FailPendingTask (none) = %v, %v; want PendingStarted", got, err)
	}
	f.wantState(t, "after a bound on a row still in none", "none")
}
