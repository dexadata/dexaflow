//go:build integration

// Package storage_test — the on-failure alert claim/deliver split.
//
// The scheduler-level tests use a fake that models the state machine but not
// time; the backoff and the attempt guard live entirely in the UPDATE's WHERE
// clause, so only real Postgres can show they hold. These pin the predicates the
// fake cannot: that a claim is refused while backing off, allowed once the
// backoff elapses, refused once delivered or out of budget, and that a delivery
// stamp from a superseded episode lands nowhere.
package storage_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
)

// seedFailedRun creates a DAG with one failed run and returns its UUID.
func seedFailedRun(t *testing.T, repo *storage.Repository, sched *storage.SchedulerStore, ctx context.Context, dagID string) string {
	t.Helper()
	tasks := []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython}}
	registerSpec(t, repo, ctx, dagID, tasks)
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual",
		LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateDagRun: %v", err)
	}
	return resolveRunUUID(t, sched, ctx, dagID)
}

// A claim consumes an attempt and then refuses the next one until the backoff
// elapses. Without this the retry runs at the scheduler tick — once per second by
// default — so a dead alert endpoint would be hit thousands of times for a single
// failed run, by the very system meant to report that failure.
func TestClaimAlertAttemptRespectsBackoffIntegration(t *testing.T) {
	repo, sched, _, ctx := openExec(t)
	runUUID := seedFailedRun(t, repo, sched, ctx, fmt.Sprintf("alert_backoff_%d", time.Now().UnixNano()))

	attempt, err := sched.ClaimAlertAttempt(ctx, runUUID, 5, time.Hour)
	if err != nil || attempt != 1 {
		t.Fatalf("first claim = (%d, %v), want (1, nil)", attempt, err)
	}
	again, err := sched.ClaimAlertAttempt(ctx, runUUID, 5, time.Hour)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if again != 0 {
		t.Fatalf("second claim = %d, want 0 — the backoff has not elapsed", again)
	}
}

// Once the backoff has elapsed the episode is claimable again. A negative
// backoff puts next_alert_attempt_at in the past, which is how the previous
// attempt looks after waiting, without making the test sleep.
func TestClaimAlertAttemptAllowsRetryAfterBackoffIntegration(t *testing.T) {
	repo, sched, _, ctx := openExec(t)
	runUUID := seedFailedRun(t, repo, sched, ctx, fmt.Sprintf("alert_retry_%d", time.Now().UnixNano()))

	if a, err := sched.ClaimAlertAttempt(ctx, runUUID, 5, -time.Minute); err != nil || a != 1 {
		t.Fatalf("first claim = (%d, %v), want (1, nil)", a, err)
	}
	second, err := sched.ClaimAlertAttempt(ctx, runUUID, 5, -time.Minute)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if second != 2 {
		t.Fatalf("second claim = %d, want 2 — an elapsed backoff must allow the retry", second)
	}
}

// The budget is a hard ceiling: past it the episode stops being claimable, so a
// permanently unreachable endpoint is abandoned rather than retried forever.
func TestClaimAlertAttemptStopsAtBudgetIntegration(t *testing.T) {
	repo, sched, _, ctx := openExec(t)
	runUUID := seedFailedRun(t, repo, sched, ctx, fmt.Sprintf("alert_budget_%d", time.Now().UnixNano()))

	const max = 3
	for i := 1; i <= max; i++ {
		got, err := sched.ClaimAlertAttempt(ctx, runUUID, max, -time.Minute)
		if err != nil || got != i {
			t.Fatalf("claim %d = (%d, %v), want (%d, nil)", i, got, err, i)
		}
	}
	if got, err := sched.ClaimAlertAttempt(ctx, runUUID, max, -time.Minute); err != nil || got != 0 {
		t.Fatalf("claim past the budget = (%d, %v), want (0, nil)", got, err)
	}
}

// A delivered episode is never claimed again — the dedup this whole mechanism
// exists for (#431) still holds after the split.
func TestClaimAlertAttemptRefusedAfterDeliveryIntegration(t *testing.T) {
	repo, sched, _, ctx := openExec(t)
	runUUID := seedFailedRun(t, repo, sched, ctx, fmt.Sprintf("alert_delivered_%d", time.Now().UnixNano()))

	attempt, err := sched.ClaimAlertAttempt(ctx, runUUID, 5, -time.Minute)
	if err != nil || attempt != 1 {
		t.Fatalf("claim = (%d, %v), want (1, nil)", attempt, err)
	}
	if err := sched.MarkRunAlertDelivered(ctx, runUUID, attempt); err != nil {
		t.Fatalf("MarkRunAlertDelivered: %v", err)
	}
	if got, err := sched.ClaimAlertAttempt(ctx, runUUID, 5, -time.Minute); err != nil || got != 0 {
		t.Fatalf("claim after delivery = (%d, %v), want (0, nil) — one page per episode", got, err)
	}
}

// The send is detached from the tick, so an operator clear can start a NEW
// failure episode while an old send is still in flight. A stamp carrying the
// superseded attempt must land nowhere: otherwise the new episode is recorded as
// paged without anyone having been paged for it.
func TestMarkRunAlertDeliveredIgnoresSupersededAttemptIntegration(t *testing.T) {
	repo, sched, _, ctx := openExec(t)
	dagID := fmt.Sprintf("alert_superseded_%d", time.Now().UnixNano())
	runUUID := seedFailedRun(t, repo, sched, ctx, dagID)

	stale, err := sched.ClaimAlertAttempt(ctx, runUUID, 5, -time.Minute)
	if err != nil || stale != 1 {
		t.Fatalf("claim = (%d, %v), want (1, nil)", stale, err)
	}
	// An operator clears the run: a new episode, with a fresh attempt budget.
	if _, err := repo.ClearTaskInstances(ctx, "default", dagID, "r1", nil, false, domain.ClearOptions{ResetDagRun: true, RunOnLatestVersion: true}); err != nil {
		t.Fatalf("ClearTaskInstances: %v", err)
	}
	// The in-flight send from the old episode finally reports success.
	if err := sched.MarkRunAlertDelivered(ctx, runUUID, stale); err != nil {
		t.Fatalf("MarkRunAlertDelivered (superseded): %v", err)
	}
	// The new episode must still be claimable — nothing was paged for it.
	got, err := sched.ClaimAlertAttempt(ctx, runUUID, 5, -time.Minute)
	if err != nil {
		t.Fatalf("claim after clear: %v", err)
	}
	if got == 0 {
		t.Fatal("a stamp from the superseded episode marked the new one delivered; " +
			"the new episode would never be paged")
	}
}
