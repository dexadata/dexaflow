package storage

import (
	"reflect"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

// TestRetryBudgetOf pins the retries a clear restores the budget from to the
// same precedence materialization uses: the task's own retries, then the DAG's
// default_args, then none.
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

	got := retryBudgetOf(spec)
	if want := []string{"own", "default", "explicit_zero"}; !reflect.DeepEqual(got.taskIDs, want) {
		t.Errorf("task ids = %v, want %v", got.taskIDs, want)
	}
	if want := []int32{2, 3, 0}; !reflect.DeepEqual(got.retries, want) {
		t.Errorf("retries = %v, want %v", got.retries, want)
	}

	spec.DefaultArgs = nil
	if got := retryBudgetOf(spec); got.retries[1] != 0 {
		t.Errorf("a task with no retries and no default_args has %d retries, want 0", got.retries[1])
	}
}
