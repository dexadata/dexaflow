//go:build integration

package storage_test

import (
	"testing"
)

// TestTaskAttemptsReportsTheLiveAttempt covers the read behind the abandoned
// pod teardown (#2430): a running task instance is live on its try and epoch,
// a verdict that ends it makes it not live, and an id with no row is left
// out.
func TestTaskAttemptsReportsTheLiveAttempt(t *testing.T) {
	f := seedStaleHeartbeat(t, "task_attempts")
	missing := "00000000-0000-0000-0000-00000000dead"

	running, err := f.sched.TaskAttempts(f.ctx, []string{f.tiID, missing})
	if err != nil {
		t.Fatalf("TaskAttempts: %v", err)
	}
	f.setState(t, "failed")
	ended, err := f.sched.TaskAttempts(f.ctx, []string{f.tiID})
	if err != nil {
		t.Fatalf("TaskAttempts: %v", err)
	}

	row, ok := running[f.tiID]
	if !ok || !row.Live || row.TryNumber != f.tryNumber(t) || row.AttemptEpoch != f.attemptEpoch(t) {
		t.Errorf("running row = %+v (found %v), want live on try %d epoch %d", row, ok, f.tryNumber(t), f.attemptEpoch(t))
	}
	if _, ok := running[missing]; ok {
		t.Error("an id with no row was returned")
	}
	if ended[f.tiID].Live {
		t.Error("a failed task instance reads as live")
	}
	if _, err := f.sched.TaskAttempts(f.ctx, []string{"not-a-uuid"}); err == nil {
		t.Error("an invalid id was accepted")
	}
}
