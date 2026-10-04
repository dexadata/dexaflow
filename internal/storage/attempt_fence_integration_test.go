//go:build integration

// Package storage_test: the attempt-epoch report fence (ADR 0051 amendment,
// PR A3, #911).
//
// The six agent-path queries match the attempt on (try_number, attempt_epoch),
// with a token that carries no epoch read as epoch 0. A superseded attempt that
// shares its replacement's try_number (an infra re-place, a reschedule poke, a
// repeated dispatch) is rejected with ErrStaleReport, which the agent RPC maps
// to should_terminate.
package storage_test

import (
	"errors"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/agentrpc"
	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

// identityFor is the agent identity a token minted for this dispatch carries.
func (f *staleHeartbeatFixture) identityFor(try, epoch int) auth.AgentIdentity {
	return auth.AgentIdentity{
		TaskInstanceID: f.tiID, RunID: f.runUUID, TaskID: "t", DagID: f.dagID,
		TryNumber: try, AttemptEpoch: epoch, HasAttemptEpoch: true,
	}
}

// legacyIdentity is the identity of a token minted before the epoch existed.
func (f *staleHeartbeatFixture) legacyIdentity(try int) auth.AgentIdentity {
	return auth.AgentIdentity{TaskInstanceID: f.tiID, RunID: f.runUUID, TaskID: "t", DagID: f.dagID, TryNumber: try}
}

// dispatch claims an epoch the way the dispatcher does and returns the
// identity the token minted from it carries.
func (f *staleHeartbeatFixture) dispatch(t *testing.T) auth.AgentIdentity {
	t.Helper()
	r, err := f.exec.ResolveTask(f.ctx, f.runUUID, "t")
	if err != nil {
		t.Fatalf("ResolveTask: %v", err)
	}
	return f.identityFor(r.TryNumber, r.AttemptEpoch)
}

func wantStale(t *testing.T, what string, err error) {
	t.Helper()
	if !errors.Is(err, agentrpc.ErrStaleReport) {
		t.Errorf("%s from a superseded attempt must be ErrStaleReport, got %v", what, err)
	}
}

// TestSupersededAttemptIsFencedAfterInfraReplace reproduces the Lite chain of
// #911: an attempt is dispatched, its queued row is failed as dispatch_lost and
// re-placed off-budget (same try), and the replacement is dispatched. The
// superseded agent, still holding its token, keeps reporting RUNNING. Every
// agent-path write and the liveness read must reject it, and none may touch the
// replacement's row.
func TestSupersededAttemptIsFencedAfterInfraReplace(t *testing.T) {
	f := seedStaleHeartbeat(t, "fence_lite")
	f.setState(t, "scheduled")
	old := f.dispatch(t)
	f.transition(t, domain.TaskStateQueued)
	if err := f.sched.MarkTaskDispatchLost(f.ctx, f.tiID); err != nil {
		t.Fatalf("MarkTaskDispatchLost: %v", err)
	}
	if applied, err := f.sched.ResetForInfraReplace(f.ctx, f.runUUID, "t"); err != nil || !applied {
		t.Fatalf("ResetForInfraReplace applied=%v err=%v", applied, err)
	}
	f.setState(t, "scheduled")
	replacement := f.dispatch(t)
	f.transition(t, domain.TaskStateQueued)
	if old.TryNumber != replacement.TryNumber {
		t.Fatalf("precondition: an infra re-place keeps the try (%d vs %d)", old.TryNumber, replacement.TryNumber)
	}

	wantStale(t, "a RUNNING report", f.exec.ReportState(f.ctx, old, domain.TaskStateRunning, 0, ""))
	wantStale(t, "a heartbeat", f.exec.RecordHeartbeat(f.ctx, old))
	wantStale(t, "a reschedule", f.exec.Reschedule(f.ctx, old, time.Now().Add(time.Minute)))
	if live, err := f.exec.IsTaskInstanceLive(f.ctx, old); err != nil || live {
		t.Errorf("a superseded attempt must not be live for secrets: live=%v err=%v", live, err)
	}
	var state string
	if err := f.pg.Pool.QueryRow(f.ctx, "SELECT state::text FROM task_instances WHERE id=$1::uuid", f.tiID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "queued" {
		t.Errorf("the superseded attempt's reports must not move the replacement's row, state=%s", state)
	}

	// The replacement itself is accepted.
	if err := f.exec.ReportState(f.ctx, replacement, domain.TaskStateRunning, 0, ""); err != nil {
		t.Errorf("the live attempt's RUNNING report must apply: %v", err)
	}
	if err := f.exec.RecordHeartbeat(f.ctx, replacement); err != nil {
		t.Errorf("the live attempt's heartbeat must apply: %v", err)
	}
	if live, err := f.exec.IsTaskInstanceLive(f.ctx, replacement); err != nil || !live {
		t.Errorf("the live attempt must be live: live=%v err=%v", live, err)
	}
}

// TestRepeatedDispatchFencesTheFirstPod: two dispatches of one row with no
// reset between them (the queued write of the first one failed). The first
// pod's token is superseded by the second dispatch's claim.
func TestRepeatedDispatchFencesTheFirstPod(t *testing.T) {
	f := seedStaleHeartbeat(t, "fence_redispatch")
	f.setState(t, "scheduled")
	first := f.dispatch(t)
	second := f.dispatch(t)
	f.transition(t, domain.TaskStateQueued)
	wantStale(t, "the first pod's RUNNING report", f.exec.ReportState(f.ctx, first, domain.TaskStateRunning, 0, ""))
	if err := f.exec.ReportState(f.ctx, second, domain.TaskStateRunning, 0, ""); err != nil {
		t.Errorf("the second pod's report must apply: %v", err)
	}
}

// TestLegacyTokenFence is the mixed-version rule: a token minted before the
// upgrade carries no epoch and is read as epoch 0. It keeps working for its
// own in-flight attempt (the row is still at epoch 0) and is rejected once the
// row has been dispatched again by a new binary (epoch 1 or higher).
func TestLegacyTokenFence(t *testing.T) {
	f := seedStaleHeartbeat(t, "fence_legacy")
	legacy := f.legacyIdentity(f.tryNumber(t))
	if f.attemptEpoch(t) != 0 {
		t.Fatalf("precondition: a pre-upgrade row is at epoch 0")
	}
	if err := f.exec.RecordHeartbeat(f.ctx, legacy); err != nil {
		t.Errorf("a legacy token must heartbeat its own epoch-0 attempt: %v", err)
	}
	if err := f.exec.ReportState(f.ctx, legacy, domain.TaskStateRunning, 0, ""); err != nil {
		t.Errorf("a legacy token must report for its own epoch-0 attempt: %v", err)
	}
	if live, err := f.exec.IsTaskInstanceLive(f.ctx, legacy); err != nil || !live {
		t.Errorf("a legacy token's own attempt must be live: live=%v err=%v", live, err)
	}

	// The legacy attempt is reaped and re-placed after the upgrade; the new
	// binary dispatches the replacement at epoch >= 1.
	if ok, err := f.sched.MarkTaskAgentLost(f.ctx, f.tiID); err != nil || !ok {
		t.Fatalf("MarkTaskAgentLost ok=%v err=%v", ok, err)
	}
	if applied, err := f.sched.ResetForInfraReplace(f.ctx, f.runUUID, "t"); err != nil || !applied {
		t.Fatalf("ResetForInfraReplace applied=%v err=%v", applied, err)
	}
	f.setState(t, "scheduled")
	_ = f.dispatch(t)
	f.transition(t, domain.TaskStateRunning)
	wantStale(t, "a legacy RUNNING report", f.exec.ReportState(f.ctx, legacy, domain.TaskStateRunning, 0, ""))
	wantStale(t, "a legacy heartbeat", f.exec.RecordHeartbeat(f.ctx, legacy))
	if live, err := f.exec.IsTaskInstanceLive(f.ctx, legacy); err != nil || live {
		t.Errorf("a legacy token must not be live against an epoch-1 row: live=%v err=%v", live, err)
	}
}

// TestRescheduleIsGuardedOnTheAttempt: RescheduleTaskInstance had no attempt
// guard at all, so a late poke from an earlier try parked the live retry in
// up_for_reschedule. It now matches try_number and the epoch.
func TestRescheduleIsGuardedOnTheAttempt(t *testing.T) {
	f := seedStaleHeartbeat(t, "fence_resched")
	f.setState(t, "up_for_retry")
	if applied, err := f.sched.ResetForRetry(f.ctx, f.runUUID, "t"); err != nil || !applied {
		t.Fatalf("ResetForRetry applied=%v err=%v", applied, err)
	}
	f.setState(t, "scheduled")
	live := f.dispatch(t)
	f.transition(t, domain.TaskStateRunning)

	earlierTry := f.identityFor(live.TryNumber-1, live.AttemptEpoch)
	wantStale(t, "a reschedule from an earlier try", f.exec.Reschedule(f.ctx, earlierTry, time.Now().Add(time.Minute)))
	staleEpoch := f.identityFor(live.TryNumber, live.AttemptEpoch-1)
	wantStale(t, "a reschedule from a superseded epoch", f.exec.Reschedule(f.ctx, staleEpoch, time.Now().Add(time.Minute)))
	var state string
	if err := f.pg.Pool.QueryRow(f.ctx, "SELECT state::text FROM task_instances WHERE id=$1::uuid", f.tiID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "running" {
		t.Fatalf("a stale reschedule must not park the live attempt, state=%s", state)
	}
	if err := f.exec.Reschedule(f.ctx, live, time.Now().Add(time.Minute)); err != nil {
		t.Errorf("the live attempt's reschedule must apply: %v", err)
	}
}

// TestWarmBindingAndRequeueAreGuardedOnTheEpoch: the warm ack binding and the
// reclaim requeue name one attempt; a stale epoch is a no-op on both.
func TestWarmBindingAndRequeueAreGuardedOnTheEpoch(t *testing.T) {
	f := seedStaleHeartbeat(t, "fence_warm")
	f.setState(t, "scheduled")
	stale := f.dispatch(t)
	live := f.dispatch(t)
	f.transition(t, domain.TaskStateQueued)

	if err := f.exec.BindWarmAttempt(f.ctx, f.runUUID, "t", stale.TryNumber, stale.AttemptEpoch, "warm-stale"); err != nil {
		t.Fatalf("BindWarmAttempt(stale): %v", err)
	}
	var bound *string
	if err := f.pg.Pool.QueryRow(f.ctx, "SELECT warm_worker_id FROM task_instances WHERE id=$1::uuid", f.tiID).Scan(&bound); err != nil {
		t.Fatal(err)
	}
	if bound != nil {
		t.Errorf("a stale-epoch ack must not bind a worker, bound %q", *bound)
	}
	if err := f.exec.RequeueForRedispatch(f.ctx, f.runUUID, "t", stale.TryNumber, stale.AttemptEpoch); err != nil {
		t.Fatalf("RequeueForRedispatch(stale): %v", err)
	}
	var state string
	if err := f.pg.Pool.QueryRow(f.ctx, "SELECT state::text FROM task_instances WHERE id=$1::uuid", f.tiID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "queued" {
		t.Errorf("a stale-epoch reclaim must not requeue the live attempt, state=%s", state)
	}

	if err := f.exec.BindWarmAttempt(f.ctx, f.runUUID, "t", live.TryNumber, live.AttemptEpoch, "warm-live"); err != nil {
		t.Fatalf("BindWarmAttempt(live): %v", err)
	}
	if err := f.pg.Pool.QueryRow(f.ctx, "SELECT warm_worker_id FROM task_instances WHERE id=$1::uuid", f.tiID).Scan(&bound); err != nil {
		t.Fatal(err)
	}
	if bound == nil || *bound != "warm-live" {
		t.Errorf("the live attempt's ack must bind its worker, got %v", bound)
	}
}
