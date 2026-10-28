package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sort"
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
// canceled the loop, a bug, a misconfig, a shutdown race), we KEEP it at
// ERROR. The tripwire is what catches an unexpected cancel that the downgrade
// would otherwise have silently swallowed. DeadlineExceeded is also kept at
// ERROR, a deadline IS a real stall worth seeing, not a deliberate cancel.
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
	// DisplayRunID is the dag_runs.run_id column, the identifier an operator
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
	// Held reports that the run's DAG is paused. A held run that is still
	// queued waits: the tick does not start it, materialize its task instances
	// or dispatch anything for it until the DAG is unpaused, as Airflow 3 does
	// (#1705). A held run that is already running advances as usual, so the
	// tasks it started finish. Either way a held run stays in the tick's
	// snapshot: it counts toward max_active_runs, and its queued and running
	// task instances count toward max_active_tasks and the pool caps (ADR
	// 0053, ADR 0066). A clear on a running run puts it back in queued while
	// other tasks of it may still run, so a queued held run can hold slots.
	Held bool
	// Suspension is the suspension mode of the run's tenant, empty when it is
	// not suspended (#2181). Under drain or stop the tick dispatches nothing
	// of the run and does not start it while it is queued; under stop it also
	// fails the run's queued and running task instances (stopSuspended).
	Suspension domain.SuspensionMode
	// MaxActiveRuns is the DAG's max_active_runs, read from its dags row (the
	// same value run creation caps on). The tick starts a queued run only
	// while fewer than this many runs of the DAG are running, oldest logical
	// date first, as Airflow's _start_queued_dagruns does (#1766). Zero or
	// negative means unlimited.
	MaxActiveRuns int
	Tasks         []domain.TaskSpec
	// Graph is the task index for Tasks, built once per dag_version by the store
	// and shared read-only by every run of that version. Nil means "build it
	// when needed", so callers that construct a RunState by hand need not set it.
	Graph  *TaskGraph
	States map[string]domain.TaskState
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
	// reschedule_at, without consuming retry budget (#380). Absent/zero entries
	// re-dispatch immediately (preserves the test seam, like EndedAt).
	RescheduleAt map[string]*time.Time
	// NextDispatchAt holds the earliest time a `scheduled` task may be dispatched
	// again after a synchronous dispatch failure (ADR 0031 Amendment A). The
	// planner does not promote scheduled→queued while Now < next_dispatch_at.
	// Absent/zero entries dispatch immediately (the common case: never failed).
	NextDispatchAt map[string]*time.Time
	// DispatchAttempts counts consecutive synchronous dispatch failures per task.
	// It is a separate counter from try_number, a dispatch failure is infra, not
	// a task failure, and drives the dispatch_failed give-up at
	// dispatchMaxAttempts. Absent entries mean zero.
	DispatchAttempts map[string]int
	// InfraFailed marks a `failed` task whose failure was infra-caused (agent/pod/
	// dispatch lost), from the durable last_failure_kind signal. Such a task
	// re-places without consuming the task's retry budget (ADR 0051 Phase 1), an
	// infrastructure fault is not the user's task failing.
	InfraFailed map[string]bool
	// InfraProvisional marks an InfraFailed task whose mark the pod reconciler
	// has not confirmed yet (infra_confirmed_at NULL, ADR 0052 amendment). Until
	// it is confirmed, or InfraConfirmMaxWait has passed since ended_at, the
	// task is active: no re-place and no downstream condemnation, so a durable
	// SUCCESS record can still settle it. Absent entries mean confirmed.
	InfraProvisional map[string]bool
	// InfraAttempts counts asynchronous infra re-placements per task, the
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
	// means unlimited, the admission gate is a no-op, so a DAG that never sets it
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
	// PoolsEnabled turns on the named-pool slot gate (ADR 0053 Stage 3). The
	// Step loop sets it from the scheduler's pools flag (scheduler.pools.enabled).
	// Left false, the pool gate is a no-op and planning is byte-identical to the
	// max_active_tasks-only path.
	PoolsEnabled bool
	// ConfineUndefinedPools makes a task that names a pool its tenant has not
	// defined draw on default_pool instead of running unlimited. The Step loop
	// sets it when tenant pool writes are locked (server.pools_read_only): a
	// tenant cannot create pools then, so an undefined name would otherwise be a
	// way around the default_pool budget (#646).
	ConfineUndefinedPools bool
	// PoolBudgets is the per-pool slot cap keyed by PoolKey(TenantID, pool). A pool
	// with a non-positive or absent budget is unlimited (fail open, never
	// deadlock). The Step loop sets it from the once-per-tick PoolBudgets snapshot;
	// nil in Lite. Shared read-only across sibling runs.
	PoolBudgets map[string]int
	// PoolActive is the cross-DAG number of slots held by currently non-terminal
	// (queued+running) task instances per pool (each takes its pool_slots, ADR
	// 0066), keyed like PoolBudgets, plus any this tick already admitted into the
	// same pool by earlier runs. PlanRun adds its own within-call
	// promotions on top. The Step loop sets it and folds each run's admissions back
	// in so a single tick cannot breach a pool across runs; nil in Lite.
	PoolActive map[string]int
	// PoolReservations holds, per pool key, the starved task the pool is
	// reserved for (ADR 0066 §4): while set, the pool admits only that task.
	// The Step loop sets it from the leader's in-memory reservations; nil
	// means no pool is reserved. Shared read-only across sibling runs.
	PoolReservations map[string]PoolReservation
}

