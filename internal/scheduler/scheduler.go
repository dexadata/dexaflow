package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync/atomic"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
)

// logSchedulerError logs an error from the scheduler loop or one of its
// reapers, downgrading to WARN ONLY when (a) the error wraps context.Canceled
// AND (b) the caller is currently in the step-down window. Both conditions
// together identify the expected cancel-fanout of a graceful leader step-down
// (the scheduler cancels its run-context when it loses the Postgres advisory
// lock, and every in-flight loop returns "context canceled" milliseconds
// later). Surfacing those as ERROR pages on a non-event (#311).
//
// When context.Canceled appears OUTSIDE a known step-down (someone else
// canceled the loop — a bug, a misconfig, a shutdown race), we KEEP it at
// ERROR. The tripwire is what catches an unexpected cancel that the downgrade
// would otherwise have silently swallowed. DeadlineExceeded is also kept at
// ERROR — a deadline IS a real stall worth seeing, not a deliberate cancel.
func logSchedulerError(logger *slog.Logger, msg string, err error, inStepDown bool) {
	if inStepDown && errors.Is(err, context.Canceled) {
		logger.Warn(msg+" canceled (leader step-down in progress)",
			"error", err, "expected", true)
		return
	}
	logger.Error(msg, "error", err)
}

// RunState is the scheduler's snapshot of a dag run: its topology and the
// current state of each task.
type RunState struct {
	// RunID is the dag_runs primary key (a UUID). It is what dispatch, the agent
	// token and the log sink key on, so it must not be swapped for the
	// user-facing id.
	RunID string
	// DisplayRunID is the dag_runs.run_id column — the identifier an operator
	// sees in the UI and passes to the API ("manual__2026-07-30T12:00:00+00:00").
	// Alerts render this one: a UUID is not something anyone can act on.
	DisplayRunID string
	// LogicalDate is the run's logical date in RFC3339, empty for an unscheduled
	// run. Carried here so an alert can say which interval failed without a
	// second query.
	LogicalDate string
	DagID       string
	// DagVersionID is the dag_versions row this run pins (dag_runs.dag_version_id).
	// It names the warm-worker pool a task is placed onto (ADR 0058 N1b1-place):
	// dispatch threads it to the placer so an attempt reuses a worker of the exact
	// version it compiled against. Empty for runs loaded by paths that do not set
	// it; the dedicated pod path ignores it.
	DagVersionID string
	TenantID     string
	State        domain.DagRunState
	Tasks        []domain.TaskSpec
	States       map[string]domain.TaskState
	// Tries and MaxTries hold the current and maximum attempt counts per task,
	// driving retry decisions. Absent entries mean no retry budget.
	Tries    map[string]int
	MaxTries map[string]int
	// EndedAt holds the failure timestamp per task (only meaningful for tasks
	// currently in `up_for_retry`). Combined with RetryDelaySeconds + Now, it
	// gates the `up_for_retry → none` transition so retries honor user-
	// declared backoff (issue #201). Absent entries mean no cooldown applies.
	EndedAt           map[string]*time.Time
	RetryDelaySeconds map[string]int
	// RescheduleAt holds the next-poke time per task (only meaningful for tasks in
	// up_for_reschedule). The planner re-dispatches such a task once Now >=
	// reschedule_at — without consuming retry budget (#380). Absent/zero entries
	// re-dispatch immediately (preserves the test seam, like EndedAt).
	RescheduleAt map[string]*time.Time
	// NextDispatchAt holds the earliest time a `scheduled` task may be dispatched
	// again after a synchronous dispatch failure (ADR 0031 Amendment A). The
	// planner does not promote scheduled→queued while Now < next_dispatch_at.
	// Absent/zero entries dispatch immediately (the common case: never failed).
	NextDispatchAt map[string]*time.Time
	// DispatchAttempts counts consecutive synchronous dispatch failures per task.
	// It is a separate counter from try_number — a dispatch failure is infra, not
	// a task failure — and drives the dispatch_failed give-up at
	// dispatchMaxAttempts. Absent entries mean zero.
	DispatchAttempts map[string]int
	// InfraFailed marks a `failed` task whose failure was infra-caused (agent/pod/
	// dispatch lost), from the durable last_failure_kind signal. Such a task
	// re-places without consuming the task's retry budget (ADR 0051 Phase 1) — an
	// infrastructure fault is not the user's task failing.
	InfraFailed map[string]bool
	// InfraAttempts counts asynchronous infra re-placements per task — the
	// try_number-free analog of DispatchAttempts for agent/pod/dispatch-lost faults.
	// It bounds the re-place at infraMaxAttempts so a poison placement cannot loop
	// forever. Absent entries mean zero.
	InfraAttempts map[string]int
	// Now is the wall-clock value the planner compares against EndedAt. Zero
	// means "skip the cooldown gate" so legacy callers + tests that don't set
	// retry_delay get the previous (immediate-retry) behavior.
	Now time.Time
	// Alerts carries the DAG's native on-failure alerting config (#424), loaded
	// from the dag.json alongside Tasks. nil means no alerting; the scheduler
	// dispatches these rules once the run finalizes as failed.
	Alerts *domain.AlertsConfig
	// MaxActiveTasks is the DAG's per-DAG task-concurrency cap (Airflow's
	// max_active_tasks, ADR 0053 Stage 1), loaded from the spec. Zero or negative
	// means unlimited — the admission gate is a no-op, so a DAG that never sets it
	// (and all of Lite) plans exactly as before.
	MaxActiveTasks int
	// ActiveTaskCount is the DAG-wide number of currently non-terminal (queued or
	// running) task instances across every active run of this DAG, plus any this
	// tick already admitted for the same DAG in earlier sibling runs. PlanRun
	// subtracts it from MaxActiveTasks to derive this run's admission headroom, so
	// the cap holds per-DAG across runs and one tick cannot breach it. The Step
	// loop populates it; it defaults to zero, leaving the gate unlimited when
	// MaxActiveTasks is unset.
	ActiveTaskCount int
	// PoolsEnabled turns on the named-pool slot gate (ADR 0053 Stage 3, Pro only).
	// The Step loop sets it from the scheduler's edition flag. Lite and any
	// non-Pro deployment leave it false, so the pool gate is a no-op and planning
	// is byte-identical to the max_active_tasks-only path.
	PoolsEnabled bool
	// PoolBudgets is the per-pool slot cap keyed by PoolKey(TenantID, pool). A pool
	// with a non-positive or absent budget is unlimited (fail open, never
	// deadlock). The Step loop sets it from the once-per-tick PoolBudgets snapshot;
	// nil in Lite. Shared read-only across sibling runs.
	PoolBudgets map[string]int
	// PoolActive is the cross-DAG count of currently non-terminal (queued+running)
	// task instances per pool, keyed like PoolBudgets, plus any this tick already
	// admitted into the same pool by earlier runs. PlanRun adds its own within-call
	// promotions on top. The Step loop sets it and folds each run's admissions back
	// in so a single tick cannot breach a pool across runs; nil in Lite.
	PoolActive map[string]int
}

