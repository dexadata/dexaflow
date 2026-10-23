package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// ListDagRuns returns a page of runs for a DAG and the total count.
func (r *Repository) ListDagRuns(ctx context.Context, tenant, dagID string, limit, offset int) ([]domain.DagRun, int, error) {
	dag, err := r.resolveDag(ctx, tenant, dagID)
	if err != nil {
		return nil, 0, err
	}
	out, err := r.listDagRunsPage(ctx, dag, dagID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	total, err := r.q.CountDagRunsByDag(ctx, queries.CountDagRunsByDagParams{DagID: dag.ID, TenantID: dag.TenantID})
	if err != nil {
		return nil, 0, fmt.Errorf("counting dag runs: %w", err)
	}
	return out, int(total), nil
}

// ListDagRunsPage is ListDagRuns without the total: it skips the COUNT over
// every run of the DAG, for callers that never use it (the grid, the latest
// run header, the in-memory run scans).
func (r *Repository) ListDagRunsPage(ctx context.Context, tenant, dagID string, limit, offset int) ([]domain.DagRun, error) {
	dag, err := r.resolveDag(ctx, tenant, dagID)
	if err != nil {
		return nil, err
	}
	return r.listDagRunsPage(ctx, dag, dagID, limit, offset)
}

func (r *Repository) listDagRunsPage(ctx context.Context, dag queries.Dag, dagID string, limit, offset int) ([]domain.DagRun, error) {
	rows, err := r.q.ListDagRunsByDagWithVersion(ctx, queries.ListDagRunsByDagWithVersionParams{
		TenantID: dag.TenantID, DagID: dag.ID, Limit: toInt32(limit), Offset: toInt32(offset),
	})
	if err != nil {
		return nil, fmt.Errorf("listing dag runs: %w", err)
	}
	out := make([]domain.DagRun, 0, len(rows))
	for _, run := range rows {
		out = append(out, mapDagRunWithVersion(queries.GetDagRunWithVersionRow(run), dagID))
	}
	return out, nil
}

// ListDagRunsAfter returns up to limit of a DAG's runs strictly before the
// cursor, newest first, and the number of runs matching states (all runs when
// states is empty). It is the keyset form of ListDagRuns: the same order, but a
// deep page is an index range scan instead of a scan past every skipped row.
func (r *Repository) ListDagRunsAfter(ctx context.Context, tenant, dagID string, states []string, after domain.PageCursor, limit int) ([]domain.DagRun, int, error) {
	dag, err := r.resolveDag(ctx, tenant, dagID)
	if err != nil {
		return nil, 0, err
	}
	if states == nil {
		states = []string{}
	}
	rows, err := r.q.ListDagRunsByDagAfter(ctx, queries.ListDagRunsByDagAfterParams{
		TenantID: dag.TenantID, DagID: dag.ID, States: states, AfterLogicalDate: pgtype.Timestamptz{Time: after.At, Valid: true},
		AfterRunID: after.Key, RowLimit: toInt32(limit),
	})
	if err != nil {
		return nil, 0, fmt.Errorf("listing dag runs: %w", err)
	}
	var total int64
	if len(states) == 0 {
		total, err = r.q.CountDagRunsByDag(ctx, queries.CountDagRunsByDagParams{DagID: dag.ID, TenantID: dag.TenantID})
	} else {
		total, err = r.q.CountDagRunsByDagStates(ctx, queries.CountDagRunsByDagStatesParams{TenantID: dag.TenantID, DagID: dag.ID, States: states})
	}
	if err != nil {
		return nil, 0, fmt.Errorf("counting dag runs: %w", err)
	}
	out := make([]domain.DagRun, 0, len(rows))
	for _, run := range rows {
		out = append(out, mapDagRunWithVersion(queries.GetDagRunWithVersionRow(run), dagID))
	}
	return out, int(total), nil
}

// GetDagRun returns a single run by its run id.
func (r *Repository) GetDagRun(ctx context.Context, tenant, dagID, runID string) (domain.DagRun, error) {
	dag, err := r.resolveDag(ctx, tenant, dagID)
	if err != nil {
		return domain.DagRun{}, err
	}
	run, err := r.q.GetDagRunWithVersion(ctx, queries.GetDagRunWithVersionParams{TenantID: dag.TenantID, DagID: dag.ID, RunID: runID})
	if err != nil {
		return domain.DagRun{}, mapNotFound(err)
	}
	return mapDagRunWithVersion(run, dagID), nil
}

// DeleteDagRun removes one run (and, by cascade, its task instances and XCom).
// It returns domain.ErrNotFound when no run with that id exists for the DAG, so
// the API can answer 404 rather than a silent 204 for a bad id.
func (r *Repository) DeleteDagRun(ctx context.Context, tenant, dagID, runID string) error {
	dag, err := r.resolveDag(ctx, tenant, dagID)
	if err != nil {
		return err
	}
	n, err := r.q.DeleteDagRun(ctx, queries.DeleteDagRunParams{TenantID: dag.TenantID, DagID: dag.ID, RunID: runID})
	if err != nil {
		return fmt.Errorf("deleting dag run: %w", err)
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// CreateDagRun inserts a new run for a DAG at its current version. The
// per-DAG max_active_runs cap (#200) is enforced here for any caller that
// goes through the repository, manual triggers via the API, scripted
// backfills, and any future programmatic trigger path, so the contract
// is honored in one place. A cap of zero is treated as "unlimited" to
// match the scheduler path (see `Scheduler.hasHeadroom`). The check
// races with concurrent inserts, but the small overshoot window is
// bounded by the number of concurrent writers and lets us avoid an
// advisory lock on the hot path. The tenant's max_runs_per_day, when set, is
// charged exactly (createRunWithinDailyLimit).
func (r *Repository) CreateDagRun(ctx context.Context, tenant, dagID string, run domain.DagRun) (domain.DagRun, error) {
	dag, err := r.resolveDag(ctx, tenant, dagID)
	if err != nil {
		return domain.DagRun{}, err
	}
	if maxActive := int(dag.MaxActiveRuns); maxActive > 0 {
		active, countErr := r.q.CountActiveDagRunsByDagID(ctx, queries.CountActiveDagRunsByDagIDParams{TenantID: dag.TenantID, DagID: dag.ID})
		if countErr != nil {
			return domain.DagRun{}, fmt.Errorf("counting active runs: %w", countErr)
		}
		if int(active) >= maxActive {
			return domain.DagRun{}, domain.Safef(domain.ErrConflict, "dag %q is at max_active_runs cap of %d", dagID, maxActive)
		}
	}
	// Persist an explicit empty object when the run carries no conf (or an
	// explicit JSON null), so the column is never NULL and downstream params
	// readers see a valid object.
	conf := []byte(run.Conf)
	if len(conf) == 0 || string(conf) == "null" {
		conf = []byte("{}")
	}
	var created queries.DagRun
	err = createRunWithinDailyLimit(ctx, r.q, r.pool, dag.TenantID, runCreation{
		insert: func(q *queries.Queries) (bool, error) {
			var ierr error
			created, ierr = q.CreateDagRun(ctx, queries.CreateDagRunParams{
				TenantID:     dag.TenantID,
				DagID:        dag.ID,
				DagVersionID: dag.CurrentVersionID,
				RunID:        run.RunID,
				LogicalDate:  pgtype.Timestamptz{Time: run.LogicalDate, Valid: true},
				State:        queries.DagRunState(run.State),
				Trigger:      queries.DagRunTrigger(run.RunType),
				Note:         strPtr(run.Note),
				Conf:         conf,
			})
			if ierr != nil {
				return false, fmt.Errorf("creating dag run: %w", mapConflict(ierr))
			}
			return true, nil
		},
		exists: func(q *queries.Queries) (bool, error) {
			return q.DagRunExistsByDagID(ctx, queries.DagRunExistsByDagIDParams{TenantID: dag.TenantID, DagID: dag.DagID, RunID: run.RunID})
		},
		// The answer a duplicate run id gets from the insert (dag_runs_unique).
		existsErr: fmt.Errorf("creating dag run: %w", domain.ErrConflict),
	})
	if err != nil {
		return domain.DagRun{}, err
	}
	// The trigger's audit entry is written by the API handler, where the acting
	// user is known (so the Audit Log shows the owner).
	return mapDagRun(created, dagID), nil
}

// ListTaskInstanceAttempts returns every attempt for (run, task), oldest first,
// the current task_instances row UNIONed with all archived task_instance_history
// rows. The UI's /tries endpoint needs this to render one navigable tab per
// attempt; without history, a cleared task shows only the latest attempt and the
// user cannot inspect prior failures (Lima bug #241).
func (r *Repository) ListTaskInstanceAttempts(ctx context.Context, tenant, dagID, runID, taskID string) ([]domain.TaskInstance, error) {
	dag, err := r.resolveDag(ctx, tenant, dagID)
	if err != nil {
		return nil, err
	}
	run, err := r.q.GetDagRun(ctx, queries.GetDagRunParams{TenantID: dag.TenantID, DagID: dag.ID, RunID: runID})
	if err != nil {
		return nil, mapNotFound(err)
	}
	rows, err := r.q.ListTaskInstanceAttempts(ctx, queries.ListTaskInstanceAttemptsParams{
		TenantID: run.TenantID, DagRunID: run.ID, TaskID: taskID,
	})
	if err != nil {
		return nil, fmt.Errorf("listing task instance attempts: %w", err)
	}
	out := make([]domain.TaskInstance, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.TaskInstance{
			DagID:       dagID,
			RunID:       runID,
			TaskID:      taskID,
			MapIndex:    int(row.MapIndex),
			TryNumber:   int(row.TryNumber),
			MaxTries:    int(row.MaxTries),
			State:       domain.TaskState(row.State),
			Operator:    row.Operator,
			ScheduledAt: timePtr(row.ScheduledAt),
			QueuedAt:    timePtr(row.QueuedAt),
			StartedAt:   timePtr(row.StartedAt),
			EndedAt:     timePtr(row.EndedAt),
			Duration:    row.DurationSeconds,
			Hostname:    strOrEmpty(row.Hostname),
			Note:        strOrEmpty(row.Note),
			Pool:        poolName(row.Pool),
			PoolSlots:   int(row.PoolSlots),
			// The attempts view is where an operator opens a specific failed try, so
			// it must carry the same cause the run-level list does. The UNION already
			// selects error_message from both history and the live row.
			FailureReason: strOrEmpty(row.ErrorMessage),
		})
	}
	return out, nil
}

// ListTaskInstances returns the task instances of a run.
func (r *Repository) ListTaskInstances(ctx context.Context, tenant, dagID, runID string, _, _ int) ([]domain.TaskInstance, int, error) {
	dag, err := r.resolveDag(ctx, tenant, dagID)
	if err != nil {
		return nil, 0, err
	}
	run, err := r.q.GetDagRun(ctx, queries.GetDagRunParams{TenantID: dag.TenantID, DagID: dag.ID, RunID: runID})
	if err != nil {
		return nil, 0, mapNotFound(err)
	}
	rows, err := r.q.ListTaskInstancesByRun(ctx, queries.ListTaskInstancesByRunParams{TenantID: run.TenantID, DagRunID: run.ID})
	if err != nil {
		return nil, 0, fmt.Errorf("listing task instances: %w", err)
	}
	out := make([]domain.TaskInstance, 0, len(rows))
	for _, ti := range rows {
		out = append(out, mapTaskInstance(ti, dagID, runID))
	}
	return out, len(out), nil
}

// ClearTaskInstances resets tasks to none for re-run. When onlyFailed is true,
// only tasks currently in a failed-ish state (failed, upstream_failed,
// up_for_retry) are reset; with an empty taskIDs and onlyFailed, every failed
// task in the run is cleared. It returns the number of task instances actually
// reset.
//
// opts carries the two independent run-level decisions, whether to re-open the
// run, and which version the re-run executes (see domain.ClearOptions).
//
// A clear that asks for the current version (opts.RunOnLatestVersion with
// opts.ResetDagRun) moves the run to it only when the run has finished and
// none of its task instances is queued or running, decided inside the
// clear's transaction on the locked run (decideMove, ADR 0086). The move
// brings the run's task instances to that version's tasks
// (alignTaskInstances). Any other run keeps its version, and the clear
// applies on it; RunsKeepingVersion reports why. Either way the clear resets
// only tasks the version the re-run executes declares, so a task instance
// left removed by an earlier move stays removed.
//
// Each cleared task instance gets its retry budget back and starts without the
// XCom of the attempt it replaces (#1131). The whole clear is one transaction,
// and the stored XCom values are deleted before it commits: a backend failure
// rolls the clear back rather than re-queueing a task whose previous values are
// still readable.
func (r *Repository) ClearTaskInstances(ctx context.Context, tenant, dagID, runID string, taskIDs []string, onlyFailed bool, opts domain.ClearOptions) (int, error) {
	dag, err := r.resolveDag(ctx, tenant, dagID)
	if err != nil {
		return 0, err
	}
	run, err := r.q.GetDagRun(ctx, queries.GetDagRunParams{TenantID: dag.TenantID, DagID: dag.ID, RunID: runID})
	if err != nil {
		return 0, mapNotFound(err)
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("beginning clear tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // best-effort; the commit path returns the meaningful error
	qtx := r.q.WithTx(tx)
	cleared, err := r.clearInTx(ctx, qtx, dag, run, taskIDs, onlyFailed, opts)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("committing clear tx: %w", err)
	}
	return len(cleared), nil
}

// clearInTx is ClearTaskInstances on run, inside the clear's transaction q.
// It returns the ids of the task instances it reset.
func (r *Repository) clearInTx(ctx context.Context, q *queries.Queries, dag queries.Dag, run queries.DagRun, taskIDs []string, onlyFailed bool, opts domain.ClearOptions) ([]string, error) {
	run, tis, moves, err := decideMove(ctx, q, dag, run, opts)
	if err != nil {
		return nil, err
	}
	versionID := run.DagVersionID
	if moves {
		versionID = dag.CurrentVersionID
	}
	budget, err := loadClearRetryBudget(ctx, q, dag.TenantID, versionID)
	if err != nil {
		return nil, err
	}
	var restored []string
	if moves {
		if restored, err = alignTaskInstances(ctx, q, run, tis, budget); err != nil {
			return nil, err
		}
	}
	if versionID.Valid {
		// Only a task the version the re-run executes declares is reset,
		// whether or not the run moves: a task instance a past move left
		// removed, reset to none, would never be planned and the finished
		// run would never settle again (ADR 0086).
		named := len(taskIDs) > 0
		taskIDs = declaredOnly(taskIDs, budget, restored)
		if named && len(taskIDs) == 0 {
			// Every named task was dropped or already brought back: an
			// empty list would mean "every failed task" below.
			onlyFailed = false
		}
	}
	cleared, err := resetTaskInstances(ctx, q, run, taskIDs, onlyFailed, budget)
	if err != nil {
		return nil, err
	}
	cleared = append(cleared, restored...)
	if err := r.deleteClearedXCom(ctx, q, run, cleared); err != nil {
		return nil, err
	}
	if opts.ResetDagRun {
		if err := reopenClearedRun(ctx, q, dag, run, moves); err != nil {
			return nil, err
		}
	}
	return cleared, nil
}

// reopenClearedRun re-opens a cleared run so the scheduler looks at it again,
// re-binding it to the DAG's current version when moves is set.
func reopenClearedRun(ctx context.Context, q *queries.Queries, dag queries.Dag, run queries.DagRun, moves bool) error {
	if moves {
		// Re-bind the run to the DAG's current version so a clear after a
		// code/yaml fix re-runs against the newest image + config (ADR 0020).
		// In dev the current version is the last hot-reload; in prod, the last
		// deploy. When the version is unchanged this is equivalent to a plain
		// state reset.
		if err := q.ResetDagRunToVersion(ctx, queries.ResetDagRunToVersionParams{
			ID:           run.ID,
			DagVersionID: dag.CurrentVersionID,
			TenantID:     run.TenantID,
		}); err != nil {
			return fmt.Errorf("re-binding dag run to current version: %w", err)
		}
		return nil
	}
	// The run is re-opened but keeps its pinned version, so the re-run
	// executes the image that produced the original attempt.
	if err := q.ReopenDagRunKeepingVersion(ctx, queries.ReopenDagRunKeepingVersionParams{ID: run.ID, TenantID: run.TenantID}); err != nil {
		return fmt.Errorf("re-opening dag run: %w", err)
	}
	return nil
}

// deleteClearedXCom removes the XCom of the cleared task instances: their index
// rows inside the clear transaction, then their stored values. XCom carries no
// try number, so without this the new attempt's downstream reads the previous
// attempt's values for every key the new attempt does not overwrite (#1131).
// Airflow likewise never lets an attempt see another attempt's XCom.
func (r *Repository) deleteClearedXCom(ctx context.Context, q *queries.Queries, run queries.DagRun, taskIDs []string) error {
	if len(taskIDs) == 0 {
		return nil
	}
	keys, err := q.DeleteXComIndexForTasks(ctx, queries.DeleteXComIndexForTasksParams{
		TenantID: run.TenantID, DagRunID: run.ID, TaskIds: taskIDs,
	})
	if err != nil {
		return fmt.Errorf("deleting the cleared tasks' xcom index: %w", err)
	}
	if r.xcomValues == nil {
		return nil
	}
	for _, key := range keys {
		if err := r.xcomValues.Delete(ctx, key); err != nil {
			return fmt.Errorf("deleting the cleared tasks' xcom value %q: %w", key, err)
		}
	}
	return nil
}

// resetTaskInstances applies the clear semantics: a specific task list, or (with
// an empty list and onlyFailed) every failed task in the run. Each reset restores
// the task's retry budget from budget. It returns the ids of the task instances
// it reset.
func resetTaskInstances(ctx context.Context, q *queries.Queries, run queries.DagRun, taskIDs []string, onlyFailed bool, budget clearRetryBudget) ([]string, error) {
	if len(taskIDs) == 0 {
		if !onlyFailed {
			return nil, nil
		}
		ids, err := q.ResetAllFailedTaskInstances(ctx, queries.ResetAllFailedTaskInstancesParams{
			TenantID: run.TenantID, DagRunID: run.ID, SpecTaskIds: budget.taskIDs, SpecRetries: budget.retries, SpecPoolSlots: budget.poolSlots, SpecPools: budget.pools,
		})
		if err != nil {
			return nil, fmt.Errorf("clearing failed tasks: %w", err)
		}
		return ids, nil
	}
	cleared := make([]string, 0, len(taskIDs))
	for _, taskID := range taskIDs {
		if onlyFailed {
			n, err := q.ResetFailedTaskInstance(ctx, queries.ResetFailedTaskInstanceParams{
				TenantID: run.TenantID, DagRunID: run.ID, TaskID: taskID, SpecTaskIds: budget.taskIDs, SpecRetries: budget.retries, SpecPoolSlots: budget.poolSlots, SpecPools: budget.pools,
			})
			if err != nil {
				return nil, fmt.Errorf("clearing failed task %q: %w", taskID, err)
			}
			if n > 0 {
				cleared = append(cleared, taskID)
			}
			continue
		}
		if err := q.ResetTaskInstanceToNone(ctx, budget.resetToNone(run, taskID)); err != nil {
			return nil, fmt.Errorf("clearing task %q: %w", taskID, err)
		}
		cleared = append(cleared, taskID)
	}
	return cleared, nil
}

// SetDagRunState sets a DAG run's state directly, backing the UI's mark run
// success/failed actions. Terminal states stamp ended_at; re-opening to a
// non-terminal state clears it. started_at is preserved.
func (r *Repository) SetDagRunState(ctx context.Context, tenant, dagID, runID, state string) error {
	dag, err := r.resolveDag(ctx, tenant, dagID)
	if err != nil {
		return err
	}
	run, err := r.q.GetDagRun(ctx, queries.GetDagRunParams{TenantID: dag.TenantID, DagID: dag.ID, RunID: runID})
	if err != nil {
		return mapNotFound(err)
	}
	ended := pgtype.Timestamptz{}
	if domain.DagRunState(state).IsTerminal() {
		ended = pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	}
	// A run set back to queued waits for the scheduler to start it again, as
	// after a clear: its start is cleared so the next start stamps a new one.
	started := run.StartedAt
	if domain.DagRunState(state) == domain.DagRunStateQueued {
		started = pgtype.Timestamptz{}
	}
	if _, err := r.q.UpdateDagRunState(ctx, queries.UpdateDagRunStateParams{
		ID: run.ID, State: queries.DagRunState(state), StartedAt: started, EndedAt: ended, TenantID: run.TenantID,
	}); err != nil {
		return fmt.Errorf("setting dag run state: %w", err)
	}
	return nil
}

// SetTaskInstanceState sets a task instance's state directly, backing the UI's
// "mark success"/"mark failed" actions. It does not run the task. A user's
// state is a verdict, so it clears an infra failure kind and confirms the row:
// a reaped task marked failed is neither re-placed nor overridden by a late
// SUCCESS record (ADR 0052 amendment).
func (r *Repository) SetTaskInstanceState(ctx context.Context, tenant, dagID, runID, taskID, state string) error {
	dag, err := r.resolveDag(ctx, tenant, dagID)
	if err != nil {
		return err
	}
	run, err := r.q.GetDagRun(ctx, queries.GetDagRunParams{TenantID: dag.TenantID, DagID: dag.ID, RunID: runID})
	if err != nil {
		return mapNotFound(err)
	}
	if err := r.q.SetTaskInstanceStateByUser(ctx, queries.SetTaskInstanceStateByUserParams{
		State: queries.TaskState(state), TenantID: run.TenantID, DagRunID: run.ID, TaskID: taskID,
	}); err != nil {
		return fmt.Errorf("setting task %q state: %w", taskID, err)
	}
	return nil
}

// TaskInstancesForRuns returns the task instances of the given runs of a DAG in
// one query, ordered by run_id, task_id, try_number, for the grid summaries.
func (r *Repository) TaskInstancesForRuns(ctx context.Context, tenant, dagID string, runIDs []string) ([]domain.TaskInstance, error) {
	if len(runIDs) == 0 {
		return nil, nil
	}
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return nil, err
	}
	rows, err := r.q.TaskInstancesForDagRuns(ctx, queries.TaskInstancesForDagRunsParams{
		TenantID: tid, DagID: dagID, Column3: runIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("task instances for runs: %w", err)
	}
	out := make([]domain.TaskInstance, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.TaskInstance{
			DagID:     dagID,
			RunID:     row.RunID,
			TaskID:    row.TaskID,
			TryNumber: int(row.TryNumber),
			State:     domain.TaskState(row.State),
			StartedAt: timePtr(row.StartedAt),
			EndedAt:   timePtr(row.EndedAt),
		})
	}
	return out, nil
}
