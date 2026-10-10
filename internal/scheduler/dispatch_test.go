package scheduler

import (
	"context"
	"errors"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
)

type fakeDispatcher struct {
	dispatched []string
	// dagVersions records the dag_version_id passed to each Dispatch call, in the
	// same order as dispatched, so a test can assert RunState.DagVersionID reaches
	// the dispatcher (ADR 0058 N1b1-place placement key).
	dagVersions []string
	// disp is the typed disposition returned alongside err. It defaults to the
	// zero value (executor.Dispatched); a test that exercises a failure path sets
	// it to executor.Rejected or executor.Backpressure to mirror what the real
	// executor would classify the error as.
	disp executor.Disposition
	err  error
}

func (d *fakeDispatcher) Dispatch(_ context.Context, _, _, dagVersionID string, task domain.TaskSpec) (executor.Disposition, error) {
	d.dispatched = append(d.dispatched, task.TaskID)
	d.dagVersions = append(d.dagVersions, dagVersionID)
	return d.disp, d.err
}

func hasTransition(ts []transition, taskID string, to domain.TaskState) bool {
	for _, tr := range ts {
		if tr.taskID == taskID && tr.to == to {
			return true
		}
	}
	return false
}

func TestStepUsesDispatcherForPodTasks(t *testing.T) {
	store := runWithScheduledRoot() // python task "a"
	dispatcher := &fakeDispatcher{}
	s := newScheduler(store)
	s.SetDispatcher(dispatcher)

	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(dispatcher.dispatched) != 1 || !hasTransition(store.transitions, "a", domain.TaskStateQueued) {
		t.Errorf("python task should dispatch then queue, got dispatched=%v transitions=%v", dispatcher.dispatched, store.transitions)
	}
}

// TestStepThreadsDagVersionToDispatcher locks the N1b1-place plumbing: the
// dag_version_id carried on RunState reaches the dispatcher, which uses it as the
// warm-worker placement key. Without this thread the placer could never target
// the right pool.
func TestStepThreadsDagVersionToDispatcher(t *testing.T) {
	store := newFakeStore(RunState{
		RunID: "r1", DagID: "etl", DagVersionID: "ver-42",
		State: domain.DagRunStateRunning, Tasks: linearTasks(),
		States: map[string]domain.TaskState{"a": domain.TaskStateScheduled, "b": domain.TaskStateNone},
	})
	d := &fakeDispatcher{}
	s := newScheduler(store)
	s.SetDispatcher(d)

	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(d.dagVersions) != 1 || d.dagVersions[0] != "ver-42" {
		t.Errorf("dispatcher got dag_versions %v, want [ver-42]", d.dagVersions)
	}
}

func runWithScheduledRoot() *fakeStore {
	return newFakeStore(RunState{
		RunID: "r1", DagID: "etl", State: domain.DagRunStateRunning, Tasks: linearTasks(),
		States: map[string]domain.TaskState{"a": domain.TaskStateScheduled, "b": domain.TaskStateNone},
	})
}

func TestStepDispatchesQueuedTaskBeforeTransition(t *testing.T) {
	store := runWithScheduledRoot()
	d := &fakeDispatcher{}
	s := newScheduler(store)
	s.SetDispatcher(d)

	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(d.dispatched) != 1 || d.dispatched[0] != "a" {
		t.Errorf("expected task a dispatched once, got %v", d.dispatched)
	}
	if len(store.transitions) != 1 || store.transitions[0] != (transition{"r1", "a", domain.TaskStateQueued}) {
		t.Errorf("expected a->queued after dispatch, got %v", store.transitions)
	}
}

func TestStepLeavesTaskScheduledWhenDispatchFails(t *testing.T) {
	store := runWithScheduledRoot()
	d := &fakeDispatcher{disp: executor.Rejected, err: errors.New("executor unavailable")}
	s := newScheduler(store)
	s.SetDispatcher(d)

	if err := s.Step(context.Background()); err != nil {
		t.Fatalf("a dispatch failure must not abort the step: %v", err)
	}
	if len(d.dispatched) != 1 {
		t.Errorf("dispatch should have been attempted once, got %v", d.dispatched)
	}
	if len(store.transitions) != 0 {
		t.Errorf("failed dispatch must leave the task scheduled, got %v", store.transitions)
	}
}

func TestStepWithoutDispatcherFailsUndispatchable(t *testing.T) {
	// With no dispatcher and no inline runner the task can never run, so the
	// scheduler fails it fast (with a note) instead of queuing it forever (#50).
	store := runWithScheduledRoot()
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.transitions) != 1 || store.transitions[0].to != domain.TaskStateFailed {
		t.Errorf("state-only scheduler should fail the undispatchable task, got %v", store.transitions)
	}
}

// A synchronous dispatch failure records a backed-off retry (ADR 0031 Amendment
// A) instead of silently re-attempting every tick. The task stays scheduled; the
// backoff is what the planner gates the next attempt on.
func TestStepBacksOffOnDispatchFailure(t *testing.T) {
	store := runWithScheduledRoot()
	d := &fakeDispatcher{disp: executor.Rejected, err: errors.New("kube-apiserver unreachable")}
	s := newScheduler(store)
	s.SetDispatcher(d)

	if err := s.Step(context.Background()); err != nil {
		t.Fatalf("a dispatch failure must not abort the step: %v", err)
	}
	if len(store.dispatchFailures) != 1 || store.dispatchFailures[0].taskID != "a" {
		t.Errorf("first dispatch failure should record a backoff for task a, got %v", store.dispatchFailures)
	}
	if len(store.dispatchExhausted) != 0 {
		t.Errorf("one failure must not exhaust the budget, got %v", store.dispatchExhausted)
	}
	if len(store.transitions) != 0 {
		t.Errorf("a failed dispatch must not transition the task, got %v", store.transitions)
	}
}

// Once the dispatch-attempt budget is spent, the task is failed as
// dispatch_failed so the run can finalize instead of looping forever.
func TestStepFailsTaskWhenDispatchExhausted(t *testing.T) {
	store := newFakeStore(RunState{
		RunID: "r1", DagID: "etl", State: domain.DagRunStateRunning, Tasks: linearTasks(),
		States:           map[string]domain.TaskState{"a": domain.TaskStateScheduled, "b": domain.TaskStateNone},
		DispatchAttempts: map[string]int{"a": dispatchMaxAttempts - 1}, // one more failure exhausts it
	})
	d := &fakeDispatcher{disp: executor.Rejected, err: errors.New("RBAC: cannot create pods")}
	s := newScheduler(store)
	s.SetDispatcher(d)

	if err := s.Step(context.Background()); err != nil {
		t.Fatalf("dispatch exhaustion must not abort the step: %v", err)
	}
	if len(store.dispatchExhausted) != 1 || store.dispatchExhausted[0] != "a" {
		t.Errorf("the final dispatch failure should fail task a as dispatch_failed, got %v", store.dispatchExhausted)
	}
	if len(store.dispatchFailures) != 0 {
		t.Errorf("the exhausting attempt must fail, not back off again, got %v", store.dispatchFailures)
	}
}
