//go:build integration

// Package storage_test: the infra confirmation gate (ADR 0052 amendment,
// PR B3). On Kubernetes a reaper's infra mark is provisional until the
// reconciler confirms it; the infra re-place rail refuses a provisional mark
// inside InfraConfirmMaxWait. On Lite the mark is confirmed at mark time.
package storage_test

import (
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/scheduler"
)

func (f *staleHeartbeatFixture) confirmedAt(t *testing.T) *time.Time {
	t.Helper()
	var at *time.Time
	if err := f.pg.Pool.QueryRow(f.ctx,
		"SELECT infra_confirmed_at FROM task_instances WHERE id=$1::uuid", f.tiID).Scan(&at); err != nil {
		t.Fatalf("select infra_confirmed_at: %v", err)
	}
	return at
}

func (f *staleHeartbeatFixture) markAgentLost(t *testing.T) {
	t.Helper()
	if ok, err := f.sched.MarkTaskAgentLost(f.ctx, f.tiID, f.tryNumber(t), f.attemptEpoch(t)); err != nil || !ok {
		t.Fatalf("MarkTaskAgentLost ok=%v err=%v", ok, err)
	}
}

func (f *staleHeartbeatFixture) provisional(t *testing.T) bool {
	t.Helper()
	runs, err := f.sched.ActiveRuns(f.ctx)
	if err != nil {
		t.Fatalf("ActiveRuns: %v", err)
	}
	for _, r := range runs {
		if r.RunID == f.runUUID {
			return r.InfraProvisional["t"]
		}
	}
	t.Fatalf("run %s not active", f.runUUID)
	return false
}

// TestInfraMarksAreProvisionalOnKubernetes: with provisional marks on, every
// reaper mark leaves infra_confirmed_at NULL and the planner sees the task as
// provisional; the reconciler's confirmation stamps it, guarded on the attempt.
func TestInfraMarksAreProvisionalOnKubernetes(t *testing.T) {
	marks := map[string]func(t *testing.T, f *staleHeartbeatFixture){
		"agent_lost": func(t *testing.T, f *staleHeartbeatFixture) { f.markAgentLost(t) },
		"pod_lost": func(t *testing.T, f *staleHeartbeatFixture) {
			if ok, err := f.sched.MarkTaskPodLost(f.ctx, f.tiID, f.tryNumber(t), f.attemptEpoch(t)); err != nil || !ok {
				t.Fatalf("MarkTaskPodLost ok=%v err=%v", ok, err)
			}
		},
		"dispatch_lost": func(t *testing.T, f *staleHeartbeatFixture) {
			f.setState(t, "queued")
			if ok, err := f.sched.MarkTaskDispatchLost(f.ctx, f.tiID, f.tryNumber(t), f.attemptEpoch(t)); err != nil || !ok {
				t.Fatalf("MarkTaskDispatchLost ok=%v err=%v", ok, err)
			}
		},
	}
	for name, mark := range marks {
		t.Run(name, func(t *testing.T) {
			f := seedStaleHeartbeat(t, "confirm_"+name)
			f.sched.SetProvisionalInfraMarks(true)
			mark(t, f)
			if at := f.confirmedAt(t); at != nil {
				t.Fatalf("a Kubernetes infra mark must be provisional, got confirmed at %v", at)
			}
			if !f.provisional(t) {
				t.Fatalf("the planner must see the mark as provisional")
			}
			rows, err := f.sched.ListProvisionalInfraFailures(f.ctx)
			if err != nil {
				t.Fatalf("ListProvisionalInfraFailures: %v", err)
			}
			found := false
			for _, r := range rows {
				if r.TaskInstanceID == f.tiID {
					found = r.DagRunID == f.runUUID && r.TaskID == "t" && r.TryNumber == f.tryNumber(t) && r.AttemptEpoch == f.attemptEpoch(t)
				}
			}
			if !found {
				t.Fatalf("the provisional mark must be listed with its attempt, got %+v", rows)
			}
			if ok, err := f.sched.ConfirmInfraFailure(f.ctx, f.tiID, f.tryNumber(t), f.attemptEpoch(t)+1); err != nil || ok {
				t.Fatalf("a confirmation for another epoch is a no-op: ok=%v err=%v", ok, err)
			}
			if ok, err := f.sched.ConfirmInfraFailure(f.ctx, f.tiID, f.tryNumber(t), f.attemptEpoch(t)); err != nil || !ok {
				t.Fatalf("ConfirmInfraFailure ok=%v err=%v", ok, err)
			}
			if f.confirmedAt(t) == nil || f.provisional(t) {
				t.Fatalf("the confirmed mark must no longer be provisional")
			}
			if ok, err := f.sched.ConfirmInfraFailure(f.ctx, f.tiID, f.tryNumber(t), f.attemptEpoch(t)); err != nil || ok {
				t.Fatalf("a second confirmation is a no-op: ok=%v err=%v", ok, err)
			}
		})
	}
}