// ScheduledDAG is a cron-scheduled DAG and the logical date of its latest run.
// Catchup and StartDate drive the per-tick catchup decision (#129): when a
// leader has been down across multiple slots, catchup=true backfills every
// missed slot while catchup=false jumps straight to the most recent one.
type ScheduledDAG struct {
	// TenantID is the UUID of the tenant that owns the DAG. A dag_id is unique
	// only within its tenant, so every per-DAG decision keys on the pair (#209).
	TenantID    string
	DagID       string
	Schedule    string
	LastLogical *time.Time
	StartDate   *time.Time
	Catchup     bool
	// MaxActiveRuns caps how many runs of this DAG may be active (queued or
	// running) at once. Zero means "unlimited" (Airflow-faithful: a missing
	// limit is unbounded). The scheduler enforces the cap in createDueRuns:
	// once the active-run count reaches this value, additional due slots are
	// skipped on this tick (issue #200). Starting a queued run is capped
	// separately, on the running runs alone (RunState.MaxActiveRuns, #1766).
	MaxActiveRuns int
	// NextRunAt is the next due time the scheduler stored for the DAG
	// (SetNextRunAt, #2175), nil when none is stored. ScheduleEpoch counts the
	// changes that invalidate it (a new current version, a deleted run); a
	// write conditioned on the epoch the list returned is dropped when the DAG
	// changed in between.
	NextRunAt     *time.Time
	ScheduleEpoch int64
}

