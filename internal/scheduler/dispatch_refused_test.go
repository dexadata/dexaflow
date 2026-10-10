package scheduler

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
)

// A Refused dispatch (ADR 0066 §3) is a permanent "this task may not run here":
// it fails the task on the first attempt, with the message as its reason, and
// spends neither dispatch retries nor the task's own retries on a verdict a
// retry cannot change.

// TestStepRefusedDispatchFailsOnFirstAttempt: with a fresh dispatch budget, a
// Refused dispatch fails the task now instead of backing off.
func TestStepRefusedDispatchFailsOnFirstAttempt(t *testing.T) {
	// Arrange
	store := newFakeStore(RunState{
		RunID: "r1", DagID: "etl", State: domain.DagRunStateRunning, Tasks: linearTasks(),
		States: map[string]domain.TaskState{"a": domain.TaskStateScheduled, "b": domain.TaskStateNone},
	})
	d := &fakeDispatcher{disp: executor.Refused, err: errors.New(`task "a" declares resources larger than its size`)}
	s := newScheduler(store)
	s.SetDispatcher(d)

	// Act
	err := s.Step(context.Background())

	// Assert
	if err != nil {
		t.Fatalf("a refused dispatch must not abort the step: %v", err)
	}
	if len(store.dispatchRefused) != 1 || store.dispatchRefused[0] != "a" {
		t.Errorf("a refused dispatch should fail task a at once, got %v", store.dispatchRefused)
	}
	if len(store.dispatchExhausted) != 0 {
		t.Errorf("a refused dispatch is not a spent dispatch budget, got %v", store.dispatchExhausted)
	}
	if len(store.dispatchFailures) != 0 || len(store.dispatchBackpressure) != 0 {
		t.Errorf("a refused dispatch must not back off, got failures=%v backpressure=%v",
			store.dispatchFailures, store.dispatchBackpressure)
	}
}

// TestAsyncRefusedDispatchFailsOnFirstAttempt: the buffered path does the same,
// with the refusal message as the failure reason.
func TestAsyncRefusedDispatchFailsOnFirstAttempt(t *testing.T) {
	// Arrange
	st := &fakeAsyncStore{attempts: 0, active: true}
	h := NewAsyncDispatchFailures(st, quietLogger())
	cause := errors.New(`task "a" is size 80, above executor.unit.max_size 64`)

	// Act
	err := h.HandleDispatchFailure(context.Background(), "r1", "a", executor.Refused, cause)

	// Assert
	if err != nil {
		t.Fatal(err)
	}
	if len(st.requeued) != 0 || len(st.failed) != 0 || len(st.refused) != 1 {
		t.Fatalf("refused=%v failed=%v requeued=%v, want the task failed as refused at once",
			st.refused, st.failed, st.requeued)
	}
	if !strings.Contains(st.failNotes[0], cause.Error()) {
		t.Errorf("fail reason = %q, want it to carry %q", st.failNotes[0], cause.Error())
	}
}

// TestStepRefusedDispatchIsNotRetried: the task's own retries are not spent on
// a refusal either. A refused task with retries left is failed for good, so the
// planner does not move it to up_for_retry, dispatch it again and have it
// refused again once per retry, each after retry_delay.
func TestStepRefusedDispatchIsNotRetried(t *testing.T) {
	// Arrange
	store := newFakeStore(RunState{
		RunID: "r1", DagID: "etl", State: domain.DagRunStateRunning, Tasks: linearTasks(),
		States:   map[string]domain.TaskState{"a": domain.TaskStateScheduled, "b": domain.TaskStateNone},
		Tries:    map[string]int{"a": 1, "b": 1},
		MaxTries: map[string]int{"a": 3, "b": 3}, // retries: 2
	})
	s := newScheduler(store)
	s.SetDispatcher(&fakeDispatcher{disp: executor.Refused, err: errors.New(`task "a" is size 80, above executor.unit.max_size 64`)})

	// Act: the first tick is refused, the second plans what follows.
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Assert
	if len(store.dispatchRefused) != 1 || store.dispatchRefused[0] != "a" {
		t.Fatalf("want task a failed as refused once, got %v", store.dispatchRefused)
	}
	if hasTransition(store.transitions, "a", domain.TaskStateUpForRetry) {
		t.Errorf("a refused dispatch went to up_for_retry, so it would be refused again: %v", store.transitions)
	}
}