// ScheduledDAG is a cron-scheduled DAG and the logical date of its latest run.
// Catchup and StartDate drive the per-tick catchup decision (#129): when a
// leader has been down across multiple slots, catchup=true backfills every
// missed slot while catchup=false jumps straight to the most recent one.
type ScheduledDAG struct {
	DagID       string
	Schedule    string
	LastLogical *time.Time
	StartDate   *time.Time
	Catchup     bool
	// MaxActiveRuns caps how many runs of this DAG may be active (queued or
	// running) at once. Zero means "unlimited" (Airflow-faithful: a missing
	// limit is unbounded). The scheduler enforces the cap in createDueRuns:
	// once the active-run count reaches this value, additional due slots are
	// skipped on this tick (issue #200).
	MaxActiveRuns int
}

// Store is the scheduler's view of persistent state. The concrete
// implementation is sqlc-backed; tests use a fake.
type Store interface {
	ActiveRuns(ctx context.Context) ([]RunState, error)
	// PoolBudgets returns every named pool's slot cap keyed by PoolKey(tenant,
	// pool) — the cross-DAG admission budget the pool gate enforces (ADR 0053
	// Stage 3). Called once per tick, and ONLY on the Pro path (poolsEnabled);
	// Lite never invokes it.
	PoolBudgets(ctx context.Context) (map[string]int, error)
	MaterializeTasks(ctx context.Context, runID string, tasks []domain.TaskSpec) error
	ApplyTransition(ctx context.Context, runID, taskID string, to domain.TaskState) error
	// ApplyTransitions moves every listed task of a run to the SAME target state
	// in one statement — the batched form of calling ApplyTransition once per
	// task. The scheduler groups a tick's plain state-set transitions
	// (scheduled/skipped/upstream_failed/up_for_retry) by target state and flushes
	// each group here, so R updates collapse to one per distinct state with a
	// byte-identical result. It carries no source-state guard, matching
	// ApplyTransition; the guarded none-rail resets stay on their own methods.
	ApplyTransitions(ctx context.Context, runID string, taskIDs []string, to domain.TaskState) error
	// ResetForRetry returns a task to 'none' and increments its try number so a
	// retry re-evaluates and re-runs it. It is guarded to the up_for_retry source
	// state; the bool reports whether the reset actually fired (false = the TI was
	// no longer up_for_retry, nothing reset) so the caller does not record a
	// phantom retry on a lost race.
	ResetForRetry(ctx context.Context, runID, taskID string) (bool, error)
	// ResetForInfraReplace returns a task a reaper failed as infra to 'none' for a
	// re-run, PRESERVING try_number and bumping infra_attempts instead — an infra
	// fault must not consume the retry budget (ADR 0051 Phase 1). Guarded to the
	// failed+infra source; the bool reports whether the reset fired (false = no
	// longer a failed-infra candidate) so the caller does not record a phantom
	// re-placement on a lost race.
	ResetForInfraReplace(ctx context.Context, runID, taskID string) (bool, error)
	// RedispatchReschedule returns a task parked in up_for_reschedule to 'none' for
	// re-dispatch, PRESERVING try_number (reschedule is not a retry; #380).
	RedispatchReschedule(ctx context.Context, runID, taskID string) error
	// RecordDispatchFailure backs off a scheduled task after a synchronous dispatch
	// failure: it increments dispatch_attempts and sets next_dispatch_at so the
	// planner does not re-attempt until the backoff elapses (ADR 0031 Amendment A).
	RecordDispatchFailure(ctx context.Context, runID, taskID string, nextAt time.Time) error
	// RecordDispatchBackpressure backs off a scheduled task after a retriable-
	// forever cluster-backpressure dispatch failure (quota 403 / APF 429): it sets
	// next_dispatch_at WITHOUT incrementing dispatch_attempts, so the task is
	// re-offered once the backoff elapses but never accumulates toward the
	// dispatch_failed cap (ADR 0053).
	RecordDispatchBackpressure(ctx context.Context, runID, taskID string, nextAt time.Time) error
	// FailDispatchExhausted fails a scheduled task as dispatch_failed once its
	// dispatch-attempt budget is spent, so the run finalizes instead of looping.
	FailDispatchExhausted(ctx context.Context, runID, taskID, reason string) error
	SetRunState(ctx context.Context, runID string, state domain.DagRunState) error
	// ClaimAlertAttempt atomically claims ONE on-failure send attempt, reporting
	// true iff this call won it. It refuses when the episode was already
	// delivered, when the attempt budget is spent, or when the backoff has not
	// elapsed — so a dead endpoint stops being retried instead of being hit once
	// per tick for the life of the run (#431, and the delivery split below).
	// It returns the attempt number won, or 0 when the claim was refused.
	ClaimAlertAttempt(ctx context.Context, runID string, maxAttempts int, backoff time.Duration) (int, error)
	// MarkRunAlertDelivered stamps the episode as paged, for the attempt the
	// caller won. Claiming and stamping were once the same operation, which meant
	// every failed send was a page lost with no retry. The attempt is part of the
	// call because the send is detached from the tick: an operator clear can start
	// a new episode mid-send, and a stamp from the superseded one must not land.
	MarkRunAlertDelivered(ctx context.Context, runID string, attempt int) error
	ScheduledDAGs(ctx context.Context) ([]ScheduledDAG, error)
	CreateScheduledRun(ctx context.Context, dagID string, logical time.Time) error
	// SetTaskNote attaches operational context to a task instance (shown in the
	// UI), e.g. why it is queued but not running.
	SetTaskNote(ctx context.Context, runID, taskID, note string) error
}

// Recorder records scheduler metrics. observability.Metrics implements it.
type Recorder interface {
	RecordSchedulerDecision(decisionType string)
	RecordTaskTransition(from, to, dagID string)
	// RecordUndispatchable counts tasks queued with no executor to launch them.
	RecordUndispatchable(reason string)
	// RecordSchedulerStepDown counts a leader step-down by reason — the rate
	// of this counter is the operator-facing SLI for leader-churn (#311). A
	// nil Recorder is tolerated by the scheduler so tests need not stub it.
	RecordSchedulerStepDown(reason string)
	// ObserveSchedulerReacquire records the wall-clock duration of a step-down
	// → re-acquire cycle (#311). Operators alert on P99 to spot churn that
	// starts to delay scheduling latency.
	ObserveSchedulerReacquire(d time.Duration)
	// RecordAlert counts one on-failure alert outcome by dag, channel type, and
	// result. The scheduler records result="dropped" when its dispatch semaphore
	// is saturated (#435); the dispatcher records "sent"/"failed" for deliveries.
	RecordAlert(dagID, channelType, result string)
}

// Dispatcher launches a task instance for execution. The scheduler dispatches a
// task as it becomes queued; the concrete implementation builds the executor
// request and routes it to the right executor.
type Dispatcher interface {
	Dispatch(ctx context.Context, runID, dagID, dagVersionID string, task domain.TaskSpec) (executor.Disposition, error)
}

// maxCatchupSlotsPerTick caps how many missed cron slots are backfilled for
// one DAG on a single scheduler tick (#129). After a leader has been down
// across hundreds of slots, the catchup helper would produce that many runs;
// the cap protects the tick from being stalled by a degenerate inputs and
// the rest are picked up on the next interval (reconciliation-loop semantics,
// ADR 0031).
const maxCatchupSlotsPerTick = 100

