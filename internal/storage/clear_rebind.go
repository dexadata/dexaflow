package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// RunsKeepingVersion lists the runs, among runIDs, that a clear with opts
// asks to move to the DAG's current version and that keep their own, each
// with the reason (ADR 0086). It writes nothing. A run that does not exist
// in the tenant is domain.ErrNotFound, so the API refuses an unknown run
// before it previews or clears any. The list is empty when the clear does not
// re-bind (no run_on_latest_version, or no reset_dag_runs) and for a run
// already on the current version.
//
// The answer is read before the clear. ClearTaskInstances decides again
// inside its transaction, so a run that finishes in between moves although
// it is listed here, and a run another clear re-opens in between keeps its
// version although it is not.
func (r *Repository) RunsKeepingVersion(ctx context.Context, tenant, dagID string, runIDs []string, opts domain.ClearOptions) ([]domain.ClearVersionKept, error) {
	dag, err := r.resolveDag(ctx, tenant, dagID)
	if err != nil {
		return nil, err
	}
	runs := make([]queries.DagRun, 0, len(runIDs))
	for _, runID := range runIDs {
		run, gerr := r.q.GetDagRun(ctx, queries.GetDagRunParams{TenantID: dag.TenantID, DagID: dag.ID, RunID: runID})
		if gerr != nil {
			return nil, mapNotFound(gerr)
		}
		runs = append(runs, run)
	}
	kept := make([]domain.ClearVersionKept, 0)
	if !rebinds(dag, opts) {
		return kept, nil
	}
	for _, run := range runs {
		if run.DagVersionID == dag.CurrentVersionID {
			continue
		}
		tis, lerr := r.q.ListTaskInstancesByRun(ctx, queries.ListTaskInstancesByRunParams{TenantID: run.TenantID, DagRunID: run.ID})
		if lerr != nil {
			return nil, fmt.Errorf("listing the task instances of a run a clear re-binds: %w", lerr)
		}
		if reason := keepVersionReason(run, tis); reason != "" {
			kept = append(kept, domain.ClearVersionKept{RunID: run.RunID, Reason: reason})
		}
	}
	return kept, nil
}

// decideMove decides, inside the clear's transaction q, whether the clear
// moves run to the DAG's current version (ADR 0086). Only a run that looked
// finished is locked and read again, and the lock itself holds only a run
// that is still finished: the decision is then taken on the locked row and
// its task instances, so two clears of the same finished run do not both
// move it. A run that is not finished, including one another clear re-opened
// after run was read, is never locked here: the orphan reaper share-locks a
// running run's task instances before it updates the run row, and a clear
// holding that row while it resets those task instances would form a cycle
// with it. Such a run keeps its version, read again inside the transaction
// when the lock found it no longer finished, since another clear may have
// moved it after run was read. It returns the run as read last, its task
// instances when the move was considered, and the decision.
func decideMove(ctx context.Context, q *queries.Queries, dag queries.Dag, run queries.DagRun, opts domain.ClearOptions) (queries.DagRun, []queries.TaskInstance, bool, error) {
	if !rebinds(dag, opts) || !finished(run) {
		return run, nil, false, nil
	}
	locked, err := q.GetDagRunForUpdate(ctx, queries.GetDagRunForUpdateParams{ID: run.ID, TenantID: run.TenantID})
	if errors.Is(err, pgx.ErrNoRows) {
		// No longer finished (or gone): the run keeps its version, the one
		// it has now. Another clear may have moved it since run was read,
		// so it is read again here: the clear applies on the version the
		// re-run executes, not on the one the stale read names (#2046).
		current, gerr := q.GetDagRun(ctx, queries.GetDagRunParams{TenantID: run.TenantID, DagID: run.DagID, RunID: run.RunID})
		if gerr != nil {
			return run, nil, false, fmt.Errorf("reading the cleared run again: %w", mapNotFound(gerr))
		}
		return current, nil, false, nil
	}
	if err != nil {
		return run, nil, false, fmt.Errorf("locking the cleared run: %w", err)
	}
	tis, err := q.ListTaskInstancesByRun(ctx, queries.ListTaskInstancesByRunParams{TenantID: locked.TenantID, DagRunID: locked.ID})
	if err != nil {
		return locked, nil, false, fmt.Errorf("listing the task instances of the cleared run: %w", err)
	}
	return locked, tis, keepVersionReason(locked, tis) == "", nil
}