// Store is the scheduler's view of persistent state. The concrete
// implementation is sqlc-backed; tests use a fake.
type Store interface {
	ActiveRuns(ctx context.Context) ([]RunState, error)
	// PoolBudgets returns every named pool's slot cap keyed by PoolKey(tenant,
	// pool), the cross-DAG admission budget the pool gate enforces (ADR 0053
	// Stage 3). Called once per tick, and ONLY on the Pro path (poolsEnabled);
	// Lite never invokes it.
	PoolBudgets(ctx context.Context) (map[string]int, error)
	MaterializeTasks(ctx context.Context, runID string, tasks []domain.TaskSpec) error
	ApplyTransition(ctx context.Context, runID, taskID string, to domain.TaskState) error
	// MarkQueued is the scheduled -> queued write after an accepted dispatch,
	// guarded to the slot the tick planned: still scheduled, with the
	// next_dispatch_at the tick read. It reports false when the row moved on (a
	// buffered dispatch worker already failed or re-offered the task, or the
	// agent already reported), so that newer outcome is never overwritten.
	MarkQueued(ctx context.Context, runID, taskID string, expectNextDispatchAt *time.Time) (bool, error)
	// ApplyTransitions moves every listed task of a run that is still in the
	// from state to the SAME target state in one statement. The scheduler groups
	// a tick's plain state-set transitions (scheduled/skipped/upstream_failed/
	// up_for_retry) by source and target state and flushes each group here, so R
	// updates collapse to one per group. from is the state the tick's snapshot
	// read: a row a concurrent writer moved since (a user's mark or clear, an
	// agent's report under an overlapping leader) is left alone (#1605). It
	// returns the task ids that actually moved; the guarded none-rail resets stay
	// on their own methods.
	ApplyTransitions(ctx context.Context, runID string, taskIDs []string, from, to domain.TaskState) ([]string, error)
	// ResetForRetry returns a task to 'none' and increments its try number so a
	// retry re-evaluates and re-runs it. It is guarded to the up_for_retry source
	// state; the bool reports whether the reset actually fired (false = the TI was
	// no longer up_for_retry, nothing reset) so the caller does not record a
	// phantom retry on a lost race.
	ResetForRetry(ctx context.Context, runID, taskID string) (bool, error)
	// ResetForInfraReplace returns a task a reaper failed as infra to 'none' for a
	// re-run, PRESERVING try_number and bumping infra_attempts instead, an infra
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
	// RecordDispatchUnavailable re-offers a task whose pod create met an
	// unavailable Kubernetes API, WITHOUT incrementing dispatch_attempts, held
	// for the time already spent in the outage clamped to [minDelay, maxDelay].
	// It returns when the task's outage window started and the next attempt;
	// applied is false when the task is no longer scheduled or queued (ADR 0093).
	RecordDispatchUnavailable(ctx context.Context, runID, taskID string, now time.Time, minDelay, maxDelay time.Duration) (since, nextAt time.Time, applied bool, err error)
	// FailDispatchExhausted fails a scheduled task as dispatch_failed once its
	// dispatch-attempt budget is spent, so the run finalizes instead of looping.
	FailDispatchExhausted(ctx context.Context, runID, taskID, reason string) error
	// FailDispatchRefused fails a task whose dispatch the executor refused (ADR
	// 0066 section 3) and spends its retry budget: the refusal is permanent, so
	// the planner must not retry it.
	FailDispatchRefused(ctx context.Context, runID, taskID, reason string) error
	// SetRunState moves a run from the state the tick's snapshot read to
	// another one, and reports whether it moved. False means the run moved on
	// after the tick read it (a clear re-opened it, a user marked it), so the
	// tick leaves it alone (#2047).
	SetRunState(ctx context.Context, runID string, from, to domain.DagRunState) (bool, error)
	// StartQueuedRun moves a queued run to running, guarded on the run still
	// being queued, and reports whether it moved. False means the run moved on
	// after the tick read it (a user marked or deleted it), so the tick leaves
	// it alone (#1766).
	StartQueuedRun(ctx context.Context, runID, tenantID string) (bool, error)
	// ClaimAlertAttempt atomically claims ONE on-failure send attempt, reporting
	// true iff this call won it. It refuses when the episode was already
	// delivered, when the attempt budget is spent, or when the backoff has not
	// elapsed, so a dead endpoint stops being retried instead of being hit once
	// per tick for the life of the run (#431, and the delivery split below).
	// It returns the attempt number won, or 0 when the claim was refused.
	ClaimAlertAttempt(ctx context.Context, runID string, maxAttempts int, backoff time.Duration) (int, error)
	// MarkRunAlertDelivered stamps the episode as paged, for the attempt the
	// caller won. Claiming and stamping were once the same operation, which meant
	// every failed send was a page lost with no retry. The attempt is part of the
	// call because the send is detached from the tick: an operator clear can start
	// a new episode mid-send, and a stamp from the superseded one must not land.
	MarkRunAlertDelivered(ctx context.Context, runID string, attempt int) error
	// ScheduledDAGs lists the scheduled DAGs the tick must look at: those with
	// no stored next due time and those whose time is at or before now, which
	// is the scheduler's clock so the store's own clock never delays a run.
	ScheduledDAGs(ctx context.Context, now time.Time) ([]ScheduledDAG, error)
	// SetNextRunAt stores when the DAG is next due, unless its schedule epoch
	// is no longer epoch.
	SetNextRunAt(ctx context.Context, tenantID, dagID string, epoch int64, at time.Time) error
	CreateScheduledRun(ctx context.Context, tenantID, dagID string, logical time.Time) error
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
	// RecordSchedulerStepDown counts a leader step-down by reason, the rate
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
	// ObserveSchedulerLoop records the wall time of one leader tick, whether
	// its step succeeded or not (#1186).
	ObserveSchedulerLoop(d time.Duration)
	// RecordTaskRetry counts one retry released for a task (#1186).
	RecordTaskRetry(dagID, taskID string)
	// RecordSchedulerWokenTick counts a tick started early by Wake
	// (scheduler.eager_promotion) instead of by the loop interval.
	RecordSchedulerWokenTick()
	// ObserveScheduleDelay records how late a scheduled run was created: the
	// time from its slot to its creation, for the latest slot a tick created
	// for a DAG (#2175).
	ObserveScheduleDelay(d time.Duration)
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
	// deferredRun is the run whose dispatch the buffered dispatcher deferred
	// (its queue was full) during the current tick, "" when none was. Once set,
	// the rest of the tick offers no more dispatches, and the next tick starts
	// at the run after it, so one large run cannot take every freed buffer slot
	// tick after tick. Touched only by the tick goroutine.
	deferredRun string
	// dispatchTickBudget bounds the wall time one tick spends dispatching
	// (scheduler.dispatch.tick_budget, #1596). Once it is spent the tick stops
	// dispatching, like a deferred dispatch: the rest of the ready tasks stay
	// scheduled, untouched, and the next tick starts at the run after the one
	// that was cut. 0 disables it. dispatchDeadline is the current tick's end of
	// dispatch, zero when no budget applies. Tick goroutine only.
	dispatchTickBudget time.Duration
	dispatchDeadline   time.Time
	// dispatchTenantLimit caps the dispatches one tenant gets per tick
	// (scheduler.dispatch.max_per_tenant_per_tick); 0 means no cap.
	// tenantDispatches counts the current tick's dispatches per tenant UUID.
	// Tick goroutine only.
	dispatchTenantLimit int
	tenantDispatches    map[string]int
	alerter             Alerter
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
	steppingDown atomic.Bool  // true only during a leader step-down, the campaign loop sets it before canceling the run-context so the expected cancel-fanout logs at WARN, not ERROR (#311 tripwire preserved when it's false)
	// warnedSchedules dedupes the "unparseable schedule" warning per (tenant,
	// dag_id), keyed by the offending expression, so a bad cron logs once, not
	// every tick. Accessed only from the single-threaded tick (createDueRuns),
	// so it needs no lock.
	warnedSchedules map[dagRef]string
	// warnedRunCaps dedupes the "daily run limit reached" warning: tenant UUID
	// to the UTC date it was last logged for, so a capped tenant logs once a
	// day, not on every tick that retries its due slot. Tick-only, no lock.
	warnedRunCaps map[string]string
	// warnedSuspensions dedupes the "tenant is suspended" warning: tenant UUID
	// to the refusal it was last logged with, cleared once the tenant gets a
	// run again. Tick-only, no lock.
	warnedSuspensions map[string]string
	// poolsEnabled turns on the cross-DAG named-pool admission gate (ADR 0053
	// Stage 3). main calls EnablePools() when scheduler.pools.enabled is on.
	// While false, Step never loads pool budgets and never
	// threads pool occupancy, so PlanRun's pool gate is a no-op and planning is
	// byte-identical. Set once at construction (before ticking), read on the
	// single-threaded tick, so it needs no lock.
	poolsEnabled bool
	// confineUndefinedPools threads RunState.ConfineUndefinedPools; set once by
	// ConfineUndefinedPools before the scheduler starts ticking.
	confineUndefinedPools bool
	// Starvation reservation (ADR 0066 §4), leader-only, touched only by the
	// single-threaded tick. starvationThreshold 0 disables it. poolWaitSince is
	// when each task held only by its pool was first seen held; poolReservations
	// the task each reserved pool waits for; oversizeWarned dedupes the warning
	// for a task larger than its whole pool.
	starvationThreshold time.Duration
	poolWaitSince       map[taskRef]time.Time
	poolReservations    map[string]PoolReservation
	oversizeWarned      map[taskRef]bool
	// clock is the tick's wall clock, replaceable in tests.
	clock func() time.Time
	// eagerPromotion is the scheduler.eager_promotion gate (see
	// EnableEagerPromotion). Set once before Run, read on the loop goroutine.
	eagerPromotion bool
	// wake carries Wake requests to the loop. Capacity 1 coalesces a burst of
	// them into one pending tick; the loop only reads it with eagerPromotion on.
	wake chan struct{}
}