// defaultAlertConcurrency caps concurrent on-failure alert dispatches (#424) so a
// mass failure can't spawn an unbounded burst of alert goroutines/POSTs. Each
// dispatch is short (bounded by the notifier's HTTP timeout), so a small pool
// absorbs normal bursts; beyond it, extra alerts are dropped best-effort.
const defaultAlertConcurrency = 8

// Scheduler advances dag runs by applying the planning rules each tick.
type Scheduler struct {
	store       Store
	logger      *slog.Logger
	interval    time.Duration
	stepTimeout time.Duration
	recorder    Recorder
	dispatcher  Dispatcher
	alerter     Alerter
	// alertSem bounds concurrent on-failure alert dispatches (#424): a mass
	// failure must not spawn an unbounded burst of alert goroutines/POSTs. A
	// buffered channel used as a counting semaphore; a saturated slot drops the
	// alert (best-effort) rather than blocking the tick.
	alertSem chan struct{}
	// The execution reapers are NOT driven from the tick. They run from the
	// leader's maintenance loop, after the pod reconciler's sweep, at that loop's
	// cadence: a reaper judging state at 1s that a 30s reconciler had not yet
	// recovered was the race a restart turned into a false failure. The
	// scheduler exposes LeaderSince/IsLeading/SteppingDown for that loop's gate.
	lastTick     atomic.Int64 // unix-nano of the last loop iteration; 0 = not yet ticked
	leading      atomic.Bool  // true only while this instance holds leadership and ticks
	leaderSince  atomic.Int64 // unix-nano when leadership was last acquired; 0 = not leading. Drives the execution reaper's leader-settling gate
	steppingDown atomic.Bool  // true only during a leader step-down — the campaign loop sets it before canceling the run-context so the expected cancel-fanout logs at WARN, not ERROR (#311 tripwire preserved when it's false)
	// warnedSchedules dedupes the "unparseable schedule" warning per DAG (keyed by
	// the offending expression) so a bad cron logs once, not every tick. Accessed
	// only from the single-threaded tick (createDueRuns), so it needs no lock.
	warnedSchedules map[string]string
	// poolsEnabled turns on the cross-DAG named-pool admission gate (ADR 0053
	// Stage 3). Pro-only: main calls EnablePools() only when the edition is "pro".
	// While false (Lite / non-Pro), Step never loads pool budgets and never
	// threads pool occupancy, so PlanRun's pool gate is a no-op and Lite plans
	// byte-identically. Set once at construction (before ticking), read on the
	// single-threaded tick, so it needs no lock.
	poolsEnabled bool
}

// NewScheduler builds a Scheduler over the given store, ticking every interval.
func NewScheduler(store Store, logger *slog.Logger, interval time.Duration) *Scheduler {
	return &Scheduler{
		store:           store,
		logger:          logger,
		interval:        interval,
		stepTimeout:     defaultStepTimeout(interval),
		warnedSchedules: map[string]string{},
		alertSem:        make(chan struct{}, defaultAlertConcurrency),
	}
}

// defaultStepTimeout bounds how long one scheduling tick may run before it is
// canceled so the loop can recover, rather than hang forever on a stuck query.
// It is generous (well above a healthy tick) to avoid aborting legitimate work.
func defaultStepTimeout(interval time.Duration) time.Duration {
	if t := 30 * interval; t > 30*time.Second {
		return t
	}
	return 30 * time.Second
}

// SetStepTimeout overrides the per-tick timeout (optional; mainly for tests).
func (s *Scheduler) SetStepTimeout(d time.Duration) { s.stepTimeout = d }

// SetLeading marks whether this instance currently holds scheduler leadership.
// The leadership manager sets it true while the loop runs and false when it
// steps down (lost lock) or stops. Becoming leader resets the tick clock so the
// startup grace applies afresh and a stale pre-step-down heartbeat is not
// mistaken for a stall. It governs Heartbeat: only a leader is expected to tick.
func (s *Scheduler) SetLeading(on bool) {
	if on {
		s.lastTick.Store(0)
		// Stamp when leadership was acquired so the execution reaper's settling
		// gate measures from now, not from wall-clock. Clear it on loss so a
		// non-leader reports no leadership window.
		s.leaderSince.Store(time.Now().UnixNano())
	} else {
		s.leaderSince.Store(0)
	}
	s.leading.Store(on)
}

// LeaderSince reports when this instance last acquired scheduler leadership, or
// the zero time if it is not currently leading. The agent-lost reaper uses it to
// suppress reaping within a grace window after a (re-)election (#858).
func (s *Scheduler) LeaderSince() time.Time {
	ns := s.leaderSince.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// IsLeading reports whether this instance currently holds scheduler leadership.
// Background sweeps that mutate cluster state — the pod reconciler and the
// staging-volume GC — gate on this so that at replicaCount>1 only the leader
// sweeps; otherwise every replica would reconcile and delete the same pods.
func (s *Scheduler) IsLeading() bool {
	return s.leading.Load()
}

// MarkSteppingDown records that a graceful step-down has begun. The campaign
// loop calls this BEFORE canceling the scheduler's run-context, so any
// in-flight reaper/Step that returns "context canceled" inside the window logs
// at WARN (expected) instead of ERROR. It also increments the step-down
// counter labeled by reason, so operators can alert on the *rate* of churn
// (rate(...[5m])) instead of grep'ing log content. ClearSteppingDown closes
// the window; outside it, context.Canceled stays ERROR — the tripwire that
// catches an unexpected cancel a flat downgrade would silently swallow.
func (s *Scheduler) MarkSteppingDown(reason string) {
	s.steppingDown.Store(true)
	if s.recorder != nil {
		s.recorder.RecordSchedulerStepDown(reason)
	}
}

// ClearSteppingDown ends the step-down window opened by MarkSteppingDown.
// Idempotent — calling it when no step-down is active is a no-op.
func (s *Scheduler) ClearSteppingDown() { s.steppingDown.Store(false) }

// RecordReacquireSince records the time spent stepped down (#311). It is
// called by the campaign loop immediately after a successful re-acquire, with
// the timestamp captured at the moment of step-down. A zero stepDownAt (no
// prior step-down — first acquisition at boot) is ignored.
func (s *Scheduler) RecordReacquireSince(stepDownAt time.Time) {
	if stepDownAt.IsZero() || s.recorder == nil {
		return
	}
	s.recorder.ObserveSchedulerReacquire(time.Since(stepDownAt))
}

// SteppingDown exposes the current step-down state for tests and callers that
// want to classify an error themselves: the scheduler's own log sites pass it to
// logSchedulerError, and the execution reaper receives it (as the inStepDown
// callback) so an expected step-down cancel logs at WARN, not ERROR (#311).
func (s *Scheduler) SteppingDown() bool { return s.steppingDown.Load() }

// SetRecorder attaches a metrics recorder (optional).
func (s *Scheduler) SetRecorder(r Recorder) { s.recorder = r }

// SetDispatcher attaches the executor dispatcher (optional; without it the
// scheduler advances state only and launches nothing).
func (s *Scheduler) SetDispatcher(d Dispatcher) { s.dispatcher = d }

// EnablePools turns on the cross-DAG named-pool admission gate (ADR 0053 Stage
// 3). It is Pro-only: main calls it exactly when the edition is "pro". Left
// unset in Lite/non-Pro, where the pool gate stays a no-op and the tick never
// queries pool budgets, so Lite plans byte-identically. Call once before the
// scheduler starts ticking.
func (s *Scheduler) EnablePools() { s.poolsEnabled = true }

// SetAlerter attaches the on-failure alerter (optional; #424). Without it, or
// for a DAG with no alert rules, the scheduler finalizes failures silently.
func (s *Scheduler) SetAlerter(a Alerter) { s.alerter = a }

// SetAlertConcurrency caps how many on-failure alert dispatches may run at once
// (#424). n < 1 is treated as 1. Mainly a config/test seam; the default is
// defaultAlertConcurrency.
func (s *Scheduler) SetAlertConcurrency(n int) {
	if n < 1 {
		n = 1
	}
	s.alertSem = make(chan struct{}, n)
}

// Alerter dispatches a DAG's on-failure alert rules for a run that finalized in
// the failed state. Implementations resolve each rule's managed connection to an
// endpoint and send (Slack/webhook). The scheduler calls it from a detached
// goroutine, so an implementation may block on network I/O without stalling the
// tick; it MUST treat every send as best-effort — a delivery failure is logged,
// never propagated, so alerting can never fail a run.
type Alerter interface {
	// AlertRunFailed sends every on_failure rule and reports whether all of them
	// were delivered. The caller stamps delivery only on true, so a failed send
	// stays retryable instead of being marked and lost.
	AlertRunFailed(ctx context.Context, run RunState) bool
}

// Run drives the scheduling loop until ctx is canceled. The loop is crash-proof:
// a panic or error in a tick is recovered and logged, so the scheduler keeps
// ticking — it may fall behind, but it never dies (the critical invariant).
func (s *Scheduler) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

// tick runs one Step under a timeout, recovering any panic so a single bad tick
// can never crash the process or stop the loop. It is the top-level backstop;
// per-run isolation in Step quarantines individual poison runs underneath it.
func (s *Scheduler) tick(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("scheduler tick panic recovered", "panic", r, "stack", string(debug.Stack()))
			s.record("panic")
		}
	}()
	stepCtx, cancel := context.WithTimeout(ctx, s.stepTimeout)
	defer cancel()
	if err := s.Step(stepCtx); err != nil {
		logSchedulerError(s.logger, "scheduler step", err, s.steppingDown.Load())
	}
}

