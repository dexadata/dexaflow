package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/dexadata/dexaflow/internal/agentrpc"
	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/dispatch"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
	"github.com/dexadata/dexaflow/internal/scheduler"
	"github.com/dexadata/dexaflow/internal/storage/queries"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// ExecutionStore resolves task execution context from Postgres. It implements
// both agentrpc.Store (serving the in-pod agent) and dispatch.Resolver (feeding
// the pod-path dispatcher) over the same dag_version spec.
type ExecutionStore struct {
	q     *queries.Queries
	specs *specCache
}

// NewExecutionStore builds an ExecutionStore over the given Postgres connection.
func NewExecutionStore(pg *Postgres) *ExecutionStore {
	return &ExecutionStore{q: pg.Queries, specs: sharedSpecCache(pg)}
}

// TaskSpec returns the agent-facing execution spec for a task instance.
func (s *ExecutionStore) TaskSpec(ctx context.Context, id auth.AgentIdentity) (agentrpc.TaskSpec, error) {
	task, spec, _, run, err := s.resolve(ctx, id.RunID, id.TaskID)
	if err != nil {
		return agentrpc.TaskSpec{}, err
	}
	// The DagRun's logical date, RFC3339, for the runtime's ds/ts (ADR 0040). Empty
	// when unset so the agent leaves LEOFLOW_TS/DS unset rather than stamping a zero.
	var logicalDate string
	if run.LogicalDate.Valid {
		logicalDate = run.LogicalDate.Time.UTC().Format(time.RFC3339)
	}
	// The DagRun's data interval, RFC3339, for the runtime's data_interval_start/end
	// context (ADR 0040). Empty when unset so the agent leaves them unset.
	var dataIntervalStart, dataIntervalEnd string
	if run.DataIntervalStart.Valid {
		dataIntervalStart = run.DataIntervalStart.Time.UTC().Format(time.RFC3339)
	}
	if run.DataIntervalEnd.Valid {
		dataIntervalEnd = run.DataIntervalEnd.Time.UTC().Format(time.RFC3339)
	}
	// The DagRun's params/conf JSON, for the runtime's context['params'] (#148). Empty
	// when the run carried no conf, so the agent leaves LEOFLOW_PARAMS unset (params={}).
	var paramsJSON string
	if len(run.Conf) > 0 {
		paramsJSON = string(run.Conf)
	}
	// When a reschedule-mode sensor is re-dispatched, deliver the time it first
	// entered reschedule so its get_first_reschedule_date returns the real value and
	// cumulative timeout works (#380). Best-effort: empty falls back to per-poke
	// timing, never blocks the spec. Empty on the first attempt (column is NULL).
	//
	// The attempt budget comes from the same row: a clear restores it to the new
	// try number + retries (#1131), past the spec's retries + 1, and the runtime
	// fires on_failure_callback only when try_number >= max_tries (#424). Reading
	// the spec value there would fire the callback on every retry of a cleared
	// task. The spec value is the fallback when the row cannot be read.
	var firstRescheduleAt string
	attemptBudget := maxTries(task)
	if rid, perr := parseUUID(id.RunID); perr == nil {
		if row, ferr := s.q.TaskInstanceAttemptFields(ctx,
			queries.TaskInstanceAttemptFieldsParams{DagRunID: rid, TaskID: id.TaskID}); ferr == nil {
			if row.FirstRescheduleAt.Valid {
				firstRescheduleAt = row.FirstRescheduleAt.Time.UTC().Format(time.RFC3339)
			}
			if row.MaxTries > 0 {
				attemptBudget = int(row.MaxTries)
			}
		}
	}
	var timeout int
	if task.ExecutionTimeoutSeconds != nil {
		timeout = *task.ExecutionTimeoutSeconds
	}
	// Marshal TaskFlow literals (#115) into a single string field on the gRPC
	// TaskSpec; the agent forwards it as LEOFLOW_CALL_ARGS_JSON. Marshaling
	// failures are surfaced — a non-serialisable map in dag.json is a
	// compile-time bug we want to see, not paper over.
	var callArgsJSON string
	if len(task.CallArgs) > 0 {
		b, mErr := json.Marshal(task.CallArgs)
		if mErr != nil {
			return agentrpc.TaskSpec{}, fmt.Errorf("marshaling task call_args: %w", mErr)
		}
		callArgsJSON = string(b)
	}
	// Marshal the captured operator constructor kwargs (ADR 0040) the same way;
	// the agent forwards it as LEOFLOW_OPERATOR_ARGS. A non-serialisable arg in
	// dag.json is a compile-time bug we surface rather than paper over.
	var operatorArgsJSON string
	if len(task.OperatorArgs) > 0 {
		b, mErr := json.Marshal(task.OperatorArgs)
		if mErr != nil {
			return agentrpc.TaskSpec{}, fmt.Errorf("marshaling operator args: %w", mErr)
		}
		operatorArgsJSON = string(b)
	}
	return agentrpc.TaskSpec{
		Operator:          string(task.Type),
		Entrypoint:        task.Entrypoint,
		DagVersion:        spec.DagVersion,
		Environment:       task.Env,
		XComInputMapping:  task.XComInput,
		XComSchema:        task.XComSchema,
		TimeoutSeconds:    timeout,
		CallArgsJSON:      callArgsJSON,
		OperatorClass:     task.OperatorClass,
		OperatorArgsJSON:  operatorArgsJSON,
		LogicalDate:       logicalDate,
		DependsOn:         task.DependsOn,
		DataIntervalStart: dataIntervalStart,
		DataIntervalEnd:   dataIntervalEnd,
		ParamsJSON:        paramsJSON,
		FirstRescheduleAt: firstRescheduleAt,
		MaxTries:          attemptBudget,
		OnFailureCallback: task.OnFailureCallback,
		// Carry the declared secret set (ADR 0045, ADR 0055) so a later increment
		// can scope delivery. Data only here — no secret is filtered by it yet.
		DeclaredVariables:   declaredVariables(task, spec),
		DeclaredConnections: declaredConnections(task, spec),
	}, nil
}

