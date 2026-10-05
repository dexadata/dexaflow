package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
	"github.com/dexadata/dexaflow/internal/scheduler"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// SchedulerStore is the sqlc-backed implementation of scheduler.Store.
type SchedulerStore struct {
	q     *queries.Queries
	pool  poolBeginner
	specs *specCache
	// warmExec is the operator's warm-pool config, consulted by ActiveWarmTargets
	// to resolve each active dag_version's effective warm target (ADR 0058 N1b2b).
	// Zero value = warm pools off, so ActiveWarmTargets reports every target as 0.
	warmExec config.ExecutionSection
	// provisionalInfraMarks makes the reapers' infra marks provisional
	// (infra_confirmed_at NULL) for the reconciler to confirm (ADR 0052
	// amendment, part 2). Set only where a reconciler runs (Kubernetes); false
	// (Lite) confirms each mark at mark time, which behaves exactly as before.
	provisionalInfraMarks bool
}

// poolBeginner is the slice of pgxpool.Pool the store uses to start the orphan
// reap transaction. Kept as a tiny interface so tests can fake the pool without
// pulling pgxpool into scheduler_store_test.go.
type poolBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// NewSchedulerStore builds a SchedulerStore over the given Postgres connection.
func NewSchedulerStore(pg *Postgres) *SchedulerStore {
	return &SchedulerStore{q: pg.Queries, pool: pg.Pool, specs: sharedSpecCache(pg)}
}

// ActiveRuns returns every queued/running run with its topology and task states.
// taskMaps holds the per-task state a run's task-instance rows contribute to a
// scheduler.RunState. Extracted from ActiveRuns to keep it under the complexity
// bound; the planner reads these maps.
type taskMaps struct {
	states           map[string]domain.TaskState
	tries            map[string]int
	maxTries         map[string]int
	endedAt          map[string]*time.Time
	rescheduleAt     map[string]*time.Time
	dispatchAttempts map[string]int
	nextDispatchAt   map[string]*time.Time
	infraFailed      map[string]bool
	infraProvisional map[string]bool
	infraAttempts    map[string]int
}

// taskInstanceMaps projects a run's task-instance rows into the per-task maps the
// planner consumes.
func taskInstanceMaps(tis []queries.TaskInstance) taskMaps {
	n := len(tis)
	m := taskMaps{
		states:           make(map[string]domain.TaskState, n),
		tries:            make(map[string]int, n),
		maxTries:         make(map[string]int, n),
		endedAt:          make(map[string]*time.Time, n),
		rescheduleAt:     make(map[string]*time.Time, n),
		dispatchAttempts: make(map[string]int, n),
		nextDispatchAt:   make(map[string]*time.Time, n),
		infraFailed:      make(map[string]bool, n),
		infraProvisional: make(map[string]bool, n),
		infraAttempts:    make(map[string]int, n),
	}
	for _, ti := range tis {
		m.states[ti.TaskID] = domain.TaskState(ti.State)
		m.tries[ti.TaskID] = int(ti.TryNumber)
		m.maxTries[ti.TaskID] = int(ti.MaxTries)
		if ti.EndedAt.Valid {
			t := ti.EndedAt.Time
			m.endedAt[ti.TaskID] = &t
		}
		// reschedule_at gates the up_for_reschedule → none re-dispatch (#380).
		if ti.RescheduleAt.Valid {
			t := ti.RescheduleAt.Time
			m.rescheduleAt[ti.TaskID] = &t
		}
		// dispatch_attempts / next_dispatch_at gate scheduled → queued re-dispatch
		// after a synchronous dispatch failure (ADR 0031 Amendment A).
		if ti.DispatchAttempts > 0 {
			m.dispatchAttempts[ti.TaskID] = int(ti.DispatchAttempts)
		}
		if ti.NextDispatchAt.Valid {
			t := ti.NextDispatchAt.Time
			m.nextDispatchAt[ti.TaskID] = &t
		}
		// An infra-caused terminal failure (agent/pod/dispatch lost) re-places off
		// the retry budget (ADR 0051 Phase 1). Guard on state=failed so a stale kind
		// on any other state is inert — the load-time half of the invariant the
		// reset queries enforce by clearing last_failure_kind.
		if ti.LastFailureKind != nil && *ti.LastFailureKind == "infra" &&
			domain.TaskState(ti.State) == domain.TaskStateFailed {
			m.infraFailed[ti.TaskID] = true
			// Not yet confirmed by the reconciler (ADR 0052 amendment, part 2):
			// the planner holds it as active until confirmation or the valve.
			if !ti.InfraConfirmedAt.Valid {
				m.infraProvisional[ti.TaskID] = true
			}
		}
		if ti.InfraAttempts > 0 {
			m.infraAttempts[ti.TaskID] = int(ti.InfraAttempts)
		}
	}
	return m
}