// finished reports whether the run is success or failed.
func finished(run queries.DagRun) bool {
	return run.State == queries.DagRunStateSuccess || run.State == queries.DagRunStateFailed
}

// rebinds reports whether a clear with opts asks to move its run to the DAG's
// current version.
func rebinds(dag queries.Dag, opts domain.ClearOptions) bool {
	return opts.ResetDagRun && opts.RunOnLatestVersion && dag.CurrentVersionID.Valid
}

// keepVersionReason says why a clear that asks for the current version keeps
// the run on its own, or "" when the run may move (ADR 0086). As in Airflow
// 3.2, only a finished run moves. A finished run that still has a queued or
// running task instance (one marked failed or success while it ran) keeps
// its version too, so a live attempt never reads the task spec of a version
// its image was not built for.
func keepVersionReason(run queries.DagRun, tis []queries.TaskInstance) string {
	if !finished(run) {
		return fmt.Sprintf("the run is %s: only a finished run moves to the current version", run.State)
	}
	for _, ti := range tis {
		if ti.State == queries.TaskStateQueued || ti.State == queries.TaskStateRunning {
			return fmt.Sprintf("task %q is %s: a run moves to the current version only when none of its tasks is queued or running",
				ti.TaskID, ti.State)
		}
	}
	return ""
}

// alignTaskInstances brings the task instances of a run that a clear moves to
// another version to that version's tasks, as Airflow's verify_integrity
// does after the re-bind (ADR 0086). budget is the version's tasks:
//
//   - a task without a task instance gets one, as materialization creates it
//     (state none, try 1, the version's retries, pool and size);
//   - a task instance whose task the version does not declare is marked
//     removed;
//   - a removed task instance whose task the version declares again is reset
//     to none as a new try, the reset a clear applies.
//
// It returns the task ids it reset, so the clear deletes their XCom and does
// not reset them a second time.
func alignTaskInstances(ctx context.Context, q *queries.Queries, run queries.DagRun, tis []queries.TaskInstance, budget clearRetryBudget) ([]string, error) {
	declared := make(map[string]bool, len(budget.tasks))
	for _, t := range budget.tasks {
		declared[t.TaskID] = true
	}
	have := make(map[string]bool, len(tis))
	var dropped, restored []string
	for _, ti := range tis {
		have[ti.TaskID] = true
		switch {
		case !declared[ti.TaskID] && ti.State != queries.TaskStateRemoved:
			dropped = append(dropped, ti.TaskID)
		case declared[ti.TaskID] && ti.State == queries.TaskStateRemoved:
			restored = append(restored, ti.TaskID)
		}
	}
	if len(dropped) > 0 {
		if err := q.MarkTaskInstancesRemoved(ctx, queries.MarkTaskInstancesRemovedParams{
			TenantID: run.TenantID, DagRunID: run.ID, TaskIds: dropped,
		}); err != nil {
			return nil, fmt.Errorf("marking the dropped tasks removed: %w", err)
		}
	}
	for _, taskID := range restored {
		if err := q.ResetTaskInstanceToNone(ctx, budget.resetToNone(run, taskID)); err != nil {
			return nil, fmt.Errorf("bringing back removed task %q: %w", taskID, err)
		}
	}
	added := make([]domain.TaskSpec, 0)
	for _, t := range budget.tasks {
		if !have[t.TaskID] {
			added = append(added, t)
		}
	}
	if len(added) > 0 {
		if _, err := q.CreateTaskInstances(ctx, taskInstanceRows(run.TenantID, run.ID, added)); err != nil {
			return nil, fmt.Errorf("creating the added tasks' task instances: %w", err)
		}
	}
	return restored, nil
}

// declaredOnly keeps the task ids the version of budget declares, in order:
// a clear resets nothing for a task the version the re-run executes does not
// declare, such as one whose task instance alignTaskInstances marked removed
// in this clear or an earlier one. skip names task ids already reset.
func declaredOnly(taskIDs []string, budget clearRetryBudget, skip []string) []string {
	declared := make(map[string]bool, len(budget.tasks))
	for _, t := range budget.tasks {
		declared[t.TaskID] = true
	}
	for _, id := range skip {
		delete(declared, id)
	}
	out := make([]string, 0, len(taskIDs))
	for _, id := range taskIDs {
		if declared[id] {
			out = append(out, id)
		}
	}
	return out
}
