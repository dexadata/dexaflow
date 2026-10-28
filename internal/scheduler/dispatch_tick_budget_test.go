package scheduler

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
)

// fakeClock is a manual clock the tick reads through Scheduler.clock.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

// slowDispatcher accepts every dispatch and advances the clock by cost, the
// way a dispatch held by the Kubernetes client's rate limiter takes wall time.
// It records the run of each dispatch so a test can see which run went first.
type slowDispatcher struct {
	clock *fakeClock
	cost  time.Duration
	runs  []string
	tasks []string
	// onDispatch, when set, runs before each dispatch is accepted.
	onDispatch func()
}

func (d *slowDispatcher) Dispatch(_ context.Context, runID, _, _ string, task domain.TaskSpec) (executor.Disposition, error) {
	if d.onDispatch != nil {
		d.onDispatch()
	}
	d.clock.now = d.clock.now.Add(d.cost)
	d.runs = append(d.runs, runID)
	d.tasks = append(d.tasks, task.TaskID)
	return executor.Dispatched, nil
}

// readyRun builds a running run of n independent tasks, every one scheduled
// and ready to dispatch.
func readyRun(runID, tenantID string, n int) RunState {
	run := RunState{RunID: runID, DagID: "d" + runID, TenantID: tenantID, State: domain.DagRunStateRunning,
		States: map[string]domain.TaskState{}}
	for i := 0; i < n; i++ {
		tid := fmt.Sprintf("%s-t%03d", runID, i)
		run.Tasks = append(run.Tasks, domain.TaskSpec{TaskID: tid, Type: "python"})
		run.States[tid] = domain.TaskStateScheduled
	}
	return run
}

func countRun(runs []string, runID string) int {
	n := 0
	for _, r := range runs {
		if r == runID {
			n++
		}
	}
	return n
}

// One wide run at the head of the queue must not spend the whole tick on
// dispatch (R-03, #1596). Once the tick's dispatch budget is spent the tick
// stops dispatching: the rest of the run's tasks stay scheduled with nothing
// written (no attempt charged, no backoff), and the next tick starts at the run
// after the one that was cut, so a later run is not starved behind it.
func TestStepStopsDispatchingWhenTheTickBudgetIsSpent(t *testing.T) {
	wide, later := readyRun("a", "t1", 100), readyRun("b", "t2", 3)
	store := newFakeStore(wide, later)
	clock := &fakeClock{now: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)}
	disp := &slowDispatcher{clock: clock, cost: 200 * time.Millisecond}
	s := newScheduler(store)
	s.clock = clock.Now
	s.SetDispatcher(disp)
	s.SetDispatchTickBudget(time.Second)

	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(disp.runs); got != 5 {
		t.Fatalf("a 1s budget at 200ms a dispatch must stop after 5 dispatches, got %d", got)
	}
	if countRun(disp.runs, "b") != 0 {
		t.Fatalf("the wide run started the tick, so the budget runs out inside it, got %v", disp.runs)
	}
	if len(store.dispatchFailures)+len(store.dispatchBackpressure)+len(store.dispatchExhausted) != 0 {
		t.Errorf("a task the budget left undispatched must be left untouched, got failures=%v backpressure=%v exhausted=%v",
			store.dispatchFailures, store.dispatchBackpressure, store.dispatchExhausted)
	}
	queued := 0
	for _, tr := range store.transitions {
		if tr.to == domain.TaskStateQueued {
			queued++
		}
	}
	if queued != 5 {
		t.Errorf("only the dispatched tasks move to queued, got %d queued writes", queued)
	}

	disp.runs = nil
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(disp.runs) == 0 || disp.runs[0] != "b" {
		t.Fatalf("the tick after a cut must start at the run after the cut one, got %v", disp.runs)
	}
	if countRun(disp.runs, "b") != 3 {
		t.Errorf("the later run must dispatch all of its tasks before the wide run resumes, got %v", disp.runs)
	}
}

// A zero budget keeps the historical behavior: the tick dispatches every ready
// task, however long it takes.
func TestStepDispatchTickBudgetZeroDispatchesEverything(t *testing.T) {
	store := newFakeStore(readyRun("a", "t1", 50))
	clock := &fakeClock{now: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)}
	disp := &slowDispatcher{clock: clock, cost: time.Second}
	s := newScheduler(store)
	s.clock = clock.Now
	s.SetDispatcher(disp)
	s.SetDispatchTickBudget(0)

	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(disp.runs) != 50 {
		t.Errorf("with no budget every ready task is dispatched, got %d", len(disp.runs))
	}
}