// ActiveRuns loads every active dag run and projects it into the scheduler's
// RunState (topology + per-task state), the read side of a scheduler tick.
func (s *SchedulerStore) ActiveRuns(ctx context.Context) ([]scheduler.RunState, error) {
	runs, err := s.q.ListActiveDagRuns(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing active runs: %w", err)
	}
	tisByRun, err := s.taskInstancesByRun(ctx, runs)
	if err != nil {
		return nil, err
	}
	out := make([]scheduler.RunState, 0, len(runs))
	s.specs.beginTick()
	for _, run := range runs {
		// The spec is immutable per dag_version_id (see specCache), so N active
		// runs sharing a version decode it once, not N times. The cached spec is
		// shared read-only: copy Tasks before applyDefaultRetries so filling a
		// run's retry defaults never writes through the shared backing array.
		// getWithGraph, like getForTick, keeps every version this tick reads
		// cached through the next tick, so more active versions than the cache
		// bound never thrash.
		cached, graph, err := s.specs.getWithGraph(ctx, s.q, run.DagVersionID)
		if err != nil {
			return nil, err
		}
		spec := cached
		spec.Tasks = make([]domain.TaskSpec, len(cached.Tasks))
		copy(spec.Tasks, cached.Tasks)
		applyDefaultRetries(&spec)
		ts := taskInstanceMaps(tisByRun[run.ID])
		// Build per-task retry_delay_seconds from the DAG spec so the planner
		// can gate `up_for_retry → none` on the user-declared cooldown (#201).
		// TaskSpec.RetryDelaySeconds is *int (omitempty); nil = no cooldown.
		retryDelay := make(map[string]int, len(spec.Tasks))
		for _, t := range spec.Tasks {
			if t.RetryDelaySeconds != nil {
				retryDelay[t.TaskID] = *t.RetryDelaySeconds
			}
		}
		out = append(out, scheduler.RunState{
			RunID:             uuidToString(run.ID),
			DisplayRunID:      run.RunID,
			LogicalDate:       rfc3339OrEmpty(run.LogicalDate),
			DagID:             spec.DagID,
			DagVersionID:      uuidToString(run.DagVersionID),
			TenantID:          uuidToString(run.TenantID),
			State:             domain.DagRunState(run.State),
			Tasks:             spec.Tasks,
			Graph:             graph,
			States:            ts.states,
			Tries:             ts.tries,
			MaxTries:          ts.maxTries,
			EndedAt:           ts.endedAt,
			RetryDelaySeconds: retryDelay,
			RescheduleAt:      ts.rescheduleAt,
			NextDispatchAt:    ts.nextDispatchAt,
			DispatchAttempts:  ts.dispatchAttempts,
			InfraFailed:       ts.infraFailed,
			InfraProvisional:  ts.infraProvisional,
			InfraAttempts:     ts.infraAttempts,
			Now:               time.Now(),
			Alerts:            spec.Alerts,
			MaxActiveTasks:    spec.MaxActiveTasks,
		})
	}
	return out, nil
}

// taskInstancesByRun loads the task instances of every given run in one query
// and groups them by run. One round trip per tick replaces the one-per-run reads
// that made the tick's database cost grow with the number of active runs. The
// query orders by (dag_run_id, task_id), so each run's slice keeps the task_id
// order the per-run query returned. A run with no rows (not yet materialized)
// is simply absent, which taskInstanceMaps reads as an empty run, as before.
func (s *SchedulerStore) taskInstancesByRun(ctx context.Context, runs []queries.DagRun) (map[pgtype.UUID][]queries.TaskInstance, error) {
	if len(runs) == 0 {
		return map[pgtype.UUID][]queries.TaskInstance{}, nil
	}
	ids := make([]pgtype.UUID, len(runs))
	for i, run := range runs {
		ids[i] = run.ID
	}
	tis, err := s.q.ListTaskInstancesByRuns(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("listing task instances: %w", err)
	}
	byRun := make(map[pgtype.UUID][]queries.TaskInstance, len(runs))
	for start := 0; start < len(tis); {
		end := start + 1
		for end < len(tis) && tis[end].DagRunID == tis[start].DagRunID {
			end++
		}
		byRun[tis[start].DagRunID] = tis[start:end:end]
		start = end
	}
	return byRun, nil
}

// SetWarmExecution records the operator's warm-pool config so ActiveWarmTargets
// can resolve each active dag_version's effective warm target. main.go calls it
// only when warm pools are enabled; left unset, warm pools read as off (every
// target 0).
func (s *SchedulerStore) SetWarmExecution(exec config.ExecutionSection) { s.warmExec = exec }

// SetProvisionalInfraMarks makes every reaper infra mark provisional until the
// pod reconciler confirms it (ADR 0052 amendment, part 2). main.go calls it
// only when the Kubernetes reconciler is wired with this store as its
// confirmer; without a confirmer a provisional mark would wait for the
// liveness valve on every reap.
func (s *SchedulerStore) SetProvisionalInfraMarks(on bool) { s.provisionalInfraMarks = on }

// ListProvisionalInfraFailures returns the provisional infra marks (failed,
// infra, not yet confirmed) of queued or running runs, oldest first and
// bounded, for the reconciler's confirmation pass.
func (s *SchedulerStore) ListProvisionalInfraFailures(ctx context.Context) ([]executor.ProvisionalInfraFailure, error) {
	rows, err := s.q.ListProvisionalInfraFailures(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing provisional infra failures: %w", err)
	}
	out := make([]executor.ProvisionalInfraFailure, 0, len(rows))
	for _, r := range rows {
		out = append(out, executor.ProvisionalInfraFailure{
			TaskInstanceID: uuidToString(r.TaskInstanceID),
			DagRunID:       uuidToString(r.DagRunID),
			TaskID:         r.TaskID,
			TryNumber:      int(r.TryNumber),
			AttemptEpoch:   int(r.AttemptEpoch),
		})
	}
	return out, nil
}

// ConfirmInfraFailure stamps a provisional infra mark confirmed, guarded on
// the exact attempt and on the mark still being provisional. It reports
// whether a row was confirmed.
func (s *SchedulerStore) ConfirmInfraFailure(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int) (bool, error) {
	tid, err := parseUUID(taskInstanceID)
	if err != nil {
		return false, err
	}
	n, err := s.q.ConfirmInfraFailure(ctx, queries.ConfirmInfraFailureParams{
		ID: tid, TryNumber: toInt32(tryNumber), AttemptEpoch: toInt32(attemptEpoch),
	})
	if err != nil {
		return false, fmt.Errorf("confirming infra failure: %w", err)
	}
	return n == 1, nil
}

// activeWarmVersion is one active dag_version's warm-relevant spec fields — the
// pure input to warmTargets, extracted so the projection is unit-testable without
// a database.
type activeWarmVersion struct {
	dagVersionID string
	image        string
	dagMinIdle   int
	// tenantID owns this dag_version; threaded onto the WarmTarget so the reconciler
	// can enforce the per-tenant aggregate warm-pod cap (M4). Many active runs of a
	// version share one tenant, so it dedupes with the version.
	tenantID string
	// staging marks a version whose DAG opted into the per-run shared /staging
	// volume (ADR 0022). Such versions are excluded from warm pools (ADR 0058 D5):
	// a warm worker carries no per-run /staging mount, so its attempts must run on
	// dedicated pods. Kept on the pure input so the exclusion is unit-testable.
	staging bool
}