// record reports a scheduler decision metric, ignoring a nil recorder.
func (s *Scheduler) record(decision string) {
	if s.recorder != nil {
		s.recorder.RecordSchedulerDecision(decision)
	}
}

// Heartbeat reports whether the scheduling loop is live and when it last ticked.
// Only a leader is expected to tick, so a non-leader (a follower, or an instance
// that stepped down after losing the lock) reports healthy without ticking — it
// is correctly idle, not stalled. A leader is healthy during the startup grace
// (before its first tick) and while ticks stay within a small multiple of the
// loop interval; a stalled leader goes unhealthy so the UI/monitor surfaces it.
func (s *Scheduler) Heartbeat() (bool, time.Time) {
	if !s.leading.Load() {
		return true, time.Now().UTC()
	}
	nanos := s.lastTick.Load()
	if nanos == 0 {
		return true, time.Now().UTC()
	}
	last := time.Unix(0, nanos).UTC()
	return time.Since(last) <= 3*s.interval+time.Second, last
}

// Step runs one deterministic scheduling iteration over every active run. Each
// run is advanced in isolation (see advanceSafely): a panic or error in one
// run is contained, so it never blocks the other runs or new-run creation.
// The reaper runs independently of createDueRuns success — they share no
// dependency, and silencing the reaper when scheduling has a hiccup would let
// orphans accumulate exactly when the operator is most likely to notice the
// counter is wrong. The first non-nil infra-level error is returned (logged
// by the caller); the later phases still execute.
func (s *Scheduler) Step(ctx context.Context) error {
	s.lastTick.Store(time.Now().UnixNano())
	// Single-writer invariant (ADR 0031 / issue #208): a follower MUST NOT
	// read or write scheduler state. The lastTick.Store above keeps the
	// follower's heartbeat live so the orchestrator can prove the instance
	// is alive without granting it the writer role. Lifting the gate here
	// (instead of only around individual phases) removes the wasted reads,
	// the duplicate ApplyTransition / CreateScheduledRun attempts that ON
	// CONFLICT used to swallow, and the "what does a follower's count drift
	// mean?" puzzle.
	if !s.leading.Load() {
		return nil
	}
	runs, err := s.store.ActiveRuns(ctx)
	if err != nil {
		return fmt.Errorf("listing active runs: %w", err)
	}
	activeByDAG := make(map[string]int, len(runs))
	// Per-DAG task-admission budget (max_active_tasks, ADR 0053 Stage 1).
	// activeTasksByDAG is the snapshot of currently non-terminal (queued+running)
	// TIs per DAG; admittedTasksByDAG folds in what earlier sibling runs already
	// promoted this tick so a single tick cannot breach the cap across runs —
	// mirroring how createDueRuns folds justCreated into the max_active_runs cap.
	activeTasksByDAG := activeTaskCounts(runs)
	admittedTasksByDAG := make(map[string]int, len(runs))
	// Cross-DAG named-pool budget (ADR 0053 Stage 3, Pro only). Loaded once per
	// tick; poolOccupied starts at the current cross-DAG occupancy and each run's
	// admissions fold back in — the same within-tick threading as the per-DAG cap,
	// generalized to a per-pool map. Left nil on the Lite path, where no pool query
	// runs and the pool gate is a no-op.
	poolBudgets, poolOccupied, err := s.loadPoolBudget(ctx, runs)
	if err != nil {
		return err
	}
	for i := range runs {
		run := runs[i]
		activeByDAG[run.DagID]++
		run.ActiveTaskCount = activeTasksByDAG[run.DagID] + admittedTasksByDAG[run.DagID]
		run.PoolsEnabled = s.poolsEnabled
		run.PoolBudgets = poolBudgets
		run.PoolActive = poolOccupied
		admitted, admittedByPool := s.advanceSafely(ctx, run)
		admittedTasksByDAG[run.DagID] += admitted
		for k, n := range admittedByPool {
			poolOccupied[k] += n
		}
	}
	return s.createDueRuns(ctx, activeByDAG)
}

// activeTaskCounts tallies, per DAG, the task instances that already occupy a
// max_active_tasks slot — those in a non-terminal active state (queued or
// running) across every active run of the DAG. It is the snapshot the admission
// gate subtracts from max_active_tasks to size per-run headroom (ADR 0053 Stage
// 1). Reuses the runs Step already loaded, so it adds no per-tick query.
func activeTaskCounts(runs []RunState) map[string]int {
	counts := make(map[string]int, len(runs))
	for i := range runs {
		for _, st := range runs[i].States {
			if st == domain.TaskStateQueued || st == domain.TaskStateRunning {
				counts[runs[i].DagID]++
			}
		}
	}
	return counts
}

// loadPoolBudget prepares the cross-DAG named-pool admission state for a tick
// (ADR 0053 Stage 3): the per-pool slot caps (queried once) and the current
// per-pool occupancy (derived from the runs already loaded). It is a strict
// no-op on the Lite/non-Pro path — it returns nil maps and issues NO pool query
// — so a Lite tick is byte-identical to the pre-pool behavior. A query error is
// returned so the tick surfaces it rather than silently disabling the gate.
func (s *Scheduler) loadPoolBudget(ctx context.Context, runs []RunState) (budgets, occupied map[string]int, err error) {
	if !s.poolsEnabled {
		return nil, nil, nil
	}
	budgets, err = s.store.PoolBudgets(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("loading pool budgets: %w", err)
	}
	return budgets, activePoolCounts(runs), nil
}