// The default budget is on: the issue is reachable with default settings, so
// the fix must be too.
func TestNewSchedulerHasADefaultDispatchTickBudget(t *testing.T) {
	s := newScheduler(newFakeStore())
	if s.dispatchTickBudget != DefaultDispatchTickBudget || DefaultDispatchTickBudget <= 0 {
		t.Errorf("default dispatch tick budget = %s, want %s (> 0)", s.dispatchTickBudget, DefaultDispatchTickBudget)
	}
}

// A per-tenant cap bounds how many dispatches one tenant takes in a tick, so a
// tenant with a wide run (or many runs) leaves dispatch room for the others.
func TestStepCapsDispatchesPerTenantPerTick(t *testing.T) {
	store := newFakeStore(readyRun("a1", "t1", 10), readyRun("a2", "t1", 10), readyRun("b", "t2", 3))
	clock := &fakeClock{now: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)}
	disp := &slowDispatcher{clock: clock, cost: time.Millisecond}
	s := newScheduler(store)
	s.clock = clock.Now
	s.SetDispatcher(disp)
	s.SetDispatchTenantLimit(4)

	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	t1 := countRun(disp.runs, "a1") + countRun(disp.runs, "a2")
	if t1 != 4 {
		t.Errorf("tenant t1 must dispatch at most 4 tasks this tick, got %d (%v)", t1, disp.runs)
	}
	if got := countRun(disp.runs, "b"); got != 3 {
		t.Errorf("tenant t2 must still dispatch its 3 tasks, got %d", got)
	}
	if len(store.dispatchFailures)+len(store.dispatchBackpressure)+len(store.dispatchExhausted) != 0 {
		t.Errorf("a task held by the tenant cap must be left untouched")
	}
}

// Cron run creation happens before dispatch, so a tick that spends its time on
// dispatch (or runs into its step timeout there) still creates the due runs
// (R-03: with catchup=false a slot missed this way is lost).
func TestStepCreatesDueRunsBeforeDispatching(t *testing.T) {
	store := newFakeStore(readyRun("a", "t1", 3))
	clock := &fakeClock{now: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)}
	// Run creation reads the scheduler's clock, so the last run is dated on it.
	last := clock.now.Add(-2 * time.Hour)
	store.scheduled = []ScheduledDAG{{TenantID: "t2", DagID: "hourly", Schedule: "0 * * * *", LastLogical: &last}}
	disp := &slowDispatcher{clock: clock, cost: time.Millisecond}
	createdBeforeFirstDispatch := -1
	disp.onDispatch = func() {
		if createdBeforeFirstDispatch < 0 {
			createdBeforeFirstDispatch = len(store.createdRuns)
		}
	}
	s := newScheduler(store)
	s.clock = clock.Now
	s.SetDispatcher(disp)

	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if createdBeforeFirstDispatch <= 0 {
		t.Errorf("due cron runs must be created before the tick dispatches, created before first dispatch = %d", createdBeforeFirstDispatch)
	}
}

// A run-creation error still lets the tick advance and dispatch the active
// runs, and is still returned for the caller to log.
func TestStepDispatchesEvenWhenRunCreationFails(t *testing.T) {
	store := newFakeStore(readyRun("a", "t1", 2))
	store.scheduledErr = fmt.Errorf("listing broke")
	disp := &fakeDispatcher{}
	s := newScheduler(store)
	s.SetDispatcher(disp)

	if err := s.Step(context.Background()); err == nil {
		t.Error("the run-creation error must still be returned")
	}
	if len(disp.dispatched) != 2 {
		t.Errorf("active runs must still dispatch, got %v", disp.dispatched)
	}
}

// A buffered worker whose inner dispatch is Deferred (the client rate limiter
// refused it before anything reached the cluster) re-offers the task without
// spending the dispatch budget, as the synchronous path does (#1596).
func TestAsyncDeferredDispatchIsNotCounted(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	st := &fakeAsyncStore{attempts: dispatchMaxAttempts - 1, active: true}
	h := NewAsyncDispatchFailures(st, quietLogger())
	h.now = func() time.Time { return now }
	cause := fmt.Errorf("creating pod: client rate limiter Wait returned an error: rate: Wait(n=1) would exceed context deadline")
	if err := h.HandleDispatchFailure(context.Background(), "r1", "a", executor.Deferred, cause); err != nil {
		t.Fatal(err)
	}
	if len(st.failed) != 0 || len(st.requeued) != 1 {
		t.Fatalf("failed=%v requeued=%v, want one uncounted re-offer", st.failed, st.requeued)
	}
	if st.requeued[0].counted {
		t.Errorf("a deferred dispatch must not count toward the budget, got %+v", st.requeued[0])
	}
	if st.requeued[0].cleared {
		t.Errorf("a deferred dispatch never reached the API and must keep the outage window, got %+v", st.requeued[0])
	}
}
