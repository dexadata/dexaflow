package scheduler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
)

// diamondAfterRoot is a run whose root just succeeded and whose two children
// are still none: the moment a completion report lands.
func diamondAfterRoot() RunState {
	return RunState{
		RunID: "r1", DagID: "etl", State: domain.DagRunStateRunning,
		Tasks: []domain.TaskSpec{
			{TaskID: "root", Type: "python"},
			{TaskID: "b", Type: "python", DependsOn: []string{"root"}},
			{TaskID: "c", Type: "python", DependsOn: []string{"root"}},
		},
		States: map[string]domain.TaskState{
			"root": domain.TaskStateSuccess, "b": domain.TaskStateNone, "c": domain.TaskStateNone,
		},
	}
}

func countTransitions(ts []transition, taskID string, to domain.TaskState) int {
	n := 0
	for _, tr := range ts {
		if tr.taskID == taskID && tr.to == to {
			n++
		}
	}
	return n
}

// Off by default: a ready task is scheduled in one tick and queued in the next,
// exactly as before the gate existed.
func TestEagerPromotionOffKeepsTwoTickPromotion(t *testing.T) {
	store := newFakeStore(diamondAfterRoot())
	disp := &fakeDispatcher{}
	s := newScheduler(store)
	s.SetDispatcher(disp)
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(disp.dispatched) != 0 {
		t.Errorf("gate off: nothing may dispatch in the scheduling tick, got %v", disp.dispatched)
	}
	if !hasTransition(store.transitions, "b", domain.TaskStateScheduled) || hasTransition(store.transitions, "b", domain.TaskStateQueued) {
		t.Errorf("gate off: b should only be scheduled, got %v", store.transitions)
	}
}

func TestEagerPromotionQueuesReadyTasksInTheSameTick(t *testing.T) {
	store := newFakeStore(diamondAfterRoot())
	disp := &fakeDispatcher{}
	s := newScheduler(store)
	s.SetDispatcher(disp)
	s.EnableEagerPromotion()
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"b", "c"} {
		if !hasTransition(store.transitions, id, domain.TaskStateScheduled) || !hasTransition(store.transitions, id, domain.TaskStateQueued) {
			t.Errorf("%s should go none -> scheduled -> queued in one tick, got %v", id, store.transitions)
		}
	}
	if len(disp.dispatched) != 2 {
		t.Errorf("both children should dispatch once, got %v", disp.dispatched)
	}
}

// A skip cascades through a chain within the tick and the run finalizes in that
// same tick, instead of one level per tick.
func TestEagerPromotionCascadesSkipsAndFinalizes(t *testing.T) {
	run := RunState{
		RunID: "r1", DagID: "etl", State: domain.DagRunStateRunning,
		Tasks: []domain.TaskSpec{
			{TaskID: "a", Type: "python"},
			{TaskID: "b", Type: "python", DependsOn: []string{"a"}},
			{TaskID: "c", Type: "python", DependsOn: []string{"b"}},
		},
		States: map[string]domain.TaskState{
			"a": domain.TaskStateSkipped, "b": domain.TaskStateNone, "c": domain.TaskStateNone,
		},
	}
	store := newFakeStore(run)
	s := newScheduler(store)
	s.EnableEagerPromotion()
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !hasTransition(store.transitions, "b", domain.TaskStateSkipped) || !hasTransition(store.transitions, "c", domain.TaskStateSkipped) {
		t.Errorf("skip should cascade b and c in one tick, got %v", store.transitions)
	}
	if store.runStates["r1"] != domain.DagRunStateSuccess {
		t.Errorf("run should finalize in the same tick, got %q", store.runStates["r1"])
	}
	// The store's run snapshot must not be mutated by the in-memory fold.
	if run.States["b"] != domain.TaskStateNone {
		t.Errorf("fold wrote through to the store's state map: b = %s", run.States["b"])
	}
}

// The re-plan loop is bounded: a skip chain longer than the bound advances by at
// most the bound per tick and the tick still returns.
func TestEagerPromotionIsBounded(t *testing.T) {
	const depth = 3 * maxSameTickPasses
	run := RunState{RunID: "r1", DagID: "etl", State: domain.DagRunStateRunning, States: map[string]domain.TaskState{}}
	for i := range depth {
		task := domain.TaskSpec{TaskID: fmt.Sprintf("t%03d", i), Type: "python"}
		if i > 0 {
			task.DependsOn = []string{fmt.Sprintf("t%03d", i-1)}
			run.States[task.TaskID] = domain.TaskStateNone
		} else {
			run.States[task.TaskID] = domain.TaskStateSkipped
		}
		run.Tasks = append(run.Tasks, task)
	}
	store := newFakeStore(run)
	s := newScheduler(store)
	s.EnableEagerPromotion()
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	skipped := 0
	for _, tr := range store.transitions {
		if tr.to == domain.TaskStateSkipped {
			skipped++
		}
	}
	if skipped != 1+maxSameTickPasses {
		t.Errorf("one planning pass plus %d re-plans should skip %d tasks, got %d", maxSameTickPasses, 1+maxSameTickPasses, skipped)
	}
}

