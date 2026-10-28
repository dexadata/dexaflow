//go:build integration

// Package storage_test: a queued attempt whose pod never started before its
// dispatch-minted credential ran out is failed as start_expired, an infra mark
// the planner re-places without spending a retry (#1126).
package storage_test

import (
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/executor"
)

// TestMarkTaskStartExpiredReplacesOffTheRetryBudget is the #1126 contract: the
// mark is an infra failure with its own reason, guarded on the queued state and
// the listed attempt, and the infra re-place rail then resets the row onto the
// same try with a new epoch, so the user's retry budget is untouched.
func TestMarkTaskStartExpiredReplacesOffTheRetryBudget(t *testing.T) {
	f := seedStaleHeartbeat(t, "start_expired")
	f.setState(t, "queued")
	// Queued long ago, so the row sorts first in the reaper's bounded list even
	// on a database other tests left queued rows in.
	f.execSQL(t, "UPDATE task_instances SET queued_at = '2000-01-01T00:00:00Z' WHERE id=$1::uuid", f.tiID)
	try, epoch := f.tryNumber(t), f.attemptEpoch(t)

	cands, err := f.sched.ListStaleQueuedCandidates(f.ctx, time.Minute, executor.ReapCursor{})
	if err != nil {
		t.Fatalf("ListStaleQueuedCandidates: %v", err)
	}
	listed := false
	for _, c := range cands {
		if c.TaskInstanceID == f.tiID {
			listed = true
			if c.TenantID == "" {
				t.Error("a stale-queued candidate must carry its tenant for the log marker")
			}
		}
	}
	if !listed {
		t.Fatalf("the queued attempt was not listed: %+v", cands)
	}

	if ok, err := f.sched.MarkTaskStartExpired(f.ctx, f.tiID, try, epoch+1); err != nil || ok {
		t.Fatalf("a mark for another epoch must be a no-op: ok=%v err=%v", ok, err)
	}
	if ok, err := f.sched.MarkTaskStartExpired(f.ctx, f.tiID, try, epoch); err != nil || !ok {
		t.Fatalf("MarkTaskStartExpired ok=%v err=%v", ok, err)
	}
	f.wantState(t, "start_expired mark", "failed")
	if kind := f.failureKind(t); kind == nil || *kind != "infra" {
		t.Fatalf("last_failure_kind = %v, want infra", kind)
	}
	var msg string
	if err := f.pg.Pool.QueryRow(f.ctx,
		"SELECT error_message FROM task_instances WHERE id=$1::uuid", f.tiID).Scan(&msg); err != nil {
		t.Fatalf("select error_message: %v", err)
	}
	if !strings.HasPrefix(msg, "start_expired:") {
		t.Errorf("error_message = %q, want the start_expired reason", msg)
	}
	if ok, err := f.sched.MarkTaskStartExpired(f.ctx, f.tiID, try, epoch); err != nil || ok {
		t.Fatalf("a second mark must be a no-op: ok=%v err=%v", ok, err)
	}

	replaced, err := f.sched.ResetForInfraReplace(f.ctx, f.runUUID, "t")
	if err != nil || !replaced {
		t.Fatalf("ResetForInfraReplace replaced=%v err=%v", replaced, err)
	}
	if got := f.tryNumber(t); got != try {
		t.Errorf("try_number = %d after the re-place, want %d: a retry was spent", got, try)
	}
	if got := f.attemptEpoch(t); got != epoch+1 {
		t.Errorf("attempt_epoch = %d after the re-place, want %d", got, epoch+1)
	}
}

// A row whose agent already reported RUNNING is not the listed queued attempt
// any more: the mark must leave it alone.
func TestMarkTaskStartExpiredSparesARunningAttempt(t *testing.T) {
	f := seedStaleHeartbeat(t, "start_expired_running")
	if ok, err := f.sched.MarkTaskStartExpired(f.ctx, f.tiID, f.tryNumber(t), f.attemptEpoch(t)); err != nil || ok {
		t.Fatalf("a running attempt must not be marked: ok=%v err=%v", ok, err)
	}
	f.wantState(t, "running attempt", "running")
}
