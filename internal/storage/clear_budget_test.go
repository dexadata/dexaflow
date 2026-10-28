package storage

import (
	"reflect"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

// TestRetryBudgetOf pins the retries a clear restores the budget from to the
// same precedence materialization uses: the task's own retries, then the DAG's
// default_args, then none. It also pins the pool_slots a clear writes.
func TestRetryBudgetOf(t *testing.T) {
	two, zero := 2, 0
	spec := domain.DAGSpec{
		DefaultArgs: &domain.DefaultArgs{Retries: 3},
		Tasks: []domain.TaskSpec{
			{TaskID: "own"},
			{TaskID: "default"},
			{TaskID: "explicit_zero"},
		},
	}
	spec.Tasks[0].Retries = &two
	spec.Tasks[2].Retries = &zero
	spec.Tasks[0].PoolSlots = 4
	spec.Tasks[1].Pool = "gpu"

	got := retryBudgetOf(spec)
	if want := []string{"own", "default", "explicit_zero"}; !reflect.DeepEqual(got.taskIDs, want) {
		t.Errorf("task ids = %v, want %v", got.taskIDs, want)
	}
	if want := []int32{2, 3, 0}; !reflect.DeepEqual(got.retries, want) {
		t.Errorf("retries = %v, want %v", got.retries, want)
	}

	// pool_slots follow EffectivePoolSlots, the size the admission gate charges
	// (#1499): an unset size is one slot.
	if want := []int32{4, 1, 1}; !reflect.DeepEqual(got.poolSlots, want) {
		t.Errorf("pool slots = %v, want %v", got.poolSlots, want)
	}

	// pools follow the task's declared pool, empty for the implicit
	// default_pool, so a clear re-binds the pool with the size (#1589).
	if want := []string{"", "gpu", ""}; !reflect.DeepEqual(got.pools, want) {
		t.Errorf("pools = %v, want %v", got.pools, want)
	}

	spec.DefaultArgs = nil
	if got := retryBudgetOf(spec); got.retries[1] != 0 {
		t.Errorf("a task with no retries and no default_args has %d retries, want 0", got.retries[1])
	}
}

// TestRetryBudgetOfCarriesTheTasksAsMaterialized: the tasks a clear creates
// when it moves a run to a version that adds them (ADR 0086) carry the same
// retries the budget lists, default_args included, so the added task gets
// the max_tries a materialization of that version would give it.
func TestRetryBudgetOfCarriesTheTasksAsMaterialized(t *testing.T) {
	own := 1
	spec := domain.DAGSpec{
		DefaultArgs: &domain.DefaultArgs{Retries: 3},
		Tasks:       []domain.TaskSpec{{TaskID: "own", Retries: &own}, {TaskID: "default", Pool: "gpu"}},
	}
	got := retryBudgetOf(spec)
	if len(got.tasks) != 2 {
		t.Fatalf("tasks = %v, want both", got.tasks)
	}
	for i, want := range []int{1, 3} {
		if r := got.tasks[i].Retries; r == nil || *r != want {
			t.Errorf("task %s retries = %v, want %d", got.tasks[i].TaskID, r, want)
		}
	}
	if got.tasks[1].Pool != "gpu" {
		t.Errorf("task pool = %q, want gpu", got.tasks[1].Pool)
	}
	if spec.Tasks[1].Retries != nil {
		t.Error("retryBudgetOf wrote the default into the caller's spec")
	}
}
