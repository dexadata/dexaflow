package scheduler

import (
	"context"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

// Weighted pool slots (ADR 0066 §1): a task takes pool_slots slots of its pool,
// Airflow's semantics, so a pool budgets compute rather than task count.

// weighted returns n independent tasks in pool p, each taking slots slots.
func weighted(n int, p string, slots int) []domain.TaskSpec {
	tasks := pooledTasks(n, p)
	for i := range tasks {
		tasks[i].PoolSlots = slots
	}
	return tasks
}

// TestTaskSpecEffectivePoolSlotsDefaultsToOne: an unset (or non-positive)
// pool_slots weighs 1, so every DAG compiled before ADR 0066 plans as before.
func TestTaskSpecEffectivePoolSlotsDefaultsToOne(t *testing.T) {
	for in, want := range map[int]int{0: 1, -3: 1, 1: 1, 4: 4} {
		if got := (domain.TaskSpec{PoolSlots: in}).EffectivePoolSlots(); got != want {
			t.Errorf("EffectivePoolSlots(%d) = %d, want %d", in, got, want)
		}
	}
}

// TestPlanRunWeightedTasksFillThePoolBySlots: a 6-slot pool admits three
// 2-slot tasks, not six.
func TestPlanRunWeightedTasksFillThePoolBySlots(t *testing.T) {
	tasks := weighted(5, "p", 2)
	budgets := map[string]int{PoolKey(testTenant, "p"): 6}
	if got := countQueued(PlanRun(poolRun(tasks, budgets, nil))); got != 3 {
		t.Errorf("promoted %d, want 3 (6 slots / 2 each)", got)
	}
}

// TestPlanRunTaskThatDoesNotFitWaits: a 4-slot task in a 6-slot pool already
// holding 3 stays scheduled (nothing fails), while a 1-slot sibling that fits
// is admitted.
func TestPlanRunTaskThatDoesNotFitWaits(t *testing.T) {
	tasks := []domain.TaskSpec{
		{TaskID: "big", Type: domain.TaskTypePython, Pool: "p", PoolSlots: 4},
		{TaskID: "small", Type: domain.TaskTypePython, Pool: "p", PoolSlots: 1},
	}
	budgets := map[string]int{PoolKey(testTenant, "p"): 6}
	active := map[string]int{PoolKey(testTenant, "p"): 3}
	out := PlanRun(poolRun(tasks, budgets, active))
	if len(out) != 1 || out[0].TaskID != "small" || out[0].To != domain.TaskStateQueued {
		t.Fatalf("planned %+v, want only small -> queued", out)
	}
}

// TestPlanRunTaskExactlyFillingThePoolIsAdmitted: the gate is <=, so a task
// whose slots equal the free room is admitted.
func TestPlanRunTaskExactlyFillingThePoolIsAdmitted(t *testing.T) {
	tasks := weighted(1, "p", 4)
	budgets := map[string]int{PoolKey(testTenant, "p"): 6}
	active := map[string]int{PoolKey(testTenant, "p"): 2}
	if got := countQueued(PlanRun(poolRun(tasks, budgets, active))); got != 1 {
		t.Errorf("promoted %d, want 1 (2 + 4 == 6)", got)
	}
}

// TestPlanRunMaxActiveTasksStillCountsTasks: max_active_tasks is a fan-out
// cap, so it counts tasks whatever their weight.
func TestPlanRunMaxActiveTasksStillCountsTasks(t *testing.T) {
	tasks := weighted(4, "p", 3)
	run := poolRun(tasks, map[string]int{PoolKey(testTenant, "p"): 100}, nil)
	run.MaxActiveTasks = 2
	if got := countQueued(PlanRun(run)); got != 2 {
		t.Errorf("promoted %d, want 2 (max_active_tasks counts tasks)", got)
	}
}

// TestActivePoolCountsSumsSlots: occupancy is the sum of pool_slots of the
// pool's queued and running task instances, not their count.
func TestActivePoolCountsSumsSlots(t *testing.T) {
	tasks := []domain.TaskSpec{
		{TaskID: "a", Type: domain.TaskTypePython, Pool: "p", PoolSlots: 3},
		{TaskID: "b", Type: domain.TaskTypePython, Pool: "p"},
		{TaskID: "c", Type: domain.TaskTypePython, Pool: "p", PoolSlots: 5},
	}
	runs := []RunState{{
		TenantID: testTenant, Tasks: tasks,
		States: map[string]domain.TaskState{
			"a": domain.TaskStateRunning, "b": domain.TaskStateQueued, "c": domain.TaskStateScheduled,
		},
	}}
	budgets := map[string]int{PoolKey(testTenant, "p"): 10}
	if got := activePoolCounts(runs, budgets, false)[PoolKey(testTenant, "p")]; got != 4 {
		t.Errorf("occupancy = %d, want 4 (3 running + 1 queued; scheduled does not count)", got)
	}
}

// TestStepWeightedAdmissionsFoldAcrossRuns: two runs in one tick each want a
// 3-slot task in a shared 4-slot pool. The first run's admission is folded in
// by its weight, so the second does not fit and only one task is dispatched.
func TestStepWeightedAdmissionsFoldAcrossRuns(t *testing.T) {
	task := []domain.TaskSpec{{TaskID: "a", Type: domain.TaskTypePython, Pool: "shared", PoolSlots: 3}}
	mk := func(id string) RunState {
		return RunState{
			RunID: id, DagID: "etl-" + id, TenantID: testTenant, State: domain.DagRunStateRunning,
			Tasks: task, States: scheduledStates(task),
			Tries: map[string]int{"a": 0}, MaxTries: map[string]int{"a": 1},
		}
	}
	store := newFakeStore(mk("r1"), mk("r2"))
	store.poolBudgets = map[string]int{PoolKey(testTenant, "shared"): 4}
	d := &fakeDispatcher{}
	s := newScheduler(store)
	s.SetDispatcher(d)
	s.EnablePools()
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(d.dispatched) != 1 {
		t.Errorf("dispatched %d, want 1 (3 + 3 > 4 slots across runs)", len(d.dispatched))
	}
}