// NewScheduler builds a Scheduler over the given store, ticking every interval.
func NewScheduler(store Store, logger *slog.Logger, interval time.Duration) *Scheduler {
	return &Scheduler{
		store:             store,
		logger:            logger,
		interval:          interval,
		stepTimeout:       defaultStepTimeout(interval),
		warnedSchedules:   map[dagRef]string{},
		warnedRunCaps:     map[string]string{},
		warnedSuspensions: map[string]string{},
		alertSem:          make(chan struct{}, defaultAlertConcurrency),
		clock:             time.Now,
		// The budget is on by default: the stall it prevents is reachable with
		// default settings (synchronous dispatch, client QPS 5).
		dispatchTickBudget: DefaultDispatchTickBudget,
		tenantDispatches:   map[string]int{},
		wake:               make(chan struct{}, 1),
	}
}

// DefaultDispatchTickBudget is the default for scheduler.dispatch.tick_budget:
// the wall time one tick may spend dispatching before it stops and leaves the
// rest for the next tick (#1596). Well below the 30 s step timeout, so a tick
// that dispatches a wide fan-out under the Kubernetes client's rate limit still
// reaches every other run and finishes in seconds; long enough that a healthy
// tick, which dispatches in milliseconds, never meets it.
const DefaultDispatchTickBudget = 5 * time.Second

// SetDispatchTickBudget sets how long one tick may spend dispatching
// (scheduler.dispatch.tick_budget). 0 or less disables the budget, which
// dispatches every ready task in the tick however long it takes. Call before
// the scheduler starts ticking.
func (s *Scheduler) SetDispatchTickBudget(d time.Duration) {
	if d < 0 {
		d = 0
	}
	s.dispatchTickBudget = d
}

// SetDispatchTenantLimit caps how many tasks one tenant may dispatch in one
// tick (scheduler.dispatch.max_per_tenant_per_tick), so a tenant with a wide
// fan-out or many runs leaves dispatch room for the others. 0 or less means no
// cap. Call before the scheduler starts ticking.
func (s *Scheduler) SetDispatchTenantLimit(n int) {
	if n < 0 {
		n = 0
	}
	s.dispatchTenantLimit = n
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
// Background sweeps that mutate cluster state, the pod reconciler and the
// staging-volume GC, gate on this so that at replicaCount>1 only the leader
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
// the window; outside it, context.Canceled stays ERROR, the tripwire that
// catches an unexpected cancel a flat downgrade would silently swallow.
func (s *Scheduler) MarkSteppingDown(reason string) {
	s.steppingDown.Store(true)
	if s.recorder != nil {
		s.recorder.RecordSchedulerStepDown(reason)
	}
}

// ClearSteppingDown ends the step-down window opened by MarkSteppingDown.
// Idempotent, calling it when no step-down is active is a no-op.
func (s *Scheduler) ClearSteppingDown() { s.steppingDown.Store(false) }

// RecordReacquireSince records the time spent stepped down (#311). It is
// called by the campaign loop immediately after a successful re-acquire, with
// the timestamp captured at the moment of step-down. A zero stepDownAt (no
// prior step-down, first acquisition at boot) is ignored.
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
// 3). main calls it when scheduler.pools.enabled is on, which unset means the
// cluster edition. Left unset, the pool gate stays a no-op and the tick never
// queries pool budgets, so planning is byte-identical. Call once before the
// scheduler starts ticking.
func (s *Scheduler) EnablePools() { s.poolsEnabled = true }

// ConfineUndefinedPools makes tasks that name an undefined pool draw on their
// tenant's default_pool (see RunState.ConfineUndefinedPools). Main calls it
// when server.pools_read_only is on. Call once before the scheduler ticks.
func (s *Scheduler) ConfineUndefinedPools() { s.confineUndefinedPools = true }

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
// tick; it MUST treat every send as best-effort, a delivery failure is logged,
// never propagated, so alerting can never fail a run.
type Alerter interface {
	// AlertRunFailed sends every on_failure rule and reports whether all of them
	// were delivered. The caller stamps delivery only on true, so a failed send
	// stays retryable instead of being marked and lost.
	AlertRunFailed(ctx context.Context, run RunState) bool
}

// Run drives the scheduling loop until ctx is canceled. The loop is crash-proof:
// a panic or error in a tick is recovered and logged, so the scheduler keeps
// ticking, it may fall behind, but it never dies (the critical invariant).
//
// With eager promotion on, a Wake also starts a tick, no sooner than wakeGap
// after the start of the previous one, and restarts the interval so it stays
// the upper bound between ticks. With it off the wake channel is nil, which
// never fires, so the loop ticks on the interval alone.
func (s *Scheduler) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	var wake <-chan struct{}
	if s.eagerPromotion {
		wake = s.wake
	}
	var (
		last     time.Time
		lastTick time.Duration
	)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		case <-wake:
			if !s.waitWakeGap(ctx, last, lastTick) {
				return ctx.Err()
			}
			ticker.Reset(s.interval)
			if s.recorder != nil {
				s.recorder.RecordSchedulerWokenTick()
			}
		}
		last = time.Now()
		s.tick(ctx)
		lastTick = time.Since(last)
	}
}

