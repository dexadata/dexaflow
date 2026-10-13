package scheduler

import (
	"context"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

// TestFlushTransitionsGuardsOnTheSnapshotState (#1605): the tick's batched
// plain transitions are conditioned on the state the tick's snapshot read, so
// a row a concurrent writer moved after the snapshot is not overwritten.
func TestFlushTransitionsGuardsOnTheSnapshotState(t *testing.T) {
	store := newFakeStore(RunState{
		RunID: "r1", DagID: "etl", State: domain.DagRunStateRunning, Tasks: linearTasks(),
		States: map[string]domain.TaskState{"a": domain.TaskStateSuccess, "b": domain.TaskStateNone},
	})
	s := newScheduler(store)

	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	from, ok := store.batchFrom["b"]
	if !ok {
		t.Fatal("b's none -> scheduled write did not go through the batched ApplyTransitions")
	}
	if from != domain.TaskStateNone {
		t.Errorf("b's batched write was guarded on %q, want the snapshot's %q", from, domain.TaskStateNone)
	}
}

// TestFlushTransitionsSupersededWriteIsNotAnError: a guarded batch write that
// finds the row already moved on is the guard working, not a tick failure,
// and the transition it did not make is not recorded.
func TestFlushTransitionsSupersededWriteIsNotAnError(t *testing.T) {
	store := newFakeStore(RunState{
		RunID: "r1", DagID: "etl", State: domain.DagRunStateRunning, Tasks: linearTasks(),
		States: map[string]domain.TaskState{"a": domain.TaskStateSuccess, "b": domain.TaskStateNone},
	})
	store.batchSuperseded = map[string]bool{"b": true}
	s := newScheduler(store)

	if err := s.Step(context.Background()); err != nil {
		t.Fatalf("Step: %v", err)
	}
	if hasTransition(store.transitions, "b", domain.TaskStateScheduled) {
		t.Error("a superseded batched write must not be recorded as a transition")
	}
}

// TestEagerPromotionSkipsASupersededBatchedWrite (#1605): the same-tick
// re-plan folds only the batched rows that actually moved. A task whose
// none -> scheduled write found the row already moved on (a user's mark, an
// overlapping leader's dispatch) is not queued or dispatched by the re-plan.
func TestEagerPromotionSkipsASupersededBatchedWrite(t *testing.T) {
	store := newFakeStore(diamondAfterRoot())
	store.batchSuperseded = map[string]bool{"b": true}
	disp := &fakeDispatcher{}
	s := newScheduler(store)
	s.SetDispatcher(disp)
	s.EnableEagerPromotion()

	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if hasTransition(store.transitions, "b", domain.TaskStateQueued) {
		t.Errorf("b's batched write was superseded; the re-plan must not queue it, got %v", store.transitions)
	}
	for _, d := range disp.dispatched {
		if d == "b" {
			t.Errorf("b's batched write was superseded; it must not dispatch, got %v", disp.dispatched)
		}
	}
	if !hasTransition(store.transitions, "c", domain.TaskStateQueued) {
		t.Errorf("c moved; it should still be queued in the same tick, got %v", store.transitions)
	}
}