// warmTargets dedupes the active versions (many runs share one immutable
// dag_version) and resolves each to its effective warm target through the
// operator's clamp/fallback/flag gate (config.ExecutionSection.EffectiveMinIdle).
// The result is the executor.WarmTargetSource contract: one entry per distinct
// active dag_version.
func warmTargets(versions []activeWarmVersion, exec config.ExecutionSection) []executor.WarmTarget {
	seen := make(map[string]bool, len(versions))
	out := make([]executor.WarmTarget, 0, len(versions))
	for _, v := range versions {
		if seen[v.dagVersionID] {
			continue
		}
		seen[v.dagVersionID] = true
		if v.staging {
			// ADR 0058 D5: staging versions never get a warm pool.
			continue
		}
		out = append(out, executor.WarmTarget{
			DagVersionID:     v.dagVersionID,
			Image:            v.image,
			EffectiveMinIdle: exec.EffectiveMinIdle(v.dagMinIdle),
			// MaxPoolSize is the operator's total ceiling of warm workers per
			// dag_version (model A2, ADR 0058 N1d-b). The reconciler grows the pool
			// past min_idle under load up to this cap and never beyond it.
			MaxPoolSize: exec.MaxPoolSize,
			// TenantID owns this dag_version; the reconciler sums a tenant's idle
			// floors and rations its aggregate budget across versions (M4).
			TenantID: v.tenantID,
		})
	}
	return out
}

// ActiveWarmTargets returns one warm target per distinct active dag_version with
// its effective warm-worker count (ADR 0058 N1b2b), the read side of a warm-pool
// reconcile tick. It reuses the same active-runs + cached-spec path as ActiveRuns:
// the spec is immutable per dag_version_id, so N active runs sharing a version
// decode it once and the effective target is derived from the DAG author's
// min_idle_workers under the operator's clamp/fallback. It implements
// executor.WarmTargetSource without the executor importing storage.
func (s *SchedulerStore) ActiveWarmTargets(ctx context.Context) ([]executor.WarmTarget, error) {
	runs, err := s.q.ListActiveDagRuns(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing active runs: %w", err)
	}
	versions := make([]activeWarmVersion, 0, len(runs))
	seen := make(map[pgtype.UUID]bool, len(runs))
	for _, run := range runs {
		if seen[run.DagVersionID] {
			continue
		}
		seen[run.DagVersionID] = true
		_, spec, err := s.specs.getForTick(ctx, s.q, run.DagVersionID)
		if err != nil {
			return nil, err
		}
		versions = append(versions, activeWarmVersion{
			dagVersionID: uuidToString(run.DagVersionID),
			image:        spec.Image,
			dagMinIdle:   spec.MinIdleWorkers,
			tenantID:     uuidToString(run.TenantID),
			// ADR 0058 D5: a staging version is excluded from warm pools by
			// warmTargets. Defense-in-depth alongside the dispatch-path guard.
			staging: spec.Staging != nil && spec.Staging.Enabled,
		})
	}
	return warmTargets(versions, s.warmExec), nil
}

// MaterializeTasks creates a none-state task instance for each task in the run,
// in one batched COPY rather than T INSERT round-trips. The rows are identical
// to the per-task loop this replaced: try_number pinned to 1, state none, and
// max_tries derived from the task's retries (default 1). An empty task set is a
// no-op (a DAG with no tasks materializes nothing).
func (s *SchedulerStore) MaterializeTasks(ctx context.Context, runID string, tasks []domain.TaskSpec) error {
	rid, err := parseUUID(runID)
	if err != nil {
		return err
	}
	if len(tasks) == 0 {
		return nil
	}
	run, err := s.q.GetDagRunByID(ctx, rid)
	if err != nil {
		return fmt.Errorf("loading run: %w", err)
	}
	rows := make([]queries.CreateTaskInstancesParams, len(tasks))
	for i, t := range tasks {
		maxTries := int32(1)
		if t.Retries != nil {
			maxTries = toInt32(*t.Retries + 1)
		}
		rows[i] = queries.CreateTaskInstancesParams{
			TenantID: run.TenantID,
			DagRunID: rid,
			TaskID:   t.TaskID,
			Operator: string(t.Type),
			MaxTries: maxTries,
			State:    queries.TaskStateNone,
			// A NULL pool is the implicit default_pool; the pool-usage query and the
			// admission gate both resolve it the same way, so an unset pool needs no
			// sentinel value written here. Carried through the batched COPY path so
			// pool occupancy is attributed correctly (Wave-2 review HIGH-1).
			Pool:      poolOrNil(t.Pool),
			TryNumber: 1,
		}
	}
	if _, err := s.q.CreateTaskInstances(ctx, rows); err != nil {
		return fmt.Errorf("creating task instances for run %q: %w", runID, err)
	}
	return nil
}

// poolOrNil maps an unset task pool to a NULL column so a task with no declared
// pool is stored as the implicit default_pool (resolved at read time), matching
// the admission gate's default-pool fallback.
func poolOrNil(pool string) *string {
	if pool == "" {
		return nil
	}
	return &pool
}

// PoolBudgets returns every named pool's slot cap keyed by
// scheduler.PoolKey(tenantID, name) — the cross-DAG admission budget the pool
// gate enforces (ADR 0053 Stage 3). The scheduler calls it once per tick, and
// only on the Pro path; Lite never loads pool budgets.
func (s *SchedulerStore) PoolBudgets(ctx context.Context) (map[string]int, error) {
	rows, err := s.q.PoolBudgets(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading pool budgets: %w", err)
	}
	out := make(map[string]int, len(rows))
	for _, row := range rows {
		out[scheduler.PoolKey(uuidToString(row.TenantID), row.Name)] = int(row.Slots)
	}
	return out, nil
}