// tick runs one Step under a timeout, recovering any panic so a single bad tick
// can never crash the process or stop the loop. It is the top-level backstop;
// per-run isolation in Step quarantines individual poison runs underneath it.
func (s *Scheduler) tick(ctx context.Context) {
	// Registered first so it runs last, after a recovered panic too. Only a
	// leader's tick does scheduling work; a follower's would dilute the
	// histogram with near-zero samples.
	if s.recorder != nil && s.leading.Load() {
		start := time.Now()
		defer func() { s.recorder.ObserveSchedulerLoop(time.Since(start)) }()
	}
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
// that stepped down after losing the lock) reports healthy without ticking, it
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

// StalledFor reports whether this process leads and its scheduling loop has
// not started an iteration for longer than limit, with the time of the last
// one. A follower, and a leader before its first tick, are never stalled. A
// database outage does not stall the loop: Step marks its start before it
// reads anything and fails fast, so only a loop that stopped iterating (a
// blocked goroutine) is reported. It backs the opt-in liveness check of
// server.liveness_scheduler_stall (#539).
func (s *Scheduler) StalledFor(limit time.Duration) (bool, time.Time) {
	if !s.leading.Load() {
		return false, time.Time{}
	}
	nanos := s.lastTick.Load()
	if nanos == 0 {
		return false, time.Time{}
	}
	last := time.Unix(0, nanos).UTC()
	return time.Since(last) > limit, last
}

// Step runs one deterministic scheduling iteration over every active run. Each
// run is advanced in isolation (see advanceSafely): a panic or error in one
// run is contained, so it never blocks the other runs or new-run creation.
// The reaper runs independently of createDueRuns success, they share no
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
		s.forgetPoolWaits()
		return nil
	}
	runs, err := s.store.ActiveRuns(ctx)
	if err != nil {
		return fmt.Errorf("listing active runs: %w", err)
	}
	s.stopSuspended(ctx, runs)
	activeByDAG := make(map[dagRef]int, len(runs))
	// Per-DAG task-admission budget (max_active_tasks, ADR 0053 Stage 1).
	// activeTasksByDAG is the snapshot of currently non-terminal (queued+running)
	// TIs per DAG; admittedTasksByDAG folds in what earlier sibling runs already
	// promoted this tick so a single tick cannot breach the cap across runs,
	// mirroring how createDueRuns folds justCreated into the max_active_runs cap.
	activeTasksByDAG := activeTaskCounts(runs)
	admittedTasksByDAG := make(map[dagRef]int, len(runs))
	// Cross-DAG named-pool budget (ADR 0053 Stage 3, Pro only). Loaded once per
	// tick; poolOccupied starts at the current cross-DAG occupancy and each run's
	// admissions fold back in, the same within-tick threading as the per-DAG cap,
	// generalized to a per-pool map. Left nil on the Lite path, where no pool query
	// runs and the pool gate is a no-op.
	poolBudgets, poolOccupied, err := s.loadPoolBudget(ctx, runs)
	if err != nil {
		return err
	}
	tracking := s.reservationsOn()
	var reservations map[string]PoolReservation
	var waits map[string][]PoolWait
	if tracking {
		reservations = s.pruneReservations(runs, poolBudgets)
		waits = make(map[string][]PoolWait)
	}
	// Create the due cron runs before dispatching: dispatch is the part of the
	// tick that can run long (a wide fan-out under the client rate limit), and a
	// tick that ran out of time there used to skip run creation, losing the slot
	// with catchup=false (#1596). The runs created here are not in this tick's
	// snapshot either way, so the order changes nothing else.
	for i := range runs {
		activeByDAG[dagRef{runs[i].TenantID, runs[i].DagID}]++
	}
	createErr := s.createDueRuns(ctx, activeByDAG)
	startable := s.startableQueuedRuns(runs)
	start := rotateAfter(runs, s.deferredRun)
	s.deferredRun = ""
	s.openDispatchWindow()
	for k := range runs {
		run := runs[(start+k)%len(runs)]
		if run.State == domain.DagRunStateQueued && !startable[run.RunID] {
			continue
		}
		key := dagRef{run.TenantID, run.DagID}
		run.ActiveTaskCount = activeTasksByDAG[key] + admittedTasksByDAG[key]
		run.PoolsEnabled = s.poolsEnabled
		run.ConfineUndefinedPools = s.confineUndefinedPools
		run.PoolBudgets = poolBudgets
		run.PoolActive = poolOccupied
		run.PoolReservations = reservations
		res := s.advanceSafely(ctx, run)
		admittedTasksByDAG[key] += res.admitted
		for k, n := range res.byPool {
			poolOccupied[k] += n
		}
		if tracking && len(res.waits) > 0 {
			waits[run.RunID] = res.waits
		}
	}
	if tracking {
		s.recordPoolWaits(s.clock(), runs, waits, poolBudgets)
	}
	return createErr
}