// declaredVariables and declaredConnections resolve the effective declared
// secret names for a task: the task's own declaration when it narrows the
// DAG-level set, otherwise the DAG-level declaration it inherits (ADR 0045
// §Settled #1). These carry the declared set onto the agent-facing spec; they
// do not filter what secrets are delivered — enforcement lands separately.
func declaredVariables(task domain.TaskSpec, spec domain.DAGSpec) []string {
	if len(task.Variables) > 0 {
		return task.Variables
	}
	return spec.Variables
}

func declaredConnections(task domain.TaskSpec, spec domain.DAGSpec) []string {
	if len(task.Connections) > 0 {
		return task.Connections
	}
	return spec.Connections
}

// maxTries is a task's total attempt budget: retries + 1 (a task with no retries
// runs once). It gates on_failure_callback to the terminal attempt (#424).
func maxTries(task domain.TaskSpec) int {
	if task.Retries != nil {
		return *task.Retries + 1
	}
	return 1
}

// ReportState records a state transition reported by the agent, persisting the
// exit code and error message and stamping started/ended/duration timestamps.
func (s *ExecutionStore) ReportState(ctx context.Context, id auth.AgentIdentity, state domain.TaskState, exitCode int, errMsg string) error {
	rid, err := parseUUID(id.RunID)
	if err != nil {
		return err
	}
	code := toInt32(exitCode)
	params := queries.ReportTaskResultParams{
		DagRunID:     rid,
		TaskID:       id.TaskID,
		Column3:      queries.TaskState(state), // sqlc names the $3::task_state cast param Column3.
		ExitCode:     &code,
		TryNumber:    toInt32(id.TryNumber),
		AttemptEpoch: attemptEpochArg(id),
	}
	if errMsg != "" {
		params.ErrorMessage = &errMsg
	}
	rows, err := s.q.ReportTaskResult(ctx, params)
	if err != nil {
		return err
	}
	// The UPDATE is guarded on the source state and the attempt, so zero rows
	// means the report arrived after the row moved on — not that anything
	// failed. Surface it as a distinct condition so the caller can acknowledge
	// the agent without pretending the write happened.
	if rows == 0 {
		return agentrpc.ErrStaleReport
	}
	return nil
}