// activePoolCounts tallies, per pool (keyed by PoolKey), the task instances that
// already occupy a slot — those queued or running across every active run,
// cross-DAG (ADR 0053 Stage 3). A task instance's pool is its spec pool, or the
// implicit default pool. Reuses the runs Step already loaded, so it adds no
// per-tick query. Only built on the Pro path (see loadPoolBudget).
func activePoolCounts(runs []RunState) map[string]int {
	counts := make(map[string]int, len(runs))
	for i := range runs {
		for _, t := range runs[i].Tasks {
			if st := runs[i].States[t.TaskID]; st == domain.TaskStateQueued || st == domain.TaskStateRunning {
				counts[PoolKey(runs[i].TenantID, resolvePool(t.Pool))]++
			}
		}
	}
	return counts
}

// advanceSafely advances one run, isolating it: a panic or error in a single run
// is recovered, logged, and metered, but never aborts the tick. This keeps one
// poison run (a malformed spec, a panicking dispatcher, a transient per-run DB
// error) from stalling every other run or crashing the process — the scheduler
// may fall behind on that run, but it stays alive and keeps the rest moving. It
// returns how many tasks it admitted to queued (zero on a panic or error), and
// the per-pool breakdown of those admissions (nil unless the pool gate is on),
// which the caller folds into the per-DAG max_active_tasks and per-pool budgets
// for sibling runs.
func (s *Scheduler) advanceSafely(ctx context.Context, run RunState) (admitted int, admittedByPool map[string]int) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("scheduler run panic recovered",
				"run", run.RunID, "dag", run.DagID, "panic", r, "stack", string(debug.Stack()))
			s.record("panic")
			admitted, admittedByPool = 0, nil
		}
	}()
	admitted, admittedByPool, err := s.advance(ctx, run)
	if err != nil {
		s.logger.Error("advancing run", "run", run.RunID, "dag", run.DagID, "error", err)
		s.record("run_error")
		return 0, nil
	}
	return admitted, admittedByPool
}

// createDueRuns creates a new run for each scheduled DAG whose next cron slot
// after its latest run has arrived. activeByDAG carries the count of already-
// active runs per DAG so the per-DAG max_active_runs cap is honored (#200): a
// DAG that has reached its cap is skipped this tick, and a backfill that would
// exceed the cap is truncated to the remaining headroom. The local
// `createdThisTick` map folds creations made in this same tick into the cap
// so a single tick cannot itself breach the limit.
func (s *Scheduler) createDueRuns(ctx context.Context, activeByDAG map[string]int) error {
	dags, err := s.store.ScheduledDAGs(ctx)
	if err != nil {
		return fmt.Errorf("listing scheduled dags: %w", err)
	}
	now := time.Now().UTC()
	createdThisTick := make(map[string]int, len(dags))
	for _, d := range dags {
		if domain.IsOnceSchedule(d.Schedule) {
			// @once: fire exactly one run on first sight, then never again. Once
			// the run exists, the DAG's LastLogical is non-nil and this is
			// skipped — that single-shot semantic already prevents any cap
			// breach, so no headroom check is needed here.
			if d.LastLogical == nil {
				s.createScheduledRun(ctx, d.DagID, now)
				createdThisTick[d.DagID]++
			}
			continue
		}
		if domain.IsCronlessSchedule(d.Schedule) {
			// Manual-only or @continuous: nothing to cron-schedule — skip quietly.
			continue
		}
		// A non-empty but unparseable schedule (a 4-field cron, a typo) would
		// otherwise be swallowed silently: nextScheduledRun returns "not due" and
		// the DAG never runs, with nothing logged. Surface it (once per bad
		// expression) so the operator sees why a scheduled DAG sits idle. Compile
		// validation (domain.ValidateSchedule) catches this earlier; this is the
		// backstop for DAGs registered before the fix.
		if !scheduleParseable(d.Schedule) {
			if s.warnedSchedules[d.DagID] != d.Schedule {
				s.logger.Warn("DAG has an unparseable cron schedule; it will not run on a schedule until fixed",
					"dag", d.DagID, "schedule", d.Schedule)
				s.warnedSchedules[d.DagID] = d.Schedule
			}
			continue
		}
		// First-run with no start_date keeps the legacy single-slot semantics
		// (most recent slot at or before now) — backfilling unbounded history
		// for a fresh DAG would be unsafe by default. The catchup helper opts
		// in only when there is either a last_logical or a start_date floor.
		if d.LastLogical == nil && d.StartDate == nil {
			logical, due := nextScheduledRun(d.Schedule, d.LastLogical, now)
			if !due {
				continue
			}
			if !s.hasHeadroom(d, activeByDAG, createdThisTick) {
				s.recordCapSkip(d.DagID)
				continue
			}
			s.createScheduledRun(ctx, d.DagID, logical)
			createdThisTick[d.DagID]++
			continue
		}
		slots := dueScheduledSlots(d.Schedule, d.LastLogical, d.StartDate, now, d.Catchup, maxCatchupSlotsPerTick)
		for _, logical := range slots {
			if !s.hasHeadroom(d, activeByDAG, createdThisTick) {
				s.recordCapSkip(d.DagID)
				break
			}
			s.createScheduledRun(ctx, d.DagID, logical)
			createdThisTick[d.DagID]++
		}
	}
	return nil
}

// hasHeadroom reports whether the DAG may take another active run without
// exceeding its max_active_runs cap. A non-positive cap is treated as
// "unlimited" — a defensive guard for hand-edited rows: the column is
// `NOT NULL DEFAULT 16` (migration 002) and the repository upsert defaults
// missing values to 16 (`repository.go:509`), so a zero only appears via a
// direct DB write. Diverging from Airflow's "missing == default 16" makes
// the scheduler fail open rather than locking the DAG out forever when a
// bad row is encountered. Callers pass `justCreated` so a single tick
// folds in its own creations and cannot itself breach the cap.
func (s *Scheduler) hasHeadroom(d ScheduledDAG, active map[string]int, justCreated map[string]int) bool {
	if d.MaxActiveRuns <= 0 {
		return true
	}
	return active[d.DagID]+justCreated[d.DagID] < d.MaxActiveRuns
}

// recordCapSkip logs (once per DAG between successful creations) and meters
// that a due slot was skipped because the DAG is at its max_active_runs cap.
// We use a single metric label so dashboards can see "is concurrency the
// bottleneck right now?" without per-DAG cardinality.
func (s *Scheduler) recordCapSkip(dagID string) {
	s.logger.Debug("skipping due run; DAG is at max_active_runs cap", "dag", dagID)
	s.record("max_active_runs_cap")
}

// createScheduledRun creates one scheduled run for a DAG, isolating per-DAG
// failures: a single DAG's creation error is logged and metered but never blocks
// run creation for the other scheduled DAGs in this tick.
func (s *Scheduler) createScheduledRun(ctx context.Context, dagID string, logical time.Time) {
	if err := s.store.CreateScheduledRun(ctx, dagID, logical); err != nil {
		s.logger.Error("creating scheduled run", "dag", dagID, "error", err)
		s.record("create_run_error")
		return
	}
	s.record("create_run")
}

