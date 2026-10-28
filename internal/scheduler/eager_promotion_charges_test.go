package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
)

// TestEagerPromotionDoesNotChargeAdmittedTasksTwice: with max_active_tasks=2
// and nothing active, a task already scheduled at tick start (a) and a root the
// first pass schedules (b) both fit. The re-plan must see a as queued, not as a
// scheduled candidate on top of its admission, or a spends the last slot a
// second time and b is parked until the next tick.
func TestEagerPromotionDoesNotChargeAdmittedTasksTwice(t *testing.T) {
	// Arrange
	store := newFakeStore(RunState{
		RunID: "r1", DagID: "etl", State: domain.DagRunStateRunning, MaxActiveTasks: 2,
		Tasks:  []domain.TaskSpec{{TaskID: "a", Type: "python"}, {TaskID: "b", Type: "python"}},
		States: map[string]domain.TaskState{"a": domain.TaskStateScheduled, "b": domain.TaskStateNone},
	})
	disp := &fakeDispatcher{}
	s := newScheduler(store)
	s.SetDispatcher(disp)
	s.EnableEagerPromotion()

	// Act
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Assert
	if !hasTransition(store.transitions, "b", domain.TaskStateQueued) {
		t.Errorf("b fits under max_active_tasks=2 next to a, but the re-plan parked it: %v", store.transitions)
	}
}

// TestEagerPromotionDoesNotChargePoolSlotsTwice is the pool form: a 2-slot
// task admitted by the first pass must not be offered again by the re-plan,
// where it would take its slots a second time and park a newly scheduled task
// that fits the pool.
func TestEagerPromotionDoesNotChargePoolSlotsTwice(t *testing.T) {
	// Arrange
	store := newFakeStore(RunState{
		RunID: "r1", DagID: "etl", TenantID: testTenant, State: domain.DagRunStateRunning,
		Tasks: []domain.TaskSpec{
			{TaskID: "a", Type: domain.TaskTypePython, Pool: "p", PoolSlots: 2},
			{TaskID: "b", Type: domain.TaskTypePython, Pool: "p", PoolSlots: 2},
		},
		States: map[string]domain.TaskState{"a": domain.TaskStateScheduled, "b": domain.TaskStateNone},
	})
	store.poolBudgets = map[string]int{PoolKey(testTenant, "p"): 4}
	disp := &fakeDispatcher{}
	s := newScheduler(store)
	s.SetDispatcher(disp)
	s.EnablePools()
	s.EnableEagerPromotion()

	// Act
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Assert
	if len(disp.dispatched) != 2 {
		t.Errorf("2 + 2 slots fit a pool of 4, dispatched %v", disp.dispatched)
	}
}

// TestEagerPromotionChargesPoolSlotsToSiblingRuns (ADR 0066): what a run admits
// in a re-plan is charged to its pool by the task's slots, not one per task,
// so a sibling run advanced later in the tick sees the pool as full.
func TestEagerPromotionChargesPoolSlotsToSiblingRuns(t *testing.T) {
	// Arrange
	first := RunState{
		RunID: "r1", DagID: "etl-r1", TenantID: testTenant, State: domain.DagRunStateRunning,
		Tasks: []domain.TaskSpec{
			{TaskID: "root", Type: domain.TaskTypePython},
			{TaskID: "big", Type: domain.TaskTypePython, Pool: "p", PoolSlots: 3, DependsOn: []string{"root"}},
		},
		States: map[string]domain.TaskState{"root": domain.TaskStateSuccess, "big": domain.TaskStateNone},
	}
	second := RunState{
		RunID: "r2", DagID: "etl-r2", TenantID: testTenant, State: domain.DagRunStateRunning,
		Tasks:  []domain.TaskSpec{{TaskID: "other", Type: domain.TaskTypePython, Pool: "p", PoolSlots: 2}},
		States: map[string]domain.TaskState{"other": domain.TaskStateScheduled},
	}
	store := newFakeStore(first, second)
	store.poolBudgets = map[string]int{PoolKey(testTenant, "p"): 4}
	disp := &fakeDispatcher{}
	s := newScheduler(store)
	s.SetDispatcher(disp)
	s.EnablePools()
	s.EnableEagerPromotion()

	// Act
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Assert
	if len(disp.dispatched) != 1 || disp.dispatched[0] != "big" {
		t.Errorf("3 + 2 slots exceed a pool of 4: only big may run, dispatched %v", disp.dispatched)
	}
}

// TestEagerPromotionReportsReplanPoolWaits (ADR 0066 §4): a task the re-plan
// schedules and its pool then holds is reported as waiting in this tick, so
// its starvation clock starts with the tick that scheduled it, as it would
// without the gate one tick later.
func TestEagerPromotionReportsReplanPoolWaits(t *testing.T) {
	// Arrange
	busy := RunState{
		RunID: "busy", DagID: "etl-busy", TenantID: testTenant, State: domain.DagRunStateRunning,
		Tasks:  []domain.TaskSpec{{TaskID: "x", Type: domain.TaskTypePython, Pool: "p", PoolSlots: 3}},
		States: map[string]domain.TaskState{"x": domain.TaskStateRunning},
	}
	run := RunState{
		RunID: "r1", DagID: "etl-r1", TenantID: testTenant, State: domain.DagRunStateRunning,
		Tasks: []domain.TaskSpec{
			{TaskID: "root", Type: domain.TaskTypePython},
			{TaskID: "big", Type: domain.TaskTypePython, Pool: "p", PoolSlots: 3, DependsOn: []string{"root"}},
		},
		States: map[string]domain.TaskState{"root": domain.TaskStateSuccess, "big": domain.TaskStateNone},
	}
	store := newFakeStore(busy, run)
	store.poolBudgets = map[string]int{PoolKey(testTenant, "p"): 4}
	s := newScheduler(store)
	s.SetDispatcher(&fakeDispatcher{})
	s.EnablePools()
	s.EnableEagerPromotion()
	s.SetPoolStarvationThreshold(time.Minute)

	// Act
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Assert
	if _, ok := s.poolWaitSince[taskRef{"r1", "big"}]; !ok {
		t.Errorf("big was scheduled and held by its pool this tick, but its wait was not recorded: %v", s.poolWaitSince)
	}
}
