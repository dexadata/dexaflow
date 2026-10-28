//go:build integration

// Package storage_test: the reconciler's infra settle for a pod the cluster
// stopped (R-02, #1595). An eviction or a graceful node shutdown fails the
// attempt on the infra rail (ADR 0051), so the task keeps its own retries.
package storage_test

import (
	"testing"
)

func (f *staleHeartbeatFixture) infraFailed(t *testing.T) bool {
	t.Helper()
	runs, err := f.sched.ActiveRuns(f.ctx)
	if err != nil {
		t.Fatalf("ActiveRuns: %v", err)
	}
	for _, r := range runs {
		if r.RunID == f.runUUID {
			return r.InfraFailed["t"]
		}
	}
	t.Fatalf("run %s not active", f.runUUID)
	return false
}

// TestFailTaskInfraSettlesOnTheInfraRail: the settle marks the attempt failed
// with last_failure_kind 'infra', confirmed at once (the terminal pod is the
// confirmation), keeps the try number, and the infra re-place rail takes it.
func TestFailTaskInfraSettlesOnTheInfraRail(t *testing.T) {
	f := seedStaleHeartbeat(t, "disrupted_infra")
	f.sched.SetProvisionalInfraMarks(true)
	try, epoch := f.tryNumber(t), f.attemptEpoch(t)

	if err := f.exec.FailTaskInfra(f.ctx, f.tiID, try, epoch, "pod_disrupted: evicted"); err != nil {
		t.Fatalf("FailTaskInfra: %v", err)
	}
	f.wantState(t, "after the infra settle", "failed")
	if k := f.failureKind(t); k == nil || *k != "infra" {
		t.Fatalf("last_failure_kind = %v, want infra", k)
	}
	if f.confirmedAt(t) == nil {
		t.Fatalf("the reconciler's own infra settle must be confirmed at once")
	}
	if !f.infraFailed(t) || f.provisional(t) {
		t.Fatalf("the planner must see a confirmed infra failure")
	}
	var msg string
	if err := f.pg.Pool.QueryRow(f.ctx, "SELECT error_message FROM task_instances WHERE id=$1::uuid", f.tiID).Scan(&msg); err != nil {
		t.Fatalf("select error_message: %v", err)
	}
	if msg != "pod_disrupted: evicted" {
		t.Errorf("error_message = %q, want the settle's reason", msg)
	}
	if ok, err := f.sched.ResetForInfraReplace(f.ctx, f.runUUID, "t"); err != nil || !ok {
		t.Fatalf("the infra settle must re-place: ok=%v err=%v", ok, err)
	}
	if got := f.tryNumber(t); got != try {
		t.Errorf("the infra re-place must keep try_number %d, got %d", try, got)
	}
}

// TestFailTaskInfraIsAttemptGuarded: a settle for another attempt epoch, or
// for a terminal row, changes nothing (ADR 0051 amendment, #1130).
func TestFailTaskInfraIsAttemptGuarded(t *testing.T) {
	f := seedStaleHeartbeat(t, "disrupted_guard")
	try, epoch := f.tryNumber(t), f.attemptEpoch(t)

	if err := f.exec.FailTaskInfra(f.ctx, f.tiID, try, epoch+1, "pod_disrupted: stale"); err != nil {
		t.Fatalf("FailTaskInfra (stale epoch): %v", err)
	}
	f.wantState(t, "after a stale-epoch settle", "running")
	if err := f.exec.FailTaskInfra(f.ctx, f.tiID, try+1, epoch, "pod_disrupted: stale"); err != nil {
		t.Fatalf("FailTaskInfra (stale try): %v", err)
	}
	f.wantState(t, "after a stale-try settle", "running")

	f.setState(t, "success")
	if err := f.exec.FailTaskInfra(f.ctx, f.tiID, try, epoch, "pod_disrupted: late"); err != nil {
		t.Fatalf("FailTaskInfra (terminal): %v", err)
	}
	f.wantState(t, "after a settle on a terminal row", "success")
	if k := f.failureKind(t); k != nil {
		t.Errorf("a terminal row must keep no failure kind, got %q", *k)
	}
}

// TestFailTaskInfraSettlesAQueuedAttempt: an agent whose control plane never
// answered before the task's code ran (R-01, #1594) exits before it reports
// running, so the reconciler settles its unexecutable record on a row still
// in queued. The settle and the re-place must take that row too, keeping the
// task's try number.
func TestFailTaskInfraSettlesAQueuedAttempt(t *testing.T) {
	f := seedStaleHeartbeat(t, "unexecutable_start")
	f.setState(t, "queued")
	try, epoch := f.tryNumber(t), f.attemptEpoch(t)

	if err := f.exec.FailTaskInfra(f.ctx, f.tiID, try, epoch, "control_plane_unavailable: no answer"); err != nil {
		t.Fatalf("FailTaskInfra: %v", err)
	}
	f.wantState(t, "after the infra settle of a queued attempt", "failed")
	if k := f.failureKind(t); k == nil || *k != "infra" {
		t.Fatalf("last_failure_kind = %v, want infra", k)
	}
	if ok, err := f.sched.ResetForInfraReplace(f.ctx, f.runUUID, "t"); err != nil || !ok {
		t.Fatalf("the infra settle must re-place: ok=%v err=%v", ok, err)
	}
	if got := f.tryNumber(t); got != try {
		t.Errorf("the infra re-place must keep try_number %d, got %d", try, got)
	}
}