// advance plans and applies one run's transitions, returning how many tasks it
// promoted to queued this tick (the per-DAG max_active_tasks charge, ADR 0053
// Stage 1) and the per-pool breakdown of those promotions (the cross-DAG pool
// charge, Stage 3; nil when the pool gate is off). The caller folds both into
// the sibling runs' budgets so a single tick cannot breach either cap.
func (s *Scheduler) advance(ctx context.Context, run RunState) (admitted int, admittedByPool map[string]int, err error) {
	// Materialize task instances on first sight of a queued run, then start it.
	if run.State == domain.DagRunStateQueued && len(run.States) == 0 {
		if err = s.store.MaterializeTasks(ctx, run.RunID, run.Tasks); err != nil {
			return 0, nil, fmt.Errorf("materializing tasks: %w", err)
		}
		if err = s.store.SetRunState(ctx, run.RunID, domain.DagRunStateRunning); err != nil {
			return 0, nil, fmt.Errorf("starting run: %w", err)
		}
		return 0, nil, nil
	}
	poolOf := taskPools(run) // taskID → pool key; nil when the pool gate is off.
	// Plain state-set transitions (no side effect beyond the write + metric) are
	// collected and flushed grouped by target state in one UPDATE each, instead of
	// one per task. The queued (dispatch) and none (guarded reset) rails keep their
	// per-task paths inside applyPlanned. Deferring the plain writes to after the
	// loop is safe: each targets a distinct row, FinalizeRun reads the in-memory
	// run (not the DB), and dispatch depends on task specs, not sibling TI state —
	// so the per-tick effect is byte-identical, only the statement count drops.
	batch := newTransitionBatch()
	for _, t := range PlanRun(run) {
		if t.To == domain.TaskStateQueued {
			admitted++
			if poolOf != nil {
				if admittedByPool == nil {
					admittedByPool = map[string]int{}
				}
				admittedByPool[poolOf[t.TaskID]]++
			}
		}
		if aerr := s.applyPlanned(ctx, run, t, batch); aerr != nil {
			return admitted, admittedByPool, aerr
		}
	}
	if ferr := s.flushTransitions(ctx, run, batch); ferr != nil {
		return admitted, admittedByPool, ferr
	}
	if state, done := FinalizeRun(run); done {
		if err = s.store.SetRunState(ctx, run.RunID, state); err != nil {
			return admitted, admittedByPool, fmt.Errorf("finalizing run: %w", err)
		}
		s.maybeAlertFailure(ctx, state, run)
	}
	return admitted, admittedByPool, nil
}

// taskPools maps each of a run's task IDs to its pool budget key (ADR 0053 Stage
// 3), applying the implicit-default-pool fallback. It returns nil when the pool
// gate is off (Lite / non-Pro), so advance does no per-pool bookkeeping there.
func taskPools(run RunState) map[string]string {
	if !run.PoolsEnabled {
		return nil
	}
	m := make(map[string]string, len(run.Tasks))
	for _, t := range run.Tasks {
		m[t.TaskID] = PoolKey(run.TenantID, resolvePool(t.Pool))
	}
	return m
}

// alertMaxAttempts bounds how many times one failure episode may be sent before
// Dexaflow gives up. A failed run stays failed forever, so without a ceiling a dead
// alert endpoint is retried once per scheduler tick for the life of the run — the
// alert path would DoS the very endpoint it is trying to reach. Five attempts
// spread over the backoff below survives a restart or a brief outage of the
// channel without becoming a hammer.
const alertMaxAttempts = 5

// alertRetryBackoff is the wait between attempts for one episode. Deliberately far
// longer than the scheduler tick (1s by default): the retry exists to ride out a
// transient channel outage, and an outage of the alert channel is frequently
// correlated with the incident being reported, so retrying hard makes both worse.
const alertRetryBackoff = 2 * time.Minute

// alertAttemptUnknown is the attempt number used when the claim itself errored
// and we alert anyway (fail open). It is deliberately outside the real attempt
// range so the delivery stamp, which is guarded on the attempt, matches no row —
// the page goes out, but a bookkeeping write derived from an unknown attempt
// never overwrites state we cannot reason about.
const alertAttemptUnknown = -1

// maybeAlertFailure fires the DAG's on-failure alert rules when a run finalizes
// failed. It runs in a detached goroutine (WithoutCancel) so a slow endpoint
// never stalls the tick, and the alerter's own best-effort contract means a
// delivery failure cannot fail the run. A nil alerter or a DAG without rules is
// a no-op.
func (s *Scheduler) maybeAlertFailure(ctx context.Context, state domain.DagRunState, run RunState) {
	if state != domain.DagRunStateFailed || s.alerter == nil {
		return
	}
	if run.Alerts == nil || len(run.Alerts.OnFailure) == 0 {
		return
	}
	// Claim one ATTEMPT for this failure episode. The claim no longer means
	// "paged" — it means "we are about to try" — so a send that fails leaves the
	// episode claimable and the next tick retries it after the backoff. Refused
	// when already delivered, out of budget, or still backing off. Fail OPEN on a
	// store error: a missed page is worse than a rare duplicate.
	attempt, err := s.store.ClaimAlertAttempt(ctx, run.RunID, alertMaxAttempts, alertRetryBackoff)
	if err != nil {
		s.logger.Error("claiming on-failure alert attempt (alerting anyway)",
			"dag", run.DagID, "run", run.RunID, "error", err)
		attempt = alertAttemptUnknown
	} else if attempt == 0 {
		return
	}
	// The last attempt is the one nobody hears about if it fails, so say so while
	// it is still happening. Without this, a run whose every attempt failed is
	// indistinguishable in the database from one that has not been tried — the
	// alerting system failing silently, which is the same shape as the failure it
	// exists to report.
	if attempt >= alertMaxAttempts {
		s.logger.Warn("final on-failure alert attempt for this run; no further retries after this one",
			"dag", run.DagID, "run", run.RunID, "attempt", attempt, "max", alertMaxAttempts)
	}
	// Acquire a dispatch slot without blocking the tick. A saturated semaphore
	// means a burst of failures is already sending; dropping this one (with a
	// warning) is the best-effort trade-off that protects the scheduler.
	select {
	case s.alertSem <- struct{}{}:
		go func() {
			defer func() { <-s.alertSem }()
			// Stamp delivery only when every rule landed. On anything less the
			// episode stays unstamped, so the next tick retries it once the
			// backoff elapses — until the attempt budget runs out.
			detached := context.WithoutCancel(ctx)
			if !s.alerter.AlertRunFailed(detached, run) {
				if attempt >= alertMaxAttempts {
					// Terminal: the budget is spent and nothing got through. This is
					// the line an operator needs when asking "why was I never paged",
					// so it says exactly that rather than reporting a failed send.
					s.logger.Error("on-failure alert GAVE UP: no attempt was delivered, nobody was paged",
						"dag", run.DagID, "run", run.RunID, "attempts", attempt)
					return
				}
				s.logger.Warn("on-failure alert not fully delivered; will retry after backoff",
					"dag", run.DagID, "run", run.RunID,
					"attempt", attempt, "max", alertMaxAttempts, "backoff", alertRetryBackoff)
				return
			}
			if err := s.store.MarkRunAlertDelivered(detached, run.RunID, attempt); err != nil {
				// The page went out; failing to record it costs a duplicate on the
				// next tick, which is the trade-off this path already prefers.
				s.logger.Error("recording on-failure alert delivery",
					"dag", run.DagID, "run", run.RunID, "error", err)
			}
		}()
	default:
		s.logger.Warn("dropping on-failure alert: dispatch saturated",
			"dag", run.DagID, "run", run.RunID, "limit", cap(s.alertSem))
		// Record the drop per rule (mirrors the dispatcher's per-rule sent/failed),
		// so a burst of saturation-drops is a metric operators can alert on, not
		// just a log line (#435). Best-effort: a nil recorder is a no-op.
		if s.recorder != nil {
			for _, rule := range run.Alerts.OnFailure {
				s.recorder.RecordAlert(run.DagID, rule.Type, alertResultDropped)
			}
		}
	}
}