// Tasks promoted in a re-plan still count against max_active_tasks: the first
// pass spends the only slot, so the newly scheduled task stays parked.
func TestEagerPromotionHonorsMaxActiveTasks(t *testing.T) {
	run := diamondAfterRoot()
	run.States["b"] = domain.TaskStateScheduled
	run.MaxActiveTasks = 1
	store := newFakeStore(run)
	disp := &fakeDispatcher{}
	s := newScheduler(store)
	s.SetDispatcher(disp)
	s.EnableEagerPromotion()
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(disp.dispatched) != 1 || disp.dispatched[0] != "b" {
		t.Errorf("only b fits under max_active_tasks=1, dispatched %v", disp.dispatched)
	}
	if !hasTransition(store.transitions, "c", domain.TaskStateScheduled) || hasTransition(store.transitions, "c", domain.TaskStateQueued) {
		t.Errorf("c should be scheduled and parked, got %v", store.transitions)
	}
}

// Tasks promoted in a re-plan still count against their pool, folded on top of
// what the first pass admitted.
func TestEagerPromotionHonorsPools(t *testing.T) {
	run := diamondAfterRoot()
	run.TenantID = "t"
	run.States["b"] = domain.TaskStateScheduled
	store := newFakeStore(run)
	store.poolBudgets = map[string]int{PoolKey("t", defaultPoolName): 1}
	disp := &fakeDispatcher{}
	s := newScheduler(store)
	s.SetDispatcher(disp)
	s.EnablePools()
	s.EnableEagerPromotion()
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(disp.dispatched) != 1 || disp.dispatched[0] != "b" {
		t.Errorf("the default pool has one slot, dispatched %v", disp.dispatched)
	}
}

// A task whose dispatch fails in a re-plan is backed off once, not offered again
// by a later pass of the same tick, and a task already scheduled at the start of
// the tick is offered exactly once.
func TestEagerPromotionDoesNotRedispatchWithinATick(t *testing.T) {
	run := diamondAfterRoot()
	run.States["b"] = domain.TaskStateScheduled
	store := newFakeStore(run)
	disp := &fakeDispatcher{err: errors.New("apiserver down"), disp: executor.Rejected}
	s := newScheduler(store)
	s.SetDispatcher(disp)
	s.EnableEagerPromotion()
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	attempts := map[string]int{}
	for _, id := range disp.dispatched {
		attempts[id]++
	}
	if attempts["b"] != 1 || attempts["c"] != 1 {
		t.Errorf("each task should be offered once per tick, got %v", attempts)
	}
	if len(store.dispatchFailures) != 2 {
		t.Errorf("each failure should be backed off once, got %v", store.dispatchFailures)
	}
}

// The retry rail runs once per tick: a re-plan never repeats a retry decision.
func TestEagerPromotionDoesNotRepeatRetryDecisions(t *testing.T) {
	run := RunState{
		RunID: "r1", DagID: "etl", State: domain.DagRunStateRunning,
		Tasks: []domain.TaskSpec{
			{TaskID: "a", Type: "python"},
			{TaskID: "b", Type: "python"},
			{TaskID: "c", Type: "python", DependsOn: []string{"b"}},
		},
		States: map[string]domain.TaskState{
			"a": domain.TaskStateFailed, "b": domain.TaskStateSkipped, "c": domain.TaskStateNone,
		},
		Tries: map[string]int{"a": 1}, MaxTries: map[string]int{"a": 3},
	}
	store := newFakeStore(run)
	s := newScheduler(store)
	s.EnableEagerPromotion()
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := countTransitions(store.transitions, "a", domain.TaskStateUpForRetry); n != 1 {
		t.Errorf("a should move to up_for_retry once, got %d (%v)", n, store.transitions)
	}
	if len(store.retried) != 0 {
		t.Errorf("the retry reset belongs to a later tick, got %v", store.retried)
	}
}

// countingStore counts ActiveRuns calls safely across the Run goroutine.
type countingStore struct {
	*fakeStore
	ticks atomic.Int64
}

func (c *countingStore) ActiveRuns(context.Context) ([]RunState, error) {
	c.ticks.Add(1)
	return nil, nil
}