// waitsForUnpause reports whether the tick must leave the run untouched: a
// queued run of a paused DAG is not started until the DAG is unpaused (#1705),
// nor one of a tenant suspended in drain or stop mode until it is resumed
// (#2181). It stays in the snapshot the caps are computed from (see
// RunState.Held).
func (r RunState) waitsForUnpause() bool {
	return (r.Held || r.Suspension.HoldsDispatch()) && r.State == domain.DagRunStateQueued
}

// startableQueuedRuns returns the queued runs this tick moves to running, by
// RunID. Like Airflow's _start_queued_dagruns (#1766), it takes the queued
// runs of unpaused DAGs oldest logical date first and starts one only while
// its DAG has fewer than max_active_runs runs in running; the count starts
// from the running runs in the snapshot, held ones included, and grows with
// each start. A queued run left out waits, untouched, for a later tick: it is
// not planned and nothing of it is dispatched. A non-positive max_active_runs
// is unlimited, as in hasHeadroom.
func (s *Scheduler) startableQueuedRuns(runs []RunState) map[string]bool {
	// queued holds an index into runs (so no RunState is copied) and the
	// run's logical date, parsed once.
	type candidate struct {
		idx     int
		logical time.Time
	}
	var queued []candidate
	running := map[dagRef]int{}
	for i := range runs {
		switch {
		case runs[i].State == domain.DagRunStateRunning:
			running[dagRef{runs[i].TenantID, runs[i].DagID}]++
		case runs[i].State == domain.DagRunStateQueued && !runs[i].waitsForUnpause():
			queued = append(queued, candidate{i, runs[i].logicalTime()})
		}
	}
	if len(queued) == 0 {
		return nil
	}
	sort.SliceStable(queued, func(i, j int) bool { return queued[i].logical.Before(queued[j].logical) })
	startable := make(map[string]bool, len(queued))
	for _, c := range queued {
		run := &runs[c.idx]
		key := dagRef{run.TenantID, run.DagID}
		if run.MaxActiveRuns > 0 && running[key] >= run.MaxActiveRuns {
			s.record("max_active_runs_start_cap")
			continue
		}
		running[key]++
		startable[run.RunID] = true
	}
	return startable
}

// logicalTime parses the run's logical date. A run without one, or with one
// that does not parse, sorts first, keeping its snapshot order (queued_at).
func (r RunState) logicalTime() time.Time {
	t, err := time.Parse(time.RFC3339Nano, r.LogicalDate)
	if err != nil {
		return time.Time{}
	}
	return t
}

// openDispatchWindow starts the tick's dispatch accounting: the deadline the
// dispatch budget sets and the per-tenant dispatch counts.
func (s *Scheduler) openDispatchWindow() {
	s.dispatchDeadline = time.Time{}
	if s.dispatchTickBudget > 0 {
		s.dispatchDeadline = s.clock().Add(s.dispatchTickBudget)
	}
	clear(s.tenantDispatches)
}

// holdDispatch reports whether the tick must leave run's next ready task
// scheduled instead of dispatching it: the run's tenant is suspended in drain
// or stop mode, an earlier dispatch this tick was
// deferred, the tick's dispatch budget is spent, or the run's tenant used its
// dispatches for this tick. A held task is not touched, so it costs no dispatch
// attempt and no backoff, and the next tick offers it again. When the budget
// runs out, run is remembered so the next tick starts after it.
func (s *Scheduler) holdDispatch(run RunState) bool {
	// A tenant suspended in drain or stop mode gets no dispatch (#2181). The
	// task stays scheduled and is offered again once the tenant is resumed.
	if run.Suspension.HoldsDispatch() {
		return true
	}
	if s.deferredRun != "" {
		return true
	}
	if !s.dispatchDeadline.IsZero() && !s.clock().Before(s.dispatchDeadline) {
		s.deferredRun = run.RunID
		s.logger.Debug("dispatch budget for this tick spent; the remaining tasks wait for the next tick",
			"run", run.RunID, "budget", s.dispatchTickBudget)
		s.record("dispatch_budget_spent")
		return true
	}
	if s.dispatchTenantLimit > 0 && s.tenantDispatches[run.TenantID] >= s.dispatchTenantLimit {
		return true
	}
	return false
}

// rotateAfter returns the index the tick starts advancing runs at: right after
// the run whose dispatch was deferred last tick, or 0 when none was (or it is no
// longer active), which keeps the store's order whenever the buffer kept up.
func rotateAfter(runs []RunState, deferredRun string) int {
	if deferredRun == "" {
		return 0
	}
	for i := range runs {
		if runs[i].RunID == deferredRun {
			return (i + 1) % len(runs)
		}
	}
	return 0
}