// alertResultDropped is the RecordAlert result for an on-failure alert the
// scheduler drops because its dispatch semaphore is saturated (#435). It sits
// alongside the dispatcher's "sent"/"failed" on the same metric.
const alertResultDropped = "dropped"

// applyPlanned routes one planned transition. The queued rail launches the task
// (a dispatch side effect) and the none rail runs the guarded retry/reschedule/
// infra-replace resets — both stay per-task, applied in plan order. Every other
// transition is a plain state set with no side effect beyond the write and its
// metric, so it is collected into the batch and flushed grouped by target state
// after the loop.
func (s *Scheduler) applyPlanned(ctx context.Context, run RunState, t PlannedTransition, batch *transitionBatch) error {
	switch t.To {
	case domain.TaskStateQueued:
		return s.launchQueued(ctx, run, t)
	case domain.TaskStateNone:
		// A → none transition is a retry release (bump try_number), a reschedule
		// re-dispatch (preserve try_number), or an infra re-placement (preserve
		// try_number, bump infra_attempts) — keyed on the from-state.
		if run.States[t.TaskID] == domain.TaskStateUpForReschedule {
			return s.redispatchReschedule(ctx, run, t.TaskID)
		}
		if run.States[t.TaskID] == domain.TaskStateFailed && run.InfraFailed[t.TaskID] {
			return s.resetForInfraReplace(ctx, run, t.TaskID)
		}
		return s.resetForRetry(ctx, run, t.TaskID)
	default:
		batch.add(t.To, t.TaskID)
		return nil
	}
}

// transitionBatch groups a tick's plain state-set transitions by target state so
// each group can be flushed in one UPDATE (SchedulerStore.ApplyTransitions)
// rather than one per task. It carries only side-effect-free transitions; the
// queued (dispatch) and none (guarded reset) rails never enter it.
type transitionBatch struct {
	// order lists target states in first-seen order so the flush — and the
	// metrics it emits — is deterministic regardless of Go's map iteration.
	order   []domain.TaskState
	byState map[domain.TaskState][]string
}

// newTransitionBatch builds an empty batch.
func newTransitionBatch() *transitionBatch {
	return &transitionBatch{byState: make(map[domain.TaskState][]string)}
}

// add records that taskID transitions to the given target state this tick.
func (b *transitionBatch) add(to domain.TaskState, taskID string) {
	if _, seen := b.byState[to]; !seen {
		b.order = append(b.order, to)
	}
	b.byState[to] = append(b.byState[to], taskID)
}

// flushTransitions applies each collected target-state group in one UPDATE, then
// records the per-task metrics recordTransition would have. Metrics are emitted
// only after the group's write succeeds — the same "record nothing we did not
// persist" contract the per-task path had — and per-task counts are identical to
// the unbatched path (metric ordering is not an observable output).
func (s *Scheduler) flushTransitions(ctx context.Context, run RunState, b *transitionBatch) error {
	for _, to := range b.order {
		taskIDs := b.byState[to]
		if err := s.store.ApplyTransitions(ctx, run.RunID, taskIDs, to); err != nil {
			return fmt.Errorf("applying %s transitions: %w", to, err)
		}
		if s.recorder != nil {
			for _, taskID := range taskIDs {
				s.recorder.RecordSchedulerDecision(string(to))
				s.recorder.RecordTaskTransition(string(run.States[taskID]), string(to), run.DagID)
			}
		}
	}
	return nil
}

// resetForRetry returns a task to 'none' with an incremented try number so the
// next tick re-evaluates and re-runs it.
func (s *Scheduler) resetForRetry(ctx context.Context, run RunState, taskID string) error {
	applied, err := s.store.ResetForRetry(ctx, run.RunID, taskID)
	if err != nil {
		return fmt.Errorf("resetting %s for retry: %w", taskID, err)
	}
	// The guarded reset no-ops when the TI is no longer up_for_retry — a stale
	// decision that raced a concurrent writer (e.g. a re-dispatch). Do not record
	// a retry we did not perform; surface the lost race instead.
	if !applied {
		if s.recorder != nil {
			s.recorder.RecordSchedulerDecision("retry_noop")
		}
		return nil
	}
	if s.recorder != nil {
		s.recorder.RecordSchedulerDecision("retry")
		s.recorder.RecordTaskTransition(string(run.States[taskID]), string(domain.TaskStateNone), run.DagID)
	}
	return nil
}

// resetForInfraReplace returns a task a reaper failed as infra to 'none' for a
// re-run, preserving try_number and bumping infra_attempts — an infrastructure
// fault (agent/pod/dispatch lost) is not a task failure and must not consume the
// user's retry budget (ADR 0051 Phase 1).
func (s *Scheduler) resetForInfraReplace(ctx context.Context, run RunState, taskID string) error {
	applied, err := s.store.ResetForInfraReplace(ctx, run.RunID, taskID)
	if err != nil {
		return fmt.Errorf("re-placing infra-failed %s: %w", taskID, err)
	}
	// The guarded reset no-ops when the TI is no longer a failed-infra candidate
	// (a late terminal report, or an app failure raced the reap). Do not record a
	// re-placement we did not perform; surface the lost race instead.
	if !applied {
		if s.recorder != nil {
			s.recorder.RecordSchedulerDecision("infra_replace_noop")
		}
		return nil
	}
	if s.recorder != nil {
		s.recorder.RecordSchedulerDecision("infra_replace")
		s.recorder.RecordTaskTransition(string(run.States[taskID]), string(domain.TaskStateNone), run.DagID)
	}
	return nil
}

// redispatchReschedule returns a reschedule-mode sensor to 'none' for re-dispatch
// once its reschedule_at has passed, preserving try_number (no attempt consumed).
func (s *Scheduler) redispatchReschedule(ctx context.Context, run RunState, taskID string) error {
	if err := s.store.RedispatchReschedule(ctx, run.RunID, taskID); err != nil {
		return fmt.Errorf("re-dispatching rescheduled %s: %w", taskID, err)
	}
	if s.recorder != nil {
		s.recorder.RecordSchedulerDecision("reschedule")
		s.recorder.RecordTaskTransition(string(run.States[taskID]), string(domain.TaskStateNone), run.DagID)
	}
	return nil
}

