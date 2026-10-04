package storage

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// clearRetryBudget is the per-task retries a clear restores the budget from
// (#1131), as parallel arrays for the reset queries. Like Airflow, it reads the
// tasks of the version the re-run executes: the DAG's current version when the
// clear re-binds the run (run_on_latest_version), otherwise the version the run
// is pinned to. A run with no version yields empty arrays, and every task then
// keeps its budget, never below the attempt being made.
type clearRetryBudget struct {
	taskIDs []string
	retries []int32
}

// loadClearRetryBudget resolves the version the cleared run will execute and
// returns its tasks' retries.
func (r *Repository) loadClearRetryBudget(ctx context.Context, dag queries.Dag, run queries.DagRun, opts domain.ClearOptions) (clearRetryBudget, error) {
	versionID := run.DagVersionID
	if opts.ResetDagRun && opts.RunOnLatestVersion {
		versionID = dag.CurrentVersionID
	}
	if !versionID.Valid {
		return clearRetryBudget{}, nil
	}
	version, err := r.q.GetDagVersionByID(ctx, versionID)
	if err != nil {
		return clearRetryBudget{}, fmt.Errorf("loading the version a clear re-runs: %w", err)
	}
	var spec domain.DAGSpec
	if err := json.Unmarshal(version.Spec, &spec); err != nil {
		return clearRetryBudget{}, fmt.Errorf("decoding the version a clear re-runs: %w", err)
	}
	return retryBudgetOf(spec), nil
}

// retryBudgetOf lists every task's retries the way materialization counts them:
// the task's own value, else the DAG's default_args.retries, else none.
func retryBudgetOf(spec domain.DAGSpec) clearRetryBudget {
	b := clearRetryBudget{
		taskIDs: make([]string, 0, len(spec.Tasks)),
		retries: make([]int32, 0, len(spec.Tasks)),
	}
	for _, t := range spec.Tasks {
		retries := 0
		switch {
		case t.Retries != nil:
			retries = *t.Retries
		case spec.DefaultArgs != nil:
			retries = spec.DefaultArgs.Retries
		}
		b.taskIDs = append(b.taskIDs, t.TaskID)
		b.retries = append(b.retries, toInt32(retries))
	}
	return b
}