// activeTaskCounts tallies, per DAG, the task instances that already occupy a
// max_active_tasks slot, those in a non-terminal active state (queued or
// running) across every active run of the DAG. It is the snapshot the admission
// gate subtracts from max_active_tasks to size per-run headroom (ADR 0053 Stage
// 1). Reuses the runs Step already loaded, so it adds no per-tick query.
func activeTaskCounts(runs []RunState) map[dagRef]int {
	counts := make(map[dagRef]int, len(runs))
	for i := range runs {
		active := 0
		for _, st := range runs[i].States {
			if st == domain.TaskStateQueued || st == domain.TaskStateRunning {
				active++
			}
		}
		if active > 0 {
			counts[dagRef{runs[i].TenantID, runs[i].DagID}] += active
		}
	}
	return counts
}

// dagRef identifies a DAG across tenants. A dag_id is unique only within its
// tenant, so the per-DAG caps (max_active_runs, max_active_tasks) and the
// once-per-schedule warnings key on the (tenant, dag_id) pair: two tenants that
// both own an "etl" DAG never share a budget (#209). A struct key costs no
// allocation per lookup and cannot collide the way a joined string could.
type dagRef struct{ tenantID, dagID string }

// loadPoolBudget prepares the cross-DAG named-pool admission state for a tick
// (ADR 0053 Stage 3): the per-pool slot caps (queried once) and the current
// per-pool occupancy (derived from the runs already loaded). It is a strict
// no-op on the Lite/non-Pro path, it returns nil maps and issues NO pool query,
// so a Lite tick is byte-identical to the pre-pool behavior. A query error is
// returned so the tick surfaces it rather than silently disabling the gate.
func (s *Scheduler) loadPoolBudget(ctx context.Context, runs []RunState) (budgets, occupied map[string]int, err error) {
	if !s.poolsEnabled {
		return nil, nil, nil
	}
	budgets, err = s.store.PoolBudgets(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("loading pool budgets: %w", err)
	}
	return budgets, activePoolCounts(runs, budgets, s.confineUndefinedPools), nil
}

// activePoolCounts tallies, per pool (keyed by PoolKey), the slots already
// occupied: the pool_slots of every task instance queued or running across
// every active run, cross-DAG (ADR 0053 Stage 3, weighted by ADR 0066). A task instance's pool is its spec pool, or the
// implicit default pool. Reuses the runs Step already loaded, so it adds no
// per-tick query. Only built on the Pro path (see loadPoolBudget). With confine
// set, an undefined pool is charged to default_pool, the same pool admission
// charges it to.
func activePoolCounts(runs []RunState, budgets map[string]int, confine bool) map[string]int {
	counts := make(map[string]int, len(runs))
	for i := range runs {
		for _, t := range runs[i].Tasks {
			if st := runs[i].States[t.TaskID]; st == domain.TaskStateQueued || st == domain.TaskStateRunning {
				counts[effectivePoolKey(runs[i].TenantID, t.Pool, budgets, confine)] += t.EffectivePoolSlots()
			}
		}
	}
	return counts
}

// advance plans and applies one run's transitions, returning how many tasks it
// promoted to queued this tick (the per-DAG max_active_tasks charge, ADR 0053
// Stage 1) and the per-pool slots those promotions take (the cross-DAG pool
// charge, Stage 3, weighted by ADR 0066; nil when the pool gate is off). The caller folds both into
// the sibling runs' budgets so a single tick cannot breach either cap.
// advanced is what advancing one run produced: the tasks it admitted to
// queued, the slots they take per pool, and the tasks held only by their pool.
type advanced struct {
	admitted int
	byPool   map[string]int
	waits    []PoolWait
}

func (s *Scheduler) advance(ctx context.Context, run RunState) (res advanced, err error) {
	// Start a queued run Step admitted: materialize its task instances on
	// first sight (a run a clear re-queued keeps the ones it has), then move it
	// to running, guarded on it still being queued. Its tasks are planned from
	// the next tick on.
	if run.State == domain.DagRunStateQueued {
		if len(run.States) == 0 {
			if err = s.store.MaterializeTasks(ctx, run.RunID, run.Tasks); err != nil {
				return res, fmt.Errorf("materializing tasks: %w", err)
			}
		}
		started, serr := s.store.StartQueuedRun(ctx, run.RunID, run.TenantID)
		if serr != nil {
			return res, fmt.Errorf("starting run: %w", serr)
		}
		if !started {
			s.logger.Debug("queued run moved on before its start; leaving it", "run", run.RunID, "dag", run.DagID)
			s.record("start_run_superseded")
		}
		return res, nil
	}
	// Resolve the task index once so planning and every dispatch below share it;
	// a run without the store's prebuilt graph gets one built here.
	run.Graph = run.taskGraph()
	poolOf := taskPools(run) // taskID → pool charge; nil when the pool gate is off.
	planned, waits := planRun(run)
	res.waits = waits
	settled, err := s.applyPlan(ctx, run, planned, poolOf, &res)
	if err != nil {
		return res, err
	}
	if s.eagerPromotion {
		if run, err = s.promoteSameTick(ctx, run, planned, settled, poolOf, &res); err != nil {
			return res, err
		}
	}
	if state, done := FinalizeRun(run); done {
		finalized, ferr := s.store.SetRunState(ctx, run.RunID, run.State, state)
		if ferr != nil {
			return res, fmt.Errorf("finalizing run: %w", ferr)
		}
		if !finalized {
			s.logger.Debug("run moved on before its finalize; leaving it", "run", run.RunID, "dag", run.DagID)
			s.record("finalize_run_superseded")
			return res, nil
		}
		s.maybeAlertFailure(ctx, state, run)
	}
	return res, nil
}

