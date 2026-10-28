package storage

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// clearRetryBudget is the per-task retries a clear restores the budget from
// (#1131), as parallel arrays for the reset queries. Like Airflow, it reads the
// tasks of the version the re-run executes: the DAG's current version when the
// clear moves the run to it (run_on_latest_version on a finished run, ADR
// 0086), otherwise the version the run is pinned to. A run with no version yields empty arrays, and every task then
// keeps its budget, never below the attempt being made.
//
// poolSlots, parallel to taskIDs, is each task's EffectivePoolSlots in that
// same version: a clear writes it to the task instance so PoolSlotUsage weighs
// the re-run with the size the admission gate charges it (#1499). pools, also
// parallel, is each task's declared pool ("" for the implicit default_pool):
// a clear re-binds it with the size, so the re-run is charged to the pool the
// version names (#1589).
//
// tasks is the version's tasks with default_args.retries filled in where a
// task has none, as materialization reads them: a clear that moves a run to
// the version creates the task instances of the tasks it adds from them (ADR
// 0086).
type clearRetryBudget struct {
	taskIDs   []string
	retries   []int32
	poolSlots []int32
	pools     []string
	tasks     []domain.TaskSpec
}

// resetToNone is the ResetTaskInstanceToNone of one task of the run with
// this budget.
func (b clearRetryBudget) resetToNone(run queries.DagRun, taskID string) queries.ResetTaskInstanceToNoneParams {
	return queries.ResetTaskInstanceToNoneParams{
		TenantID: run.TenantID, DagRunID: run.ID, TaskID: taskID,
		SpecTaskIds: b.taskIDs, SpecRetries: b.retries, SpecPoolSlots: b.poolSlots, SpecPools: b.pools,
	}
}

// loadClearRetryBudget reads the tasks of versionID, the version the cleared
// run will execute, through q, so the clear reads it inside its transaction.
// A run with no version yields an empty budget.
func loadClearRetryBudget(ctx context.Context, q *queries.Queries, tenantID, versionID pgtype.UUID) (clearRetryBudget, error) {
	if !versionID.Valid {
		return clearRetryBudget{}, nil
	}
	version, err := q.GetDagVersionInTenant(ctx, queries.GetDagVersionInTenantParams{ID: versionID, TenantID: tenantID})
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
// the task's own value, else the DAG's default_args.retries, else none. It
// lists each task's EffectivePoolSlots and declared pool alongside, and the
// tasks themselves with those retries filled in.
func retryBudgetOf(spec domain.DAGSpec) clearRetryBudget {
	b := clearRetryBudget{
		taskIDs:   make([]string, 0, len(spec.Tasks)),
		retries:   make([]int32, 0, len(spec.Tasks)),
		poolSlots: make([]int32, 0, len(spec.Tasks)),
		pools:     make([]string, 0, len(spec.Tasks)),
		tasks:     make([]domain.TaskSpec, 0, len(spec.Tasks)),
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
		b.poolSlots = append(b.poolSlots, toInt32(t.EffectivePoolSlots()))
		b.pools = append(b.pools, t.Pool)
		task := t
		task.Retries = &retries
		b.tasks = append(b.tasks, task)
	}
	return b
}