// TestInfraMarksAreConfirmedOnLite: with provisional marks off (Lite, no
// reconciler), the mark is confirmed at mark time and behaves as before.
func TestInfraMarksAreConfirmedOnLite(t *testing.T) {
	f := seedStaleHeartbeat(t, "confirm_lite")
	f.markAgentLost(t)
	if f.confirmedAt(t) == nil || f.provisional(t) {
		t.Fatalf("a Lite infra mark must be confirmed at mark time")
	}
	if ok, err := f.sched.ResetForInfraReplace(f.ctx, f.runUUID, "t"); err != nil || !ok {
		t.Fatalf("a confirmed mark re-places: ok=%v err=%v", ok, err)
	}
	if f.confirmedAt(t) != nil {
		t.Errorf("the re-place clears infra_confirmed_at for the next attempt")
	}
}

// TestInfraReplaceWaitsForConfirmation: the re-place rail refuses a
// provisional mark inside InfraConfirmMaxWait, accepts it once confirmed, and
// accepts an unconfirmed one past the valve.
func TestInfraReplaceWaitsForConfirmation(t *testing.T) {
	f := seedStaleHeartbeat(t, "confirm_replace")
	f.sched.SetProvisionalInfraMarks(true)
	f.markAgentLost(t)
	if ok, err := f.sched.ResetForInfraReplace(f.ctx, f.runUUID, "t"); err != nil || ok {
		t.Fatalf("a provisional mark inside the window must not re-place: ok=%v err=%v", ok, err)
	}
	if _, err := f.pg.Pool.Exec(f.ctx, "UPDATE task_instances SET ended_at = now() - make_interval(secs => $2) WHERE id=$1::uuid",
		f.tiID, (scheduler.InfraConfirmMaxWait + time.Second).Seconds()); err != nil {
		t.Fatalf("backdate ended_at: %v", err)
	}
	if ok, err := f.sched.ResetForInfraReplace(f.ctx, f.runUUID, "t"); err != nil || !ok {
		t.Fatalf("past the valve the provisional mark re-places: ok=%v err=%v", ok, err)
	}
	var history int
	if err := f.pg.Pool.QueryRow(f.ctx, "SELECT count(*) FROM task_instance_history WHERE task_instance_id=$1::uuid", f.tiID).Scan(&history); err != nil {
		t.Fatal(err)
	}
	if history != 1 {
		t.Errorf("the refused re-place must not archive; want one history row, got %d", history)
	}

	f.setState(t, "scheduled")
	f.dispatch(t)
	f.transition(t, domain.TaskStateQueued)
	f.transition(t, domain.TaskStateRunning)
	f.markAgentLost(t)
	if ok, err := f.sched.ConfirmInfraFailure(f.ctx, f.tiID, f.tryNumber(t), f.attemptEpoch(t)); err != nil || !ok {
		t.Fatalf("ConfirmInfraFailure ok=%v err=%v", ok, err)
	}
	if ok, err := f.sched.ResetForInfraReplace(f.ctx, f.runUUID, "t"); err != nil || !ok {
		t.Fatalf("a confirmed mark re-places at once: ok=%v err=%v", ok, err)
	}
}