// launchQueued routes a queued task to the dispatcher (pod path), recording the
// appropriate transition. A transient failure leaves the task scheduled so the
// next tick retries.
func (s *Scheduler) launchQueued(ctx context.Context, run RunState, t PlannedTransition) error {
	task, ok := findTask(run.Tasks, t.TaskID)
	if !ok {
		return fmt.Errorf("task %s not found in run %s", t.TaskID, run.RunID)
	}
	if s.dispatcher != nil {
		disp, err := s.dispatcher.Dispatch(ctx, run.RunID, run.DagID, run.DagVersionID, task)
		if err != nil {
			return s.handleDispatchFailure(ctx, run, t.TaskID, disp, err)
		}
		return s.recordTransition(ctx, run, t.TaskID, domain.TaskStateQueued)
	}
	return s.failUndispatchable(ctx, run, t.TaskID, task.Type)
}

// failUndispatchable fails a task that has no executor to run it (pod dispatch
// disabled). The condition is deterministic for the process lifetime, so failing
// fast — with the reason on the task note for the UI — beats leaving the run
// "running" forever (#46, #50).
// handleDispatchFailure records a synchronous dispatch failure: it backs the task
// off for a growing interval, or fails it as dispatch_failed once the attempt
// budget is spent (ADR 0031 Amendment A). It returns nil in both cases — the
// failure is recorded in the DB, not propagated, so one task's dispatch trouble
// does not abort the tick. A dispatch failure is infrastructure, not a task
// failure, so it never consumes the task's try_number.
//
// A Deferred dispatch (the buffered dispatch queue was full) is split out first:
// nothing was attempted, so nothing is recorded and the next tick re-offers it.
// Cluster backpressure (a ResourceQuota 403 or an APF 429) is split out next
// (ADR 0053): it is retriable-forever, so it is backed off WITHOUT touching the
// dispatch-attempt counter and can never reach the dispatch_failed give-up below.
// Dexaflow holds the task and re-offers it until the cluster has room, rather than
// failing the user's task because the cluster asked it to slow down. The
// disposition that split is decided on is classified on the execution layer and
// arrives typed over the seam (ADR 0051 Phase 4), so the scheduler never inspects
// Kubernetes error types itself.
func (s *Scheduler) handleDispatchFailure(ctx context.Context, run RunState, taskID string, disp executor.Disposition, cause error) error {
	if disp == executor.Deferred {
		// The dispatch never left the control plane (the buffered dispatch queue
		// is full or draining). Leave the task scheduled and write nothing: the
		// next tick re-offers it, and local backpressure must never spend the
		// dispatch-attempt budget or drive the task to dispatch_failed.
		s.logger.Debug("dispatch deferred; buffer at capacity, re-offering next tick",
			"run", run.RunID, "task", taskID, "error", cause)
		return nil
	}
	if disp == executor.Backpressure {
		return s.backoffBackpressure(ctx, run, taskID, cause)
	}
	attempts := run.DispatchAttempts[taskID] + 1
	if attempts >= dispatchMaxAttempts {
		reason := fmt.Sprintf("dispatch_failed after %d attempts: %v", attempts, cause)
		s.logger.Error("dispatch attempts exhausted; failing task",
			"run", run.RunID, "task", taskID, "attempts", attempts, "error", cause)
		if err := s.store.FailDispatchExhausted(ctx, run.RunID, taskID, reason); err != nil {
			s.logger.Error("failing dispatch-exhausted task", "run", run.RunID, "task", taskID, "error", err)
		}
		return nil
	}
	now := run.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	nextAt := now.Add(dispatchBackoff(attempts))
	s.logger.Warn("dispatch failed; backing off before re-attempt",
		"run", run.RunID, "task", taskID, "attempt", attempts, "next_dispatch_at", nextAt, "error", cause)
	if err := s.store.RecordDispatchFailure(ctx, run.RunID, taskID, nextAt); err != nil {
		s.logger.Error("recording dispatch failure", "run", run.RunID, "task", taskID, "error", err)
	}
	return nil
}

// backoffBackpressure handles a retriable-forever dispatch failure caused by
// cluster backpressure (ADR 0053). It sets next_dispatch_at so the planner holds
// the task `scheduled` and re-offers it once the backoff elapses, but it does NOT
// increment dispatch_attempts — backpressure is a temporary "no room," not a
// poison placement, so it must never accumulate toward the dispatch_failed cap.
//
// The backoff reuses dispatchBackoff over the task's EXISTING (un-incremented)
// attempt count: a task hitting only backpressure keeps a steady base-interval
// re-offer cadence rather than a growing one — the right trade-off for a
// condition expected to clear, since it recovers fast when the cluster frees room
// without hammering the apiserver every tick, and without inflating a counter
// that could later fail the task. Returns nil: like every dispatch outcome, the
// result is recorded, never propagated, so one task's backpressure cannot abort
// the tick.
func (s *Scheduler) backoffBackpressure(ctx context.Context, run RunState, taskID string, cause error) error {
	now := run.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	nextAt := now.Add(dispatchBackoff(run.DispatchAttempts[taskID] + 1))
	s.logger.Warn("dispatch hit cluster backpressure; backing off and re-offering (not counted against dispatch budget)",
		"run", run.RunID, "task", taskID, "next_dispatch_at", nextAt, "error", cause)
	if err := s.store.RecordDispatchBackpressure(ctx, run.RunID, taskID, nextAt); err != nil {
		s.logger.Error("recording dispatch backpressure", "run", run.RunID, "task", taskID, "error", err)
	}
	return nil
}

func (s *Scheduler) failUndispatchable(ctx context.Context, run RunState, taskID string, taskType domain.TaskType) error {
	s.logger.Warn("task has no available executor; failing it (it can never run)",
		"run", run.RunID, "dag", run.DagID, "task", taskID, "task_type", taskType,
		"reason", "no_executor",
		"hint", "pod dispatch disabled (no kubeconfig) or no executor handles this task type")
	if s.recorder != nil {
		s.recorder.RecordUndispatchable("no_executor")
	}
	note := fmt.Sprintf("Failed: no executor available for a %q task. "+
		"Pod dispatch is disabled (no Kubernetes config).", taskType)
	if nerr := s.store.SetTaskNote(ctx, run.RunID, taskID, note); nerr != nil {
		s.logger.Warn("setting task note", "run", run.RunID, "task", taskID, "error", nerr)
	}
	return s.recordTransition(ctx, run, taskID, domain.TaskStateFailed)
}

// recordTransition persists a task transition and records its metrics.
func (s *Scheduler) recordTransition(ctx context.Context, run RunState, taskID string, to domain.TaskState) error {
	from := run.States[taskID]
	if err := s.store.ApplyTransition(ctx, run.RunID, taskID, to); err != nil {
		return fmt.Errorf("applying transition for %s: %w", taskID, err)
	}
	if s.recorder != nil {
		s.recorder.RecordSchedulerDecision(string(to))
		s.recorder.RecordTaskTransition(string(from), string(to), run.DagID)
	}
	return nil
}

// findTask returns the task with the given ID from the run topology.
func findTask(tasks []domain.TaskSpec, taskID string) (domain.TaskSpec, bool) {
	for _, task := range tasks {
		if task.TaskID == taskID {
			return task, true
		}
	}
	return domain.TaskSpec{}, false
}