// Reschedule parks an active task instance in up_for_reschedule with its next-poke
// time, so the scheduler re-dispatches it once reschedule_at passes (#380). Used by
// the agent's reschedule path. It is guarded on the attempt (try_number and attempt
// epoch, ADR 0051 amendment) and the active states, so a poke from a superseded
// attempt or onto a settled row does not apply and returns agentrpc.ErrStaleReport.
func (s *ExecutionStore) Reschedule(ctx context.Context, id auth.AgentIdentity, at time.Time) error {
	rid, err := parseUUID(id.RunID)
	if err != nil {
		return err
	}
	rows, err := s.q.RescheduleTaskInstance(ctx, queries.RescheduleTaskInstanceParams{
		DagRunID:     rid,
		TaskID:       id.TaskID,
		RescheduleAt: pgtype.Timestamptz{Time: at, Valid: true},
		TryNumber:    toInt32(id.TryNumber),
		AttemptEpoch: attemptEpochArg(id),
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		return agentrpc.ErrStaleReport
	}
	return nil
}

// RecordHeartbeat stamps last_heartbeat_at on the agent's TI so the
// scheduler's heartbeat reaper (#128) can tell a live task from one whose
// agent has gone silent. The SQL guard skips terminal rows — a late
// heartbeat after a terminal report is a no-op, not a regression.
func (s *ExecutionStore) RecordHeartbeat(ctx context.Context, id auth.AgentIdentity) error {
	rid, err := parseUUID(id.RunID)
	if err != nil {
		return err
	}
	rows, err := s.q.RecordTaskHeartbeat(ctx, queries.RecordTaskHeartbeatParams{
		DagRunID:     rid,
		TaskID:       id.TaskID,
		TryNumber:    toInt32(id.TryNumber),
		AttemptEpoch: attemptEpochArg(id),
	})
	if err != nil {
		return err
	}
	// Zero rows means the heartbeat did not apply: the row moved on — a reaper
	// settled it terminal, or a later attempt bumped try_number past this one.
	// Same "moved on" predicate ReportState uses; surfaced as ErrStaleReport so
	// the agent RPC turns it into a should_terminate signal (#474). Nothing
	// failed, so this is not a DB error.
	if rows == 0 {
		return agentrpc.ErrStaleReport
	}
	return nil
}

// BindWarmAttempt records the durable warm-attempt binding (ADR 0058 N1d-a1): the
// warm worker pod (workerPod, its own downward-API pod name) that acked this
// attempt as started, stamped onto warm_worker_id so a later failover reaper can
// tell which running attempts a dead warm pod held. The UPDATE is guarded on
// state IN ('queued', 'running'), so a settled attempt is never bound — an ack
// that races a reaper settling the row is a benign no-op (zero rows), not an
// error. It is written ONLY on a warm ack; a dedicated-pod attempt (and every
// attempt while warm pools are off) leaves warm_worker_id NULL.
//
// N1d-a2 deferral: warm_worker_id is intentionally NOT cleared when the attempt
// settles. The consuming reaper filters on state, so a lingering value on a
// terminal TI is harmless; a settle-time clear is left to that increment.
func (s *ExecutionStore) BindWarmAttempt(ctx context.Context, runID, taskID string, tryNumber, attemptEpoch int, workerPod string) error {
	rid, err := parseUUID(runID)
	if err != nil {
		return err
	}
	pod := workerPod
	epoch := toInt32(attemptEpoch)
	_, err = s.q.BindWarmAttempt(ctx, queries.BindWarmAttemptParams{
		DagRunID:     rid,
		TaskID:       taskID,
		TryNumber:    toInt32(tryNumber),
		AttemptEpoch: &epoch,
		WarmWorkerID: &pod,
	})
	// Zero rows is the guard working, not a failure: the attempt already moved on
	// (terminal or superseded), so there is nothing to bind. Only a real DB error
	// propagates; the caller (best-effort) logs it and keeps the worker serving.
	return err
}

// IsTaskInstanceLive reports whether the attempt id names (run, task, try and
// attempt epoch; a token without an epoch is matched on its try alone) is still
// live (present and in an active, non-terminal state), derived from the same
// predicate RecordHeartbeat writes on, but as a pure read with no
// side-effect (ADR 0055). It is the read-only revocation signal the secret path
// consults: a terminal, superseded (try_number moved on), or reaped attempt is
// not live, so its token stops resolving secrets even while the signature holds.
//
// It derives ONLY from (run, task, try, epoch) + active state, exactly as the
// heartbeat predicate does. It must never gain a run-recency / logical_date clause: a
// recency term would deny a legitimate clear-and-rerun of an old run, binding
// credential lifetime to the run's age rather than to the attempt.
func (s *ExecutionStore) IsTaskInstanceLive(ctx context.Context, id auth.AgentIdentity) (bool, error) {
	rid, err := parseUUID(id.RunID)
	if err != nil {
		return false, err
	}
	return s.q.IsTaskInstanceLive(ctx, queries.IsTaskInstanceLiveParams{
		DagRunID:     rid,
		TaskID:       id.TaskID,
		TryNumber:    toInt32(id.TryNumber),
		AttemptEpoch: attemptEpochArg(id),
	})
}

// FailTask marks a task instance failed by its ID, guarded by the attempt
// (try_number and attempt_epoch, from the pod's labels) and the active states so it never clobbers a different attempt or a
// terminal row. It implements part of executor.OutcomeReporter for the pod
// reconciler (ADR 0052).
func (s *ExecutionStore) FailTask(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int, reason string) error {
	tid, err := parseUUID(taskInstanceID)
	if err != nil {
		return err
	}
	msg := reason
	return s.q.FailTaskInstanceIfActive(ctx, queries.FailTaskInstanceIfActiveParams{
		ID:           tid,
		TryNumber:    toInt32(tryNumber),
		AttemptEpoch: toInt32(attemptEpoch),
		ErrorMessage: &msg,
	})
}

// SucceedTask marks a task instance succeeded by its ID — recovering a success
// whose report was lost (ADR 0052) — guarded by the attempt and the active states.
// A settle on an already-terminal or superseded row is a no-op.
func (s *ExecutionStore) SucceedTask(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int) error {
	_, err := s.SucceedTaskIfActive(ctx, taskInstanceID, tryNumber, attemptEpoch)
	return err
}

// SucceedTaskIfActive is SucceedTask reporting whether it changed the row. A
// false with no error means the attempt was not active: already terminal,
// superseded, or marked by a reaper meanwhile. It implements part of
// executor.InfraOverrideReporter.
func (s *ExecutionStore) SucceedTaskIfActive(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int) (bool, error) {
	tid, err := parseUUID(taskInstanceID)
	if err != nil {
		return false, err
	}
	n, err := s.q.SucceedTaskInstanceIfActive(ctx, queries.SucceedTaskInstanceIfActiveParams{
		ID:           tid,
		TryNumber:    toInt32(tryNumber),
		AttemptEpoch: toInt32(attemptEpoch),
	})
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// SucceedTaskOverInfraMark settles a durable SUCCESS record over a reaper's
// provisional infra mark of the same attempt (ADR 0052 amendment, part 1).
// It applies only while the mark is unconfirmed and inside
// scheduler.InfraConfirmMaxWait, on a running run; otherwise it changes
// nothing and reports false. On success it returns the mark it overrode and
// the attempt's log location. It implements part of
// executor.InfraOverrideReporter.
func (s *ExecutionStore) SucceedTaskOverInfraMark(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int) (executor.InfraOverride, bool, error) {
	tid, err := parseUUID(taskInstanceID)
	if err != nil {
		return executor.InfraOverride{}, false, err
	}
	row, err := s.q.SucceedTaskInstanceOverInfraMark(ctx, queries.SucceedTaskInstanceOverInfraMarkParams{
		ID:                    tid,
		TryNumber:             toInt32(tryNumber),
		AttemptEpoch:          toInt32(attemptEpoch),
		ConfirmMaxWaitSeconds: scheduler.InfraConfirmMaxWait.Seconds(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return executor.InfraOverride{}, false, nil
	}
	if err != nil {
		return executor.InfraOverride{}, false, fmt.Errorf("settling a success over an infra mark: %w", err)
	}
	return executor.InfraOverride{
		Mark:     row.Mark,
		TenantID: uuidToString(row.TenantID),
		DagID:    row.DagIDText,
		DagRunID: uuidToString(row.DagRunID),
		TaskID:   row.TaskID,
	}, true, nil
}

// RescheduleTask parks a task instance in up_for_reschedule with the recovered
// next-poke time, guarded by the attempt and the active states, consuming no retry
// budget (ADR 0052). Used by the reconciler when a reschedule report was lost.
func (s *ExecutionStore) RescheduleTask(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int, at time.Time) error {
	tid, err := parseUUID(taskInstanceID)
	if err != nil {
		return err
	}
	return s.q.RescheduleTaskInstanceByIDIfActive(ctx, queries.RescheduleTaskInstanceByIDIfActiveParams{
		ID:           tid,
		TryNumber:    toInt32(tryNumber),
		AttemptEpoch: toInt32(attemptEpoch),
		RescheduleAt: pgtype.Timestamptz{Time: at, Valid: true},
	})
}

// RequeueForRedispatch re-places a reclaimed warm assignment (ADR 0058 N1d-c,
// H2): an attempt that a warm worker was handed but demonstrably will not run
// (its stream ended holding an unacked lease, or it acked started=false) sits
// `queued` having never run. This moves it back to `scheduled` so the planner
// re-admits and re-dispatches it, rather than leaving it stuck until the
// 3-minute dispatch-lost reaper. Guarded to state='queued' and bounded to the
// exact attempt (runID, taskID, tryNumber): zero rows is the guard working (the
// TI is running or settled, or the attempt moved on), a benign no-op, not an
// error. It bumps neither try_number nor infra_attempts — the attempt never ran,
// this is a re-offer of the same attempt.
func (s *ExecutionStore) RequeueForRedispatch(ctx context.Context, runID, taskID string, tryNumber, attemptEpoch int) error {
	rid, err := parseUUID(runID)
	if err != nil {
		return err
	}
	epoch := toInt32(attemptEpoch)
	_, err = s.q.RequeueForRedispatch(ctx, queries.RequeueForRedispatchParams{
		DagRunID:     rid,
		TaskID:       taskID,
		TryNumber:    toInt32(tryNumber),
		AttemptEpoch: &epoch,
	})
	return err
}

// attemptEpochArg is the attempt-epoch argument of the agent-path fence
// (ADR 0051 amendment): the token's epoch, or nil for a token without the claim
// (minted before the upgrade, or stripped by an old replica during a rolling
// upgrade). The report and reschedule queries read nil as epoch 0 (COALESCE):
// every row that predates the upgrade is at epoch 0 and every attempt
// dispatched after it claims at least 1, so such a token reports only for its
// own pre-upgrade attempt. The heartbeat and liveness queries read nil as "any
// epoch of this try", so a live attempt is not killed for a missing claim.
func attemptEpochArg(id auth.AgentIdentity) *int32 {
	if !id.HasAttemptEpoch {
		return nil
	}
	n := toInt32(id.AttemptEpoch)
	return &n
}

// ErrNotDispatchable is returned by ResolveTask when the task's row is no
// longer in a pre-dispatch state (none, scheduled or queued): it is running or
// settled, so dispatching it again would start a second execution.
var ErrNotDispatchable = errors.New("task instance is not in a dispatchable state")

// ResolveTask returns the dispatcher's execution context for a run's task and
// claims a fresh attempt epoch for the execution it is about to start (ADR 0051
// amendment). Every call that succeeds moves the row's attempt_epoch forward,
// so two dispatches of one try never share it. The agent's identity (A2) and
// the pod's attempt-epoch label (A4) are minted from the returned value. A row past
// dispatch is refused with ErrNotDispatchable and keeps its epoch.
func (s *ExecutionStore) ResolveTask(ctx context.Context, runID, taskID string) (dispatch.Resolved, error) {
	task, spec, ver, _, err := s.resolve(ctx, runID, taskID)
	if err != nil {
		return dispatch.Resolved{}, err
	}
	rid, err := parseUUID(runID)
	if err != nil {
		return dispatch.Resolved{}, err
	}
	ti, err := s.q.ClaimAttemptEpoch(ctx, queries.ClaimAttemptEpochParams{DagRunID: rid, TaskID: taskID})
	if errors.Is(err, pgx.ErrNoRows) {
		return dispatch.Resolved{}, fmt.Errorf("task %q in run %q: %w", taskID, runID, ErrNotDispatchable)
	}
	if err != nil {
		return dispatch.Resolved{}, fmt.Errorf("claiming attempt epoch: %w", err)
	}
	image := ver.ImageReference
	if image == "" {
		image = spec.Image
	}
	pullPolicy := "IfNotPresent"
	if task.Execution != nil && task.Execution.ImagePullPolicy != "" {
		pullPolicy = task.Execution.ImagePullPolicy
	}
	return dispatch.Resolved{
		TaskInstanceID:  uuidToString(ti.ID),
		TenantID:        uuidToString(ti.TenantID),
		Image:           image,
		ImagePullPolicy: pullPolicy,
		TryNumber:       int(ti.TryNumber),
		AttemptEpoch:    int(ti.AttemptEpoch),
		Staging:         spec.Staging,
		// Materialize source on the executor side (Lite only); Pro ignores it.
		Source: spec.Source,
	}, nil
}

// resolve loads the dag version and run for a run id and returns the named task's
// spec. The run is returned too so callers can read run-scoped fields (e.g. its
// logical date) without a second query.
func (s *ExecutionStore) resolve(ctx context.Context, runID, taskID string) (domain.TaskSpec, domain.DAGSpec, queries.DagVersion, queries.DagRun, error) {
	rid, err := parseUUID(runID)
	if err != nil {
		return domain.TaskSpec{}, domain.DAGSpec{}, queries.DagVersion{}, queries.DagRun{}, err
	}
	run, err := s.q.GetDagRunByID(ctx, rid)
	if err != nil {
		return domain.TaskSpec{}, domain.DAGSpec{}, queries.DagVersion{}, queries.DagRun{}, fmt.Errorf("loading run: %w", err)
	}
	// The version row + decoded spec are memoized per (immutable) dag_version_id,
	// so re-resolving the same run's tasks on the dispatch path reuses the parse
	// the scheduler tick already did instead of a fresh fetch + unmarshal. The
	// spec is shared read-only; this path only reads it (task lookup + Image/
	// Staging/Source), never mutates it.
	ver, spec, err := s.specs.get(ctx, s.q, run.DagVersionID)
	if err != nil {
		return domain.TaskSpec{}, domain.DAGSpec{}, queries.DagVersion{}, queries.DagRun{}, err
	}
	for _, t := range spec.Tasks {
		if t.TaskID == taskID {
			return t, spec, ver, run, nil
		}
	}
	// The one failure on this path an agent can act on, so it is composed as a
	// SafeError and reaches the task pod verbatim while every driver error behind
	// the same call is redacted. It says the pod is running a task its
	// dag_version does not declare — a stale image, a mismatched version — rather
	// than that the control plane is broken, and it names only values the caller
	// already holds in its own token.
	return domain.TaskSpec{}, domain.DAGSpec{}, queries.DagVersion{}, queries.DagRun{},
		domain.Safef(domain.ErrNotFound, "task %q not found in run %q", taskID, runID)
}

// SettledRuns reports which of the given (tenant, run) pairs are settled: run
// in success or failed and no task instance outside success, failed, skipped
// and upstream_failed. It serves the reconciler's settled-run pod collection.
// A pair that is not two UUIDs, names no such run, or names a run of another
// tenant is left out, so its pods are never collected early.
func (s *ExecutionStore) SettledRuns(ctx context.Context, refs []executor.RunRef) (map[executor.RunRef]bool, error) {
	tenants := make([]pgtype.UUID, 0, len(refs))
	runs := make([]pgtype.UUID, 0, len(refs))
	for _, ref := range refs {
		tid, terr := parseUUID(ref.Tenant)
		rid, rerr := parseUUID(ref.Run)
		if terr == nil && rerr == nil {
			tenants = append(tenants, tid)
			runs = append(runs, rid)
		}
	}
	out := make(map[executor.RunRef]bool, len(runs))
	if len(runs) == 0 {
		return out, nil
	}
	rows, err := s.q.ListSettledRunIDs(ctx, queries.ListSettledRunIDsParams{TenantIds: tenants, RunIds: runs})
	if err != nil {
		return nil, fmt.Errorf("listing settled runs: %w", err)
	}
	for _, row := range rows {
		out[executor.RunRef{Tenant: uuidToString(row.TenantID), Run: uuidToString(row.ID)}] = true
	}
	return out, nil
}