// applyPlan applies one pass of planned transitions to a run, charging every
// queued promotion to res (one max_active_tasks admission, and the task's slots
// in its pool when the pool gate is on), and returns the plain transitions it
// persisted.
//
// Plain state-set transitions (no side effect beyond the write + metric) are
// collected and flushed grouped by source and target state in one UPDATE
// each, instead of one per task. The queued (dispatch) and none (guarded
// reset) rails keep their per-task paths inside applyPlanned. Deferring the
// plain writes to after the loop is safe: each targets a distinct row,
// FinalizeRun reads the in-memory run (not the DB), and dispatch depends on
// task specs, not sibling TI state. Each write is guarded on the state this
// tick's snapshot read, so a row a concurrent writer moved during the
// dispatch loop (a user's mark or clear, an agent's report) is left alone
// (#1605), and the returned batch carries only the rows that moved.
func (s *Scheduler) applyPlan(ctx context.Context, run RunState, plan []PlannedTransition, poolOf map[string]poolCharge, res *advanced) (*transitionBatch, error) {
	batch := newTransitionBatch()
	for _, t := range plan {
		if t.To == domain.TaskStateQueued {
			res.admit(poolOf, t.TaskID)
		}
		if err := s.applyPlanned(ctx, run, t, batch); err != nil {
			return nil, err
		}
	}
	if err := s.flushTransitions(ctx, run, batch); err != nil {
		return nil, err
	}
	return batch, nil
}

// poolCharge is what admitting one task costs its pool: the pool's budget key
// and the task's slots (ADR 0066).
type poolCharge struct {
	key   string
	slots int
}

// taskPools maps each of a run's task IDs to its pool charge (ADR 0053 Stage
// 3, weighted by ADR 0066), applying the implicit-default-pool fallback. It
// returns nil when the pool gate is off (Lite / non-Pro), so advance does no
// per-pool bookkeeping there.
func taskPools(run RunState) map[string]poolCharge {
	if !run.PoolsEnabled {
		return nil
	}
	m := make(map[string]poolCharge, len(run.Tasks))
	for _, t := range run.Tasks {
		m[t.TaskID] = poolCharge{
			key:   effectivePoolKey(run.TenantID, t.Pool, run.PoolBudgets, run.ConfineUndefinedPools),
			slots: t.EffectivePoolSlots(),
		}
	}
	return m
}

// findTask returns the task with the given ID from the run topology. It looks
// the task up through the run's task index, so dispatching a whole fan-out is
// linear in its width rather than quadratic.
func findTask(run RunState, taskID string) (domain.TaskSpec, bool) {
	i, ok := run.taskGraph().Lookup(taskID)
	if !ok {
		return domain.TaskSpec{}, false
	}
	return run.Tasks[i], true
}

// TenantStopper is the optional store seam of the stop suspension mode
// (#2181): it fails the queued and running task instances of a tenant
// suspended in stop mode and returns how many it failed. A store without it
// leaves stop applying only what drain does.
type TenantStopper interface {
	FailStoppedTenantTaskInstances(ctx context.Context, tenantID string) (int64, error)
}

// stopSuspended fails, once per tenant, the queued and running task
// instances of every tenant the snapshot shows suspended in stop mode with
// such a task instance. The service API already did it when it suspended the
// tenant; the tick repeats it so an attempt dispatched while that call ran is
// stopped too. The attempt learns it on its next heartbeat, which matches no
// live row, and the agent kills its task. This tick still sees the old states;
// the next one plans from the failed rows (a retry stays scheduled under the
// suspension). A failure is logged and retried next tick, it does not abort
// the tick.
func (s *Scheduler) stopSuspended(ctx context.Context, runs []RunState) {
	stopper, ok := s.store.(TenantStopper)
	if !ok {
		return
	}
	done := map[string]bool{}
	for i := range runs {
		r := &runs[i]
		if r.Suspension != domain.SuspensionStop || done[r.TenantID] || !hasActiveTask(r.States) {
			continue
		}
		done[r.TenantID] = true
		n, err := stopper.FailStoppedTenantTaskInstances(ctx, r.TenantID)
		if err != nil {
			s.logger.Warn("could not stop the task instances of a tenant suspended in stop mode; retrying next tick",
				"tenant_id", r.TenantID, "error", err)
			s.record("tenant_stop_error")
			continue
		}
		if n > 0 {
			s.logger.Info("stopped the task instances of a tenant suspended in stop mode",
				"tenant_id", r.TenantID, "task_instances", n)
			s.record("tenant_stopped")
		}
	}
}

// hasActiveTask reports whether a task instance of states is queued or
// running.
func hasActiveTask(states map[string]domain.TaskState) bool {
	for _, st := range states {
		if st == domain.TaskStateQueued || st == domain.TaskStateRunning {
			return true
		}
	}
	return false
}
