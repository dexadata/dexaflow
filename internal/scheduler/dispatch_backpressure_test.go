package scheduler

import (
	"context"
	"errors"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
)

// A retriable-forever dispatch failure (cluster backpressure) must back the task
// off WITHOUT advancing DispatchAttempts toward the cap and must NEVER fail the
// task as dispatch_failed — even when the task's dispatch-attempt counter has
// already reached the exhaustion budget. This is the load-bearing guarantee of
// ADR 0053: backpressure never drives a task terminal, no matter how long the
// cluster stays full.
//
// The disposition is now classified on the execution layer and arrives typed
// over the seam (ADR 0051 Phase 4), so the scheduler test sets it directly on the
// fake dispatcher rather than round-tripping a Kubernetes apierror through a
// scheduler-side classifier that no longer exists.
func TestStepBackpressureRetriesForeverOffBudget(t *testing.T) {
	store := newFakeStore(RunState{
		RunID: "r1", DagID: "etl", State: domain.DagRunStateRunning, Tasks: linearTasks(),
		States: map[string]domain.TaskState{"a": domain.TaskStateScheduled, "b": domain.TaskStateNone},
		// Already at the budget: a permanent error here would fail the task this
		// tick. A backpressure error must not.
		DispatchAttempts: map[string]int{"a": dispatchMaxAttempts},
	})
	d := &fakeDispatcher{disp: executor.Backpressure, err: errors.New("creating pod for task a: exceeded quota: compute-resources")}
	s := newScheduler(store)
	s.SetDispatcher(d)

	if err := s.Step(context.Background()); err != nil {
		t.Fatalf("a dispatch failure must not abort the step: %v", err)
	}
	if len(store.dispatchExhausted) != 0 {
		t.Errorf("backpressure must NEVER fail the task as dispatch_failed, got %v", store.dispatchExhausted)
	}
	if len(store.dispatchBackpressure) != 1 || store.dispatchBackpressure[0].taskID != "a" {
		t.Errorf("backpressure should record a no-increment backoff for task a, got %v", store.dispatchBackpressure)
	}
	if len(store.dispatchFailures) != 0 {
		t.Errorf("backpressure must NOT use the attempt-incrementing dispatch-failure path, got %v", store.dispatchFailures)
	}
	if len(store.transitions) != 0 {
		t.Errorf("a backed-off dispatch must not transition the task, got %v", store.transitions)
	}
}

// A permanent dispatch error at the same already-exhausted counter DOES fail the
// task, proving the off-budget behavior above is specific to backpressure and the
// bounded path is unchanged for everything else.
func TestStepPermanentErrorStillExhausts(t *testing.T) {
	store := newFakeStore(RunState{
		RunID: "r1", DagID: "etl", State: domain.DagRunStateRunning, Tasks: linearTasks(),
		States:           map[string]domain.TaskState{"a": domain.TaskStateScheduled, "b": domain.TaskStateNone},
		DispatchAttempts: map[string]int{"a": dispatchMaxAttempts - 1},
	})
	d := &fakeDispatcher{disp: executor.Rejected, err: errors.New(`creating pod for task a: cannot create resource "pods" in the namespace "prod"`)}
	s := newScheduler(store)
	s.SetDispatcher(d)

	if err := s.Step(context.Background()); err != nil {
		t.Fatalf("dispatch exhaustion must not abort the step: %v", err)
	}
	if len(store.dispatchExhausted) != 1 || store.dispatchExhausted[0] != "a" {
		t.Errorf("a permanent RBAC denial at the budget should fail task a, got %v", store.dispatchExhausted)
	}
	if len(store.dispatchBackpressure) != 0 {
		t.Errorf("a permanent error must not take the backpressure path, got %v", store.dispatchBackpressure)
	}
}