// ApplyTransition moves a task instance to a new state.
func (s *SchedulerStore) ApplyTransition(ctx context.Context, runID, taskID string, to domain.TaskState) error {
	rid, err := parseUUID(runID)
	if err != nil {
		return err
	}
	return s.q.UpdateTaskInstanceStateByRunTask(ctx, queries.UpdateTaskInstanceStateByRunTaskParams{
		DagRunID: rid,
		TaskID:   taskID,
		State:    queries.TaskState(to),
	})
}

// ApplyTransitions moves every listed task of a run to the SAME target state in
// one UPDATE, the batched equivalent of calling ApplyTransition once per task.
// The scheduler groups a tick's plain state-set transitions by target state and
// flushes each group here, collapsing R updates into one per distinct state. The
// per-row stamping is identical to the single-row query, so the result is
// byte-identical — only the statement count drops. An empty list is a no-op.
func (s *SchedulerStore) ApplyTransitions(ctx context.Context, runID string, taskIDs []string, to domain.TaskState) error {
	if len(taskIDs) == 0 {
		return nil
	}
	rid, err := parseUUID(runID)
	if err != nil {
		return err
	}
	return s.q.UpdateTaskInstanceStatesByRunTasks(ctx, queries.UpdateTaskInstanceStatesByRunTasksParams{
		DagRunID: rid,
		TaskIds:  taskIDs,
		State:    queries.TaskState(to),
	})
}

// SetTaskNote attaches operational context to a task instance, shown in the UI.
func (s *SchedulerStore) SetTaskNote(ctx context.Context, runID, taskID, note string) error {
	rid, err := parseUUID(runID)
	if err != nil {
		return err
	}
	return s.q.SetTaskInstanceNote(ctx, queries.SetTaskInstanceNoteParams{
		DagRunID: rid,
		TaskID:   taskID,
		Note:     strPtr(note),
	})
}

