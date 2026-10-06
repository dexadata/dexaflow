package scheduler

import (
	"context"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

// With undefined pools confined (server.pools_read_only), a task naming a pool
// that does not exist in its tenant draws on default_pool instead of running
// unlimited: tenants cannot create pools then, so an undefined name would
// otherwise be a way around the default_pool budget (#646).

func TestPlanRunUndefinedPoolIsUnlimitedByDefault(t *testing.T) {
	tasks := pooledTasks(4, "made-up")
	budgets := map[string]int{PoolKey(testTenant, defaultPoolName): 1}
	if got := countQueued(PlanRun(poolRun(tasks, budgets, nil))); got != 4 {
		t.Errorf("promoted %d, want 4 (undefined pools stay unlimited unless confined)", got)
	}
}

func TestPlanRunConfinedUndefinedPoolDrawsOnDefaultPool(t *testing.T) {
	tasks := pooledTasks(4, "made-up")
	budgets := map[string]int{PoolKey(testTenant, defaultPoolName): 1}
	run := poolRun(tasks, budgets, nil)
	run.ConfineUndefinedPools = true
	if got := countQueued(PlanRun(run)); got != 1 {
		t.Errorf("promoted %d, want 1 (the default_pool budget)", got)
	}
}

func TestPlanRunConfinedDefinedPoolKeepsItsOwnBudget(t *testing.T) {
	tasks := pooledTasks(4, "batch")
	budgets := map[string]int{PoolKey(testTenant, defaultPoolName): 1, PoolKey(testTenant, "batch"): 3}
	run := poolRun(tasks, budgets, nil)
	run.ConfineUndefinedPools = true
	if got := countQueued(PlanRun(run)); got != 3 {
		t.Errorf("promoted %d, want 3 (the batch pool's own budget)", got)
	}
}

// TestActivePoolCountsChargesConfinedTasksToDefaultPool: occupancy is counted
// under the pool admission charged, so a running task in an undefined pool
// occupies a default_pool slot when confined.
func TestActivePoolCountsChargesConfinedTasksToDefaultPool(t *testing.T) {
	tasks := pooledTasks(2, "made-up")
	runs := []RunState{{
		TenantID: testTenant, Tasks: tasks,
		States: map[string]domain.TaskState{tasks[0].TaskID: domain.TaskStateRunning, tasks[1].TaskID: domain.TaskStateQueued},
	}}
	budgets := map[string]int{PoolKey(testTenant, defaultPoolName): 4}
	if got := activePoolCounts(runs, budgets, true)[PoolKey(testTenant, defaultPoolName)]; got != 2 {
		t.Errorf("confined default_pool occupancy = %d, want 2", got)
	}
	if got := activePoolCounts(runs, budgets, false)[PoolKey(testTenant, "made-up")]; got != 2 {
		t.Errorf("unconfined made-up occupancy = %d, want 2", got)
	}
}

// TestStepChargesConfinedAdmissionsToDefaultPoolAcrossRuns: two runs in one
// tick each name an undefined pool. Admission is charged to default_pool for
// both the gate and the within-tick fold, so a 1-slot default_pool admits one
// task in total, not one per run.
func TestStepChargesConfinedAdmissionsToDefaultPoolAcrossRuns(t *testing.T) {
	task := []domain.TaskSpec{{TaskID: "a", Type: domain.TaskTypePython, Pool: "made-up"}}
	mk := func(id string) RunState {
		return RunState{
			RunID: id, DagID: "etl-" + id, TenantID: testTenant, State: domain.DagRunStateRunning,
			Tasks: task, States: scheduledStates(task),
			Tries: map[string]int{"a": 0}, MaxTries: map[string]int{"a": 1},
		}
	}
	store := newFakeStore(mk("r1"), mk("r2"))
	store.poolBudgets = map[string]int{PoolKey(testTenant, defaultPoolName): 1}
	d := &fakeDispatcher{}
	s := newScheduler(store)
	s.SetDispatcher(d)
	s.EnablePools()
	s.ConfineUndefinedPools()

	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(d.dispatched) != 1 {
		t.Errorf("dispatched %v, want exactly one task through the 1-slot default_pool", d.dispatched)
	}
}
