//go:build integration

// Package storage_test: the attempt-epoch fence on the control-plane writes
// (ADR 0051 amendment, PR A4, #1130, #901).
//
// The reconciler's three settles and the reapers' three marks match the
// attempt on (try_number, attempt_epoch). A settle read from a superseded pod
// (whose label names an older epoch, or none) and a mark computed for a
// superseded attempt are no-ops on the replacement.
package storage_test

import (
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
)

func (f *staleHeartbeatFixture) state(t *testing.T) string {
	t.Helper()
	var s string
	if err := f.pg.Pool.QueryRow(f.ctx,
		"SELECT state::text FROM task_instances WHERE dag_run_id=$1::uuid AND task_id='t'", f.runUUID).Scan(&s); err != nil {
		t.Fatalf("select state: %v", err)
	}
	return s
}

func (f *staleHeartbeatFixture) wantState(t *testing.T, what, want string) {
	t.Helper()
	if got := f.state(t); got != want {
		t.Errorf("%s: state = %s, want %s", what, got, want)
	}
}

// TestReconcilerSettleIsFencedOnTheEpoch is #1130 at the storage layer: a TI
// at try 1 epoch 1 in queued, and a settle carrying try 1 and epoch 0 (the
// superseded pod, unlabeled). None of the three settles may move the row; the
// replacement's own settle does.
func TestReconcilerSettleIsFencedOnTheEpoch(t *testing.T) {
	f := seedStaleHeartbeat(t, "settle_fence")
	f.setState(t, "scheduled")
	cur := f.dispatch(t)
	f.transition(t, domain.TaskStateQueued)
	if cur.TryNumber != 1 || cur.AttemptEpoch != 1 {
		t.Fatalf("precondition: try 1 epoch 1, got try %d epoch %d", cur.TryNumber, cur.AttemptEpoch)
	}

	if err := f.exec.SucceedTask(f.ctx, f.tiID, 1, 0); err != nil {
		t.Fatalf("SucceedTask: %v", err)
	}
	f.wantState(t, "a superseded pod's SUCCESS record", "queued")
	if err := f.exec.FailTask(f.ctx, f.tiID, 1, 0, "stale"); err != nil {
		t.Fatalf("FailTask: %v", err)
	}
	f.wantState(t, "a superseded pod's failure", "queued")
	if err := f.exec.RescheduleTask(f.ctx, f.tiID, 1, 0, time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("RescheduleTask: %v", err)
	}
	f.wantState(t, "a superseded pod's reschedule", "queued")

	if err := f.exec.SucceedTask(f.ctx, f.tiID, 1, 1); err != nil {
		t.Fatalf("SucceedTask: %v", err)
	}
	f.wantState(t, "the attempt's own SUCCESS record", "success")
}

