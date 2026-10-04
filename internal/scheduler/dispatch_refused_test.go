package scheduler

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
)

// TestStepRefusedDispatchFailsAtOnce: a task the executor policy refuses
// (ADR 0063) fails on its first dispatch, with the refusal as its reason,
// instead of spending the bounded dispatch retries on something that cannot
// change between attempts.
func TestStepRefusedDispatchFailsAtOnce(t *testing.T) {
	store := newFakeStore(RunState{
		RunID: "r1", DagID: "etl", State: domain.DagRunStateRunning, Tasks: linearTasks(),
		States: map[string]domain.TaskState{"a": domain.TaskStateScheduled, "b": domain.TaskStateNone},
	})
	cause := fmt.Errorf("task a: %w: image \"evil.io/x:1\" is not in images.allowed", executor.ErrPolicyRefused)
	d := &fakeDispatcher{disp: executor.Refused, err: cause}
	s := newScheduler(store)
	s.SetDispatcher(d)

	if err := s.Step(context.Background()); err != nil {
		t.Fatalf("a refusal must not abort the step: %v", err)
	}
	if len(store.dispatchExhausted) != 1 || store.dispatchExhausted[0] != "a" {
		t.Fatalf("refused task a should fail at once, got %v", store.dispatchExhausted)
	}
	if r := store.dispatchExhaustedReasons[0]; !strings.Contains(r, "images.allowed") || !strings.Contains(r, "executor policy") {
		t.Errorf("reason = %q, want it to name the policy and the rule", r)
	}
	if len(store.dispatchFailures) != 0 || len(store.dispatchBackpressure) != 0 {
		t.Errorf("a refusal must not back off: failures=%v backpressure=%v", store.dispatchFailures, store.dispatchBackpressure)
	}
}

// TestAsyncRefusedDispatchFailsAtOnce: a refusal reported by a buffered
// dispatch worker fails the task on its first attempt, like the synchronous
// path, instead of being re-offered with a backoff.
func TestAsyncRefusedDispatchFailsAtOnce(t *testing.T) {
	st := &fakeAsyncStore{active: true}
	h := NewAsyncDispatchFailures(st, quietLogger())
	cause := fmt.Errorf("task a: %w: image \"evil.io/x:1\" is not in images.allowed", executor.ErrPolicyRefused)
	if err := h.HandleDispatchFailure(context.Background(), "r1", "a", executor.Refused, cause); err != nil {
		t.Fatal(err)
	}
	if len(st.requeued) != 0 {
		t.Errorf("a refusal was re-offered: %v", st.requeued)
	}
	if len(st.failed) != 1 || !strings.Contains(st.failNotes[0], "images.allowed") {
		t.Errorf("failed = %v, want one failure naming the rule", st.failed)
	}
}