// ResetForRetry returns a task instance to 'none', clears its timestamps, and
// increments its try number so the scheduler re-evaluates and re-runs it. It
// uses the up_for_retry-guarded query so a stale retry decision cannot reset a
// TI that has since been re-dispatched (audit follow-up; see the query doc). The
// bool reports whether the guarded update actually fired (exactly one row): a
// false means the TI was no longer up_for_retry and nothing was reset, so the
// caller must not record a retry it did not perform.
func (s *SchedulerStore) ResetForRetry(ctx context.Context, runID, taskID string) (bool, error) {
	rid, err := parseUUID(runID)
	if err != nil {
		return false, err
	}
	n, err := s.q.ResetTaskInstanceForRetry(ctx, queries.ResetTaskInstanceForRetryParams{
		DagRunID: rid,
		TaskID:   taskID,
	})
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// ResetForInfraReplace returns a task instance failed by a reaper as infra
// (last_failure_kind='infra') to 'none' so the scheduler re-runs it, bumping
// infra_attempts instead of try_number — an infrastructure fault must not
// consume the user's retry budget (ADR 0051 Phase 1). It uses the
// failed+infra-guarded query so a late terminal report or a non-infra failure at
// state='failed' cannot be re-placed off-budget. The bool reports whether the
// guarded update fired (exactly one row): a false means the TI was no longer a
// failed-infra candidate, so the caller must not record a re-placement it did
// not perform.
func (s *SchedulerStore) ResetForInfraReplace(ctx context.Context, runID, taskID string) (bool, error) {
	rid, err := parseUUID(runID)
	if err != nil {
		return false, err
	}
	n, err := s.q.ResetTaskInstanceInfraReplace(ctx, queries.ResetTaskInstanceInfraReplaceParams{
		DagRunID:              rid,
		TaskID:                taskID,
		ConfirmMaxWaitSeconds: scheduler.InfraConfirmMaxWait.Seconds(),
	})
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// RecordDispatchFailure increments a scheduled task's dispatch-failure counter
// and backs off its next attempt to nextAt (ADR 0031 Amendment A).
func (s *SchedulerStore) RecordDispatchFailure(ctx context.Context, runID, taskID string, nextAt time.Time) error {
	rid, err := parseUUID(runID)
	if err != nil {
		return err
	}
	return s.q.RecordDispatchFailure(ctx, queries.RecordDispatchFailureParams{
		DagRunID:       rid,
		TaskID:         taskID,
		NextDispatchAt: pgtype.Timestamptz{Time: nextAt, Valid: true},
	})
}

// RecordDispatchBackpressure backs off a scheduled task after a retriable-forever
// cluster-backpressure dispatch failure (quota 403 / APF 429), setting nextAt
// WITHOUT incrementing dispatch_attempts so it never accumulates toward the
// dispatch_failed cap (ADR 0053).
func (s *SchedulerStore) RecordDispatchBackpressure(ctx context.Context, runID, taskID string, nextAt time.Time) error {
	rid, err := parseUUID(runID)
	if err != nil {
		return err
	}
	return s.q.RecordDispatchBackpressure(ctx, queries.RecordDispatchBackpressureParams{
		DagRunID:       rid,
		TaskID:         taskID,
		NextDispatchAt: pgtype.Timestamptz{Time: nextAt, Valid: true},
	})
}

// MarkQueued moves a task from the scheduled slot this tick planned to queued
// after its dispatch was accepted. expectNextDispatchAt is the next_dispatch_at
// the tick read (nil when unset). It reports false, with no error, when the row
// has moved on: a buffered worker already failed or re-offered the task, or the
// agent already reported (see MarkTaskInstanceQueued).
func (s *SchedulerStore) MarkQueued(ctx context.Context, runID, taskID string, expectNextDispatchAt *time.Time) (bool, error) {
	rid, err := parseUUID(runID)
	if err != nil {
		return false, err
	}
	expect := pgtype.Timestamptz{}
	if expectNextDispatchAt != nil {
		expect = pgtype.Timestamptz{Time: *expectNextDispatchAt, Valid: true}
	}
	n, err := s.q.MarkTaskInstanceQueued(ctx, queries.MarkTaskInstanceQueuedParams{
		DagRunID:             rid,
		TaskID:               taskID,
		ExpectNextDispatchAt: expect,
	})
	return n > 0, err
}

// RequeueDispatch re-offers a task whose buffered dispatch failed in the worker:
// back to scheduled, held until nextAt, with one more dispatch attempt when
// countAttempt. It reports false when the task is no longer scheduled or queued.
func (s *SchedulerStore) RequeueDispatch(ctx context.Context, runID, taskID string, countAttempt bool, nextAt time.Time) (bool, error) {
	rid, err := parseUUID(runID)
	if err != nil {
		return false, err
	}
	inc := int32(0)
	if countAttempt {
		inc = 1
	}
	n, err := s.q.RequeueDispatch(ctx, queries.RequeueDispatchParams{
		DagRunID:         rid,
		TaskID:           taskID,
		NextDispatchAt:   pgtype.Timestamptz{Time: nextAt, Valid: true},
		AttemptIncrement: inc,
	})
	return n > 0, err
}

// DispatchAttempts returns the consecutive dispatch-failure count of a task
// still scheduled or queued; active is false when it has moved on.
func (s *SchedulerStore) DispatchAttempts(ctx context.Context, runID, taskID string) (attempts int, active bool, err error) {
	rid, err := parseUUID(runID)
	if err != nil {
		return 0, false, err
	}
	n, err := s.q.DispatchAttemptsForActive(ctx, queries.DispatchAttemptsForActiveParams{DagRunID: rid, TaskID: taskID})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return int(n), true, nil
}

// FailDispatchExhausted fails a scheduled task as dispatch_failed once its
// dispatch-attempt budget is spent (ADR 0031 Amendment A).
func (s *SchedulerStore) FailDispatchExhausted(ctx context.Context, runID, taskID, reason string) error {
	rid, err := parseUUID(runID)
	if err != nil {
		return err
	}
	return s.q.FailDispatchExhausted(ctx, queries.FailDispatchExhaustedParams{
		DagRunID:     rid,
		TaskID:       taskID,
		ErrorMessage: &reason,
	})
}

// RedispatchReschedule returns a task parked in up_for_reschedule to 'none' for
// re-dispatch, preserving try_number (reschedule is not a retry; #380).
func (s *SchedulerStore) RedispatchReschedule(ctx context.Context, runID, taskID string) error {
	rid, err := parseUUID(runID)
	if err != nil {
		return err
	}
	return s.q.RedispatchRescheduledTaskInstance(ctx, queries.RedispatchRescheduledTaskInstanceParams{
		DagRunID: rid,
		TaskID:   taskID,
	})
}

// SetRunState updates a run's state.
func (s *SchedulerStore) SetRunState(ctx context.Context, runID string, state domain.DagRunState) error {
	rid, err := parseUUID(runID)
	if err != nil {
		return err
	}
	return s.q.StampDagRunState(ctx, queries.StampDagRunStateParams{
		ID:    rid,
		State: queries.DagRunState(state),
	})
}

// ClaimAlertAttempt atomically claims one on-failure send attempt for a run
// (#431). The UPDATE consumes an attempt and sets the next-attempt time, but only
// while the episode is undelivered, within budget, and past its backoff — see the
// query for why each predicate exists. pgx.ErrNoRows means the claim was refused
// on one of those grounds: not an error, just a lost claim, so report won=false.
//
// Claiming an attempt is NOT the same as recording delivery; that is
// MarkRunAlertDelivered. Conflating the two is what made a failed send a
// permanently lost page.
// Returns the attempt number won (0 when the claim was refused), which the caller
// passes back to MarkRunAlertDelivered so a superseded send cannot stamp.
func (s *SchedulerStore) ClaimAlertAttempt(ctx context.Context, runID string, maxAttempts int, backoff time.Duration) (int, error) {
	rid, err := parseUUID(runID)
	if err != nil {
		return 0, err
	}
	iv := pgtype.Interval{Microseconds: backoff.Microseconds(), Valid: true}
	row, err := s.q.ClaimAlertAttempt(ctx, queries.ClaimAlertAttemptParams{
		ID:          rid,
		Backoff:     iv,
		MaxAttempts: int32(maxAttempts), //nolint:gosec // a small configured constant, never attacker-controlled
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	return int(row.AlertAttempts), nil
}

// MarkRunAlertDelivered stamps a run's on-failure alert as delivered, for the
// attempt the caller won. The attempt is part of the predicate so a stamp from a
// send that an operator clear has since superseded matches no row — see the query.
func (s *SchedulerStore) MarkRunAlertDelivered(ctx context.Context, runID string, attempt int) error {
	rid, err := parseUUID(runID)
	if err != nil {
		return err
	}
	return s.q.MarkRunAlertDelivered(ctx, queries.MarkRunAlertDeliveredParams{
		ID:      rid,
		Attempt: int32(attempt), //nolint:gosec // a small bounded counter, never attacker-controlled
	})
}

// ScheduledDAGs returns active, unpaused, cron-scheduled DAGs with the logical
// date of their most recent run.
func (s *SchedulerStore) ScheduledDAGs(ctx context.Context) ([]scheduler.ScheduledDAG, error) {
	rows, err := s.q.ListScheduledDags(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing scheduled dags: %w", err)
	}
	out := make([]scheduler.ScheduledDAG, 0, len(rows))
	for _, r := range rows {
		out = append(out, scheduler.ScheduledDAG{
			TenantID:      uuidToString(r.TenantID),
			DagID:         r.DagID,
			Schedule:      strOrEmpty(r.Schedule),
			LastLogical:   timeFromAny(r.LastLogical),
			StartDate:     timePtr(r.StartDate),
			Catchup:       r.Catchup,
			MaxActiveRuns: int(r.MaxActiveRuns),
		})
	}
	return out, nil
}

// CreateScheduledRun inserts a scheduled run for the DAG dagID owned by the
// tenant tenantID (a tenant UUID), idempotent on run_id. The tenant is explicit
// because a dag_id is unique only within its tenant (#209). A tenant at its
// max_runs_per_day gets domain.ErrLimitExceeded and no run.
func (s *SchedulerStore) CreateScheduledRun(ctx context.Context, tenantID, dagID string, logical time.Time) error {
	tid, err := parseUUID(tenantID)
	if err != nil {
		return fmt.Errorf("scheduled run tenant id %q: %w", tenantID, err)
	}
	runID := "scheduled__" + logical.UTC().Format(time.RFC3339)
	return createRunWithinDailyLimit(ctx, s.q, s.pool, tid, runCreation{
		insert: func(q *queries.Queries) (bool, error) {
			n, err := q.CreateScheduledRunByDagID(ctx, queries.CreateScheduledRunByDagIDParams{
				RunID:       runID,
				LogicalDate: pgtype.Timestamptz{Time: logical, Valid: true},
				TenantID:    tid,
				DagID:       dagID,
			})
			return n > 0, err
		},
		// A slot that already exists is a no-op (ON CONFLICT DO NOTHING), at
		// the cap too.
		exists: func(q *queries.Queries) (bool, error) {
			return q.DagRunExistsByDagID(ctx, queries.DagRunExistsByDagIDParams{TenantID: tid, DagID: dagID, RunID: runID})
		},
	})
}

// applyDefaultRetries fills each task's retries from the DAG default_args when
// the task has no explicit value, so materialization can derive max_tries.
func applyDefaultRetries(spec *domain.DAGSpec) {
	if spec.DefaultArgs == nil {
		return
	}
	for i := range spec.Tasks {
		if spec.Tasks[i].Retries == nil {
			r := spec.DefaultArgs.Retries
			spec.Tasks[i].Retries = &r
		}
	}
}

var _ scheduler.Store = (*SchedulerStore)(nil)
var _ executor.WarmTargetSource = (*SchedulerStore)(nil)
var _ executor.BusyWarmWorkerSource = (*SchedulerStore)(nil)

// RecordStagingVolume records a per-run staging volume as active, keyed by PVC
// name (idempotent — called per task as the PVC is ensured). ADR 0022.
func (s *SchedulerStore) RecordStagingVolume(ctx context.Context, tenantID, dagID, runID, pvcName, size string) error {
	tid, err := parseUUID(tenantID)
	if err != nil {
		return fmt.Errorf("staging volume tenant id %q: %w", tenantID, err)
	}
	if err := s.q.RecordStagingVolume(ctx, queries.RecordStagingVolumeParams{
		TenantID: tid, DagID: dagID, RunID: runID, PvcName: pvcName, Size: size,
	}); err != nil {
		return fmt.Errorf("recording staging volume: %w", err)
	}
	return nil
}

// MarkStagingDeleted records that a staging volume's PVC was deleted and why
// (run_succeeded | ttl_expired | orphaned).
func (s *SchedulerStore) MarkStagingDeleted(ctx context.Context, pvcName, reason string) error {
	var rp *string
	if reason != "" {
		rp = &reason
	}
	if err := s.q.MarkStagingDeleted(ctx, queries.MarkStagingDeletedParams{PvcName: pvcName, Reason: rp}); err != nil {
		return fmt.Errorf("marking staging volume deleted: %w", err)
	}
	return nil
}

// ListReapCandidates returns every dag_run currently in 'running' state with
// the timestamp of its most recent activity, for the scheduler's orphan reaper.
// The query (sqlc.runs.ListOrphanCandidates) is the authority on how to compute
// the timestamp; the reaper only decides whether each one is past its threshold.
func (s *SchedulerStore) ListReapCandidates(ctx context.Context) ([]executor.ReapCandidate, error) {
	rows, err := s.q.ListOrphanCandidates(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing orphan candidates: %w", err)
	}
	out := make([]executor.ReapCandidate, 0, len(rows))
	for _, r := range rows {
		var last time.Time
		if r.LastActivity.Valid {
			last = r.LastActivity.Time.UTC()
		}
		out = append(out, executor.ReapCandidate{
			RunID:        uuidToString(r.ID),
			DagID:        r.DagIDText,
			LastActivity: last,
		})
	}
	return out, nil
}

// pgLockNotAvailable is the SQLSTATE a NOWAIT row lock raises when another
// transaction holds a conflicting lock.
const pgLockNotAvailable = "55P03"

// ReapRun fails an orphaned dag run, then any of its still-active task
// instances, inside a single transaction, and reports whether it did. The list
// the reaper decided from is only a snapshot, so the orphan predicate is
// re-checked atomically here: the run's task instances are share-locked first,
// without waiting (a task instance being written counts as activity and makes
// the reap a no-op), then the run UPDATE applies the same predicate
// ListReapCandidates does (still running, no live task instance, last activity
// at or before quietBefore). If
// zero rows are touched the run is no longer an orphan (a task instance moved,
// fresh activity landed, or a competing finalizer settled it), the transaction
// rolls back without touching any task instance, and ReapRun returns false so
// the caller leaves the run's pods alone. This guarantees we cannot leave a run
// as `success`/`failed` while flipping its TIs to `failed (orphaned)`, nor fail
// a run that resumed between the list and the reap. Idempotent: a second call
// on an already-failed run returns false.
func (s *SchedulerStore) ReapRun(ctx context.Context, runID string, quietBefore time.Time) (bool, error) {
	rid, err := parseUUID(runID)
	if err != nil {
		return false, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("beginning reap tx: %w", err)
	}
	defer func() {
		// Rollback after a successful commit is a no-op (tx is closed) and after
		// a returned error there is no recovery to do; pgx logs it via the pool
		// already. Silencing it keeps the lint happy without hiding a real bug.
		_ = tx.Rollback(ctx) //nolint:errcheck // best-effort cleanup; commit path returns the meaningful error
	}()
	q := s.q.WithTx(tx)
	if lerr := q.LockRunTaskInstancesForReap(ctx, rid); lerr != nil {
		var pgErr *pgconn.PgError
		if errors.As(lerr, &pgErr) && pgErr.Code == pgLockNotAvailable {
			// A task instance of the run is being written right now: that is
			// activity, so the run is not an orphan this cycle.
			return false, nil
		}
		return false, fmt.Errorf("locking orphan candidate task instances: %w", lerr)
	}
	rows, err := q.MarkRunOrphanedRun(ctx, queries.MarkRunOrphanedRunParams{
		ID:          rid,
		QuietBefore: pgtype.Timestamptz{Time: quietBefore, Valid: true},
	})
	if err != nil {
		return false, fmt.Errorf("failing orphaned run: %w", err)
	}
	if rows == 0 {
		// No longer an orphan: abort without touching task instances.
		return false, nil
	}
	if err := q.MarkRunOrphanedTaskInstances(ctx, rid); err != nil {
		return false, fmt.Errorf("failing orphaned task instances: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("committing reap tx: %w", err)
	}
	return true, nil
}

// ListAgentLostCandidates returns every `running` TI with a non-null
// last_heartbeat_at, for the scheduler's TI heartbeat reaper (#128). The
// reaper applies the threshold per row so the SQL stays simple.
func (s *SchedulerStore) ListAgentLostCandidates(ctx context.Context) ([]executor.AgentLostCandidate, error) {
	rows, err := s.q.ListAgentLostCandidates(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing agent-lost candidates: %w", err)
	}
	out := make([]executor.AgentLostCandidate, 0, len(rows))
	for _, r := range rows {
		var last time.Time
		if r.LastHeartbeatAt.Valid {
			last = r.LastHeartbeatAt.Time.UTC()
		}
		out = append(out, executor.AgentLostCandidate{
			TaskInstanceID: uuidToString(r.TaskInstanceID),
			TenantID:       uuidToString(r.TenantID),
			DagRunID:       uuidToString(r.DagRunID),
			DagID:          r.DagIDText,
			TaskID:         r.TaskID,
			TryNumber:      int(r.TryNumber),
			AttemptEpoch:   int(r.AttemptEpoch),
			LastHeartbeat:  last,
		})
	}
	return out, nil
}

// MarkTaskDispatchFailed transitions a TI to `failed` after its asynchronous
// dispatch failed inside the BufferedDispatcher worker (#127). The SQL guard
// only targets scheduled/queued rows, so a TI that already moved to running
// or terminal between the worker accepting the request and the dispatch
// failing is left alone (defense in depth — the agent's late progress report
// wins over the dispatcher's "I failed" claim).
func (s *SchedulerStore) MarkTaskDispatchFailed(ctx context.Context, runID, taskID, reason string) error {
	rid, err := parseUUID(runID)
	if err != nil {
		return err
	}
	return s.q.MarkTaskDispatchFailed(ctx, queries.MarkTaskDispatchFailedParams{
		DagRunID:     rid,
		TaskID:       taskID,
		ErrorMessage: &reason,
	})
}

// MarkTaskAgentLost transitions one TI to `failed` with the agent_lost
// reason. The WHERE state='running' guard makes this idempotent and prevents
// a late terminal report being overwritten — if the row already moved, we
// touch zero rows and return nil. It is pinned to the listed attempt,
// (tryNumber, attemptEpoch) (ADR 0051 amendment): a mark computed for a
// superseded execution is a no-op on its replacement.
func (s *SchedulerStore) MarkTaskAgentLost(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int) (bool, error) {
	tid, err := parseUUID(taskInstanceID)
	if err != nil {
		return false, err
	}
	n, err := s.q.MarkTaskAgentLost(ctx, queries.MarkTaskAgentLostParams{
		Provisional: s.provisionalInfraMarks,
		ID:          tid, TryNumber: toInt32(tryNumber), AttemptEpoch: toInt32(attemptEpoch),
	})
	if err != nil {
		return false, fmt.Errorf("marking task agent-lost: %w", err)
	}
	return n == 1, nil
}

// ListStaleQueuedCandidates returns every `queued` TI with its queued_at, for
// the dispatch-lost reaper (#202). The reaper applies the threshold per row
// so the SQL stays simple.
func (s *SchedulerStore) ListStaleQueuedCandidates(ctx context.Context) ([]executor.StaleQueuedCandidate, error) {
	rows, err := s.q.ListStaleQueuedTaskInstances(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing stale-queued candidates: %w", err)
	}
	out := make([]executor.StaleQueuedCandidate, 0, len(rows))
	for _, r := range rows {
		var qed time.Time
		if r.QueuedAt.Valid {
			qed = r.QueuedAt.Time.UTC()
		}
		out = append(out, executor.StaleQueuedCandidate{
			TaskInstanceID: uuidToString(r.TaskInstanceID),
			DagRunID:       uuidToString(r.DagRunID),
			DagID:          r.DagIDText,
			TaskID:         r.TaskID,
			TryNumber:      int(r.TryNumber),
			AttemptEpoch:   int(r.AttemptEpoch),
			QueuedAt:       qed,
			WarmWorkerID:   strOrEmpty(r.WarmWorkerID),
		})
	}
	return out, nil
}

// ListWarmBoundRunningTIs returns every `running` TI durably bound to a warm
// worker (warm_worker_id IS NOT NULL), for the warm-worker-lost reaper (ADR 0058
// N1d-a2). With warm pools off no TI is ever bound, so this is always empty and
// the reaper is inert.
func (s *SchedulerStore) ListWarmBoundRunningTIs(ctx context.Context) ([]executor.WarmBoundTI, error) {
	rows, err := s.q.ListWarmBoundRunningTIs(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing warm-bound running TIs: %w", err)
	}
	out := make([]executor.WarmBoundTI, 0, len(rows))
	for _, r := range rows {
		out = append(out, executor.WarmBoundTI{
			TaskInstanceID: uuidToString(r.TaskInstanceID),
			DagRunID:       uuidToString(r.DagRunID),
			TaskID:         r.TaskID,
			TryNumber:      int(r.TryNumber),
			AttemptEpoch:   int(r.AttemptEpoch),
			WarmWorkerID:   strOrEmpty(r.WarmWorkerID),
		})
	}
	return out, nil
}

// ListBusyWarmWorkerPods returns the set of warm-worker pod names currently
// serving a `running` attempt (ADR 0058 N1d-b): a warm worker is BUSY iff some
// `running` task_instance is durably bound to it (warm_worker_id = the pod's own
// name). The busy-aware warm-pool reconciler reads this once per tick to classify
// each live warm pod as busy or idle, so scale-down/drain deletes only IDLE
// workers and never kills an in-flight attempt (review findings M1/M2). With warm
// pools off no TI is ever bound, so the set is always empty and every worker
// classifies as idle — byte-for-byte today. Implements
// executor.BusyWarmWorkerSource without the executor importing storage.
func (s *SchedulerStore) ListBusyWarmWorkerPods(ctx context.Context) (map[string]bool, error) {
	rows, err := s.q.ListBusyWarmWorkerPods(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing busy warm-worker pods: %w", err)
	}
	busy := make(map[string]bool, len(rows))
	for _, name := range rows {
		if name != nil {
			busy[*name] = true
		}
	}
	return busy, nil
}

// MarkTaskDispatchLost transitions one TI to `failed` with the dispatch_lost
// reason. The WHERE state='queued' guard makes this idempotent: a TI that
// has since been dispatched (real progress landed) is left alone, and so is a
// row on a different (tryNumber, attemptEpoch) than the one listed (ADR 0051
// amendment). It returns whether a row was actually updated.
func (s *SchedulerStore) MarkTaskDispatchLost(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int) (bool, error) {
	tid, err := parseUUID(taskInstanceID)
	if err != nil {
		return false, err
	}
	n, err := s.q.MarkTaskDispatchLost(ctx, queries.MarkTaskDispatchLostParams{
		Provisional: s.provisionalInfraMarks,
		ID:          tid, TryNumber: toInt32(tryNumber), AttemptEpoch: toInt32(attemptEpoch),
	})
	if err != nil {
		return false, fmt.Errorf("marking task dispatch-lost: %w", err)
	}
	return n > 0, nil
}

// ListRunningTasks returns the `running`, non-warm TIs that have been running
// for at least grace, with the timestamp each entered running, for the pod-lost
// reaper (#527). The reaper still applies the pod-liveness check per row.
func (s *SchedulerStore) ListRunningTasks(ctx context.Context, grace time.Duration) ([]executor.PodLostCandidate, error) {
	rows, err := s.q.ListRunningTasks(ctx, grace.Seconds())
	if err != nil {
		return nil, fmt.Errorf("listing running tasks: %w", err)
	}
	out := make([]executor.PodLostCandidate, 0, len(rows))
	for _, r := range rows {
		var since time.Time
		if r.StartedAt.Valid {
			since = r.StartedAt.Time.UTC()
		}
		out = append(out, executor.PodLostCandidate{
			TaskInstanceID: uuidToString(r.TaskInstanceID),
			DagRunID:       uuidToString(r.DagRunID),
			DagID:          r.DagIDText,
			TaskID:         r.TaskID,
			TryNumber:      int(r.TryNumber),
			AttemptEpoch:   int(r.AttemptEpoch),
			RunningSince:   since,
			Heartbeated:    r.Heartbeated,
			TenantID:       uuidToString(r.TenantID),
		})
	}
	return out, nil
}

// MarkTaskPodLost transitions one TI to `failed` with the pod_lost reason. The
// WHERE state='running' guard makes it idempotent: a TI that has since moved on
// (a late terminal report landed) is left alone, and so is a row on a different
// (tryNumber, attemptEpoch) than the one listed (ADR 0051 amendment).
func (s *SchedulerStore) MarkTaskPodLost(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int) (bool, error) {
	tid, err := parseUUID(taskInstanceID)
	if err != nil {
		return false, err
	}
	n, err := s.q.MarkTaskPodLost(ctx, queries.MarkTaskPodLostParams{
		Provisional: s.provisionalInfraMarks,
		ID:          tid, TryNumber: toInt32(tryNumber), AttemptEpoch: toInt32(attemptEpoch),
	})
	if err != nil {
		return false, fmt.Errorf("marking task pod-lost: %w", err)
	}
	return n == 1, nil
}

// ListActiveStagingVolumes returns active staging volumes joined with their DAG
// run's state (empty when the run row is gone), for the GC (ADR 0022).
func (s *SchedulerStore) ListActiveStagingVolumes(ctx context.Context) ([]domain.StagingVolumeState, error) {
	rows, err := s.q.ListActiveStagingVolumes(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing staging volumes: %w", err)
	}
	out := make([]domain.StagingVolumeState, 0, len(rows))
	for _, row := range rows {
		st := ""
		if row.RunState != nil {
			st = string(*row.RunState)
		}
		out = append(out, domain.StagingVolumeState{
			PVCName: row.PvcName, RunState: st, RunEndedAt: timePtr(row.RunEndedAt), CreatedAt: timeVal(row.CreatedAt),
		})
	}
	return out, nil
}

// rfc3339OrEmpty renders a nullable timestamp for display, empty when absent.
// Alerts show this value, so the format is the one the API already emits rather
// than Go's default.
func rfc3339OrEmpty(ts pgtype.Timestamptz) string {
	if !ts.Valid {
		return ""
	}
	return ts.Time.UTC().Format(time.RFC3339)
}