// TestReaperMarksAreFencedOnTheEpoch: each reaper mark names the attempt it
// listed. A mark carrying a superseded epoch is a no-op on the replacement,
// and the list queries hand the reapers the current epoch to mark with.
func TestReaperMarksAreFencedOnTheEpoch(t *testing.T) {
	t.Run("agent_lost", func(t *testing.T) {
		f := seedStaleHeartbeat(t, "mark_agent_lost")
		f.setState(t, "scheduled")
		cur := f.dispatch(t)
		f.transition(t, domain.TaskStateQueued)
		f.transition(t, domain.TaskStateRunning)
		cands, err := f.sched.ListAgentLostCandidates(f.ctx)
		if err != nil {
			t.Fatalf("ListAgentLostCandidates: %v", err)
		}
		c := findAgentLostCandidate(cands, f.runUUID)
		if c == nil || c.AttemptEpoch != cur.AttemptEpoch {
			t.Fatalf("the candidate must carry the current epoch %d, got %+v", cur.AttemptEpoch, c)
		}
		if applied, err := f.sched.MarkTaskAgentLost(f.ctx, f.tiID, 1, cur.AttemptEpoch-1); err != nil || applied {
			t.Fatalf("a stale-epoch mark must be a no-op: applied=%v err=%v", applied, err)
		}
		f.wantState(t, "a stale agent-lost mark", "running")
		if applied, err := f.sched.MarkTaskAgentLost(f.ctx, f.tiID, 1, cur.AttemptEpoch); err != nil || !applied {
			t.Fatalf("the current attempt's mark must apply: applied=%v err=%v", applied, err)
		}
		f.wantState(t, "the current agent-lost mark", "failed")
	})
	t.Run("pod_lost", func(t *testing.T) {
		f := seedStaleHeartbeat(t, "mark_pod_lost")
		f.setState(t, "scheduled")
		cur := f.dispatch(t)
		f.transition(t, domain.TaskStateQueued)
		f.transition(t, domain.TaskStateRunning)
		if _, err := f.pg.Pool.Exec(f.ctx,
			"UPDATE task_instances SET started_at = now() - interval '1 hour' WHERE id=$1::uuid", f.tiID); err != nil {
			t.Fatalf("backdate started_at: %v", err)
		}
		cands, err := f.sched.ListRunningTasks(f.ctx, time.Minute)
		if err != nil {
			t.Fatalf("ListRunningTasks: %v", err)
		}
		found := false
		for _, c := range cands {
			if c.TaskInstanceID == f.tiID {
				found = true
				if c.AttemptEpoch != cur.AttemptEpoch {
					t.Errorf("the candidate must carry the current epoch %d, got %d", cur.AttemptEpoch, c.AttemptEpoch)
				}
			}
		}
		if !found {
			t.Fatalf("precondition: the running TI is a pod-lost candidate")
		}
		if applied, err := f.sched.MarkTaskPodLost(f.ctx, f.tiID, 1, cur.AttemptEpoch-1); err != nil || applied {
			t.Fatalf("a stale-epoch mark must be a no-op: applied=%v err=%v", applied, err)
		}
		if applied, err := f.sched.MarkTaskPodLost(f.ctx, f.tiID, 2, cur.AttemptEpoch); err != nil || applied {
			t.Fatalf("a stale-try mark must be a no-op: applied=%v err=%v", applied, err)
		}
		f.wantState(t, "a stale pod-lost mark", "running")
		if applied, err := f.sched.MarkTaskPodLost(f.ctx, f.tiID, 1, cur.AttemptEpoch); err != nil || !applied {
			t.Fatalf("the current attempt's mark must apply: applied=%v err=%v", applied, err)
		}
		f.wantState(t, "the current pod-lost mark", "failed")
	})
	t.Run("dispatch_lost", func(t *testing.T) {
		f := seedStaleHeartbeat(t, "mark_dispatch_lost")
		f.setState(t, "scheduled")
		cur := f.dispatch(t)
		f.transition(t, domain.TaskStateQueued)
		cands, err := f.sched.ListStaleQueuedCandidates(f.ctx)
		if err != nil {
			t.Fatalf("ListStaleQueuedCandidates: %v", err)
		}
		found := false
		for _, c := range cands {
			if c.TaskInstanceID == f.tiID {
				found = true
				if c.AttemptEpoch != cur.AttemptEpoch {
					t.Errorf("the candidate must carry the current epoch %d, got %d", cur.AttemptEpoch, c.AttemptEpoch)
				}
			}
		}
		if !found {
			t.Fatalf("precondition: the queued TI is a dispatch-lost candidate")
		}
		if applied, err := f.sched.MarkTaskDispatchLost(f.ctx, f.tiID, 1, cur.AttemptEpoch-1); err != nil || applied {
			t.Fatalf("a stale MarkTaskDispatchLost must be a no-op: applied=%v err=%v", applied, err)
		}
		f.wantState(t, "a stale dispatch-lost mark", "queued")
		if applied, err := f.sched.MarkTaskDispatchLost(f.ctx, f.tiID, 1, cur.AttemptEpoch); err != nil || !applied {
			t.Fatalf("the current MarkTaskDispatchLost must apply: applied=%v err=%v", applied, err)
		}
		f.wantState(t, "the current dispatch-lost mark", "failed")
	})
}