func runLoop(t *testing.T, s *Scheduler) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = s.Run(ctx)
	}()
	return func() {
		cancel()
		wg.Wait()
	}
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func pollUntil(t *testing.T, cond func() bool, within time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

func TestWakeTriggersAnEarlyTick(t *testing.T) {
	store := &countingStore{fakeStore: newFakeStore()}
	s := NewScheduler(store, discardLogger(), time.Hour)
	s.SetLeading(true)
	s.EnableEagerPromotion()
	stop := runLoop(t, s)
	defer stop()
	s.Wake()
	if !pollUntil(t, func() bool { return store.ticks.Load() == 1 }, 2*time.Second) {
		t.Fatalf("a wake should tick well before the 1h interval, ticks = %d", store.ticks.Load())
	}
}

func TestWakeIsIgnoredWhenTheGateIsOff(t *testing.T) {
	store := &countingStore{fakeStore: newFakeStore()}
	s := NewScheduler(store, discardLogger(), time.Hour)
	s.SetLeading(true)
	stop := runLoop(t, s)
	defer stop()
	s.Wake()
	if pollUntil(t, func() bool { return store.ticks.Load() > 0 }, 200*time.Millisecond) {
		t.Fatalf("gate off: a wake must not tick, ticks = %d", store.ticks.Load())
	}
}

// A burst of wakes coalesces: callers never block, and the loop runs a bounded
// number of ticks rather than one per wake.
func TestWakesCoalesce(t *testing.T) {
	store := &countingStore{fakeStore: newFakeStore()}
	s := NewScheduler(store, discardLogger(), time.Hour)
	s.SetLeading(true)
	s.EnableEagerPromotion()
	stop := runLoop(t, s)
	defer stop()
	start := time.Now()
	for range 1000 {
		s.Wake()
	}
	// The burst itself can be descheduled for longer than wakeMinGap on a busy
	// race-enabled runner, and the loop may then tick once per gap it spans. The
	// bound is what coalescing promises: one tick for the first wake, one for
	// the wakes pending behind it, and one more per gap the burst outlasted.
	allowed := 2 + int64(time.Since(start)/wakeMinGap)
	pollUntil(t, func() bool { return store.ticks.Load() > 0 }, 2*time.Second)
	time.Sleep(3 * wakeMinGap)
	if n := store.ticks.Load(); n < 1 || n > allowed {
		t.Errorf("1000 wakes in a burst should coalesce into at most %d ticks, got %d", allowed, n)
	}
}

// Leader-only: a woken follower ticks its heartbeat but never reads state.
func TestWakeOnAFollowerReadsNothing(t *testing.T) {
	store := &countingStore{fakeStore: newFakeStore()}
	s := NewScheduler(store, discardLogger(), time.Hour)
	s.EnableEagerPromotion()
	stop := runLoop(t, s)
	defer stop()
	s.Wake()
	if !pollUntil(t, func() bool { return s.lastTick.Load() != 0 }, 2*time.Second) {
		t.Fatal("the woken follower should still run its (empty) tick")
	}
	if n := store.ticks.Load(); n != 0 {
		t.Errorf("a follower must not read scheduler state, ActiveRuns calls = %d", n)
	}
}

// The spacing of woken ticks adapts to how long a tick takes: at least
// wakeMinGap, and at least twice the previous tick's duration, so wake driven
// ticks never hold the loop (and its database reads) for more than half the
// time however slow a tick gets. The loop interval caps it, since the
// interval tick fires by then anyway.
func TestWakeGapAdaptsToTickDuration(t *testing.T) {
	for _, tc := range []struct {
		tick, interval, want time.Duration
	}{
		{tick: 0, interval: time.Second, want: wakeMinGap},
		{tick: 10 * time.Millisecond, interval: time.Second, want: wakeMinGap},
		{tick: 300 * time.Millisecond, interval: time.Second, want: 600 * time.Millisecond},
		{tick: 800 * time.Millisecond, interval: time.Second, want: time.Second},
		{tick: 0, interval: 50 * time.Millisecond, want: 50 * time.Millisecond},
	} {
		if got := wakeGap(tc.tick, tc.interval); got != tc.want {
			t.Errorf("wakeGap(tick %s, interval %s) = %s, want %s", tc.tick, tc.interval, got, tc.want)
		}
	}
}

// wakeCountingRecorder counts woken ticks across the Run goroutine.
type wakeCountingRecorder struct {
	fakeRecorder
	woken atomic.Int64
}

func (r *wakeCountingRecorder) RecordSchedulerWokenTick() { r.woken.Add(1) }

// A tick started by Wake is counted, so operators can see how often
// completions pull ticks forward; interval ticks are not.
func TestWokenTicksAreCounted(t *testing.T) {
	store := &countingStore{fakeStore: newFakeStore()}
	s := NewScheduler(store, discardLogger(), time.Hour)
	rec := &wakeCountingRecorder{}
	s.SetRecorder(rec)
	s.SetLeading(true)
	s.EnableEagerPromotion()
	stop := runLoop(t, s)
	defer stop()
	s.Wake()
	if !pollUntil(t, func() bool { return rec.woken.Load() == 1 }, 2*time.Second) {
		t.Fatalf("woken ticks counted = %d, want 1", rec.woken.Load())
	}
	if n := store.ticks.Load(); n != 1 {
		t.Errorf("ticks = %d, want the one woken tick", n)
	}
}
