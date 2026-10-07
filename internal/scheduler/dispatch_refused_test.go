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
// spends no dispatch retries on a verdict a retry cannot change.

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
	if len(store.dispatchExhausted) != 1 || store.dispatchExhausted[0] != "a" {
		t.Errorf("a refused dispatch should fail task a at once, got %v", store.dispatchExhausted)
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
	if len(st.requeued) != 0 || len(st.failed) != 1 {
		t.Fatalf("failed=%v requeued=%v, want the task failed at once", st.failed, st.requeued)
	}
	if !strings.Contains(st.failNotes[0], cause.Error()) {
		t.Errorf("fail reason = %q, want it to carry %q", st.failNotes[0], cause.Error())
	}
}
