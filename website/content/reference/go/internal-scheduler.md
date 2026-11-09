---
title: "internal/scheduler"
linkTitle: "internal/scheduler"
weight: 2
---

```go
import "github.com/dexadata/dexaflow/internal/scheduler"
```

Package scheduler implements the Dexaflow scheduling state machine and loop.

## Index

- [Constants](<#constants>)
- [func CampaignAndRun\(ctx context.Context, leader \*Leader, sched \*Scheduler, logger \*slog.Logger\)](<#CampaignAndRun>)
- [func CanTransition\(from, to domain.TaskState\) bool](<#CanTransition>)
- [func CanTransitionDagRun\(from, to domain.DagRunState\) bool](<#CanTransitionDagRun>)
- [func DagRunTransitions\(\) \[\]\[2\]domain.DagRunState](<#DagRunTransitions>)
- [func FinalizeRun\(run RunState\) \(domain.DagRunState, bool\)](<#FinalizeRun>)
- [func InfraReplaceMaxDelay\(\) time.Duration](<#InfraReplaceMaxDelay>)
- [func PlannerTransitions\(\) \[\]\[2\]domain.TaskState](<#PlannerTransitions>)
- [func PoolKey\(tenant, pool string\) string](<#PoolKey>)
- [func TaskTransitions\(\) \[\]\[2\]domain.TaskState](<#TaskTransitions>)
- [type Alerter](<#Alerter>)
- [type AsyncDispatchFailures](<#AsyncDispatchFailures>)
  - [func NewAsyncDispatchFailures\(store AsyncDispatchStore, logger \*slog.Logger\) \*AsyncDispatchFailures](<#NewAsyncDispatchFailures>)
  - [func \(a \*AsyncDispatchFailures\) HandleDispatchFailure\(ctx context.Context, runID, taskID string, disp executor.Disposition, cause error\) error](<#AsyncDispatchFailures.HandleDispatchFailure>)
  - [func \(a \*AsyncDispatchFailures\) MarkTaskDispatchFailed\(ctx context.Context, runID, taskID, reason string\) error](<#AsyncDispatchFailures.MarkTaskDispatchFailed>)
- [type AsyncDispatchStore](<#AsyncDispatchStore>)
- [type Dispatcher](<#Dispatcher>)
- [type Leader](<#Leader>)
  - [func NewLeader\(pool \*pgxpool.Pool\) \*Leader](<#NewLeader>)
  - [func \(l \*Leader\) HoldsLock\(ctx context.Context\) \(bool, error\)](<#Leader.HoldsLock>)
  - [func \(l \*Leader\) Release\(ctx context.Context\) error](<#Leader.Release>)
  - [func \(l \*Leader\) TryAcquire\(ctx context.Context\) \(bool, error\)](<#Leader.TryAcquire>)
- [type LeaderHealthReader](<#LeaderHealthReader>)
  - [func NewLeaderHealthReader\(pool \*pgxpool.Pool\) \*LeaderHealthReader](<#NewLeaderHealthReader>)
  - [func \(r \*LeaderHealthReader\) Heartbeat\(\) \(healthy bool, last time.Time\)](<#LeaderHealthReader.Heartbeat>)
- [type PlannedTransition](<#PlannedTransition>)
  - [func PlanRun\(run RunState\) \[\]PlannedTransition](<#PlanRun>)
- [type PoolReservation](<#PoolReservation>)
- [type PoolWait](<#PoolWait>)
- [type Recorder](<#Recorder>)
- [type RunState](<#RunState>)
- [type ScheduledDAG](<#ScheduledDAG>)
- [type Scheduler](<#Scheduler>)
  - [func NewScheduler\(store Store, logger \*slog.Logger, interval time.Duration\) \*Scheduler](<#NewScheduler>)
  - [func \(s \*Scheduler\) ClearSteppingDown\(\)](<#Scheduler.ClearSteppingDown>)
  - [func \(s \*Scheduler\) ConfineUndefinedPools\(\)](<#Scheduler.ConfineUndefinedPools>)
  - [func \(s \*Scheduler\) EnableEagerPromotion\(\)](<#Scheduler.EnableEagerPromotion>)
  - [func \(s \*Scheduler\) EnablePools\(\)](<#Scheduler.EnablePools>)
  - [func \(s \*Scheduler\) Heartbeat\(\) \(bool, time.Time\)](<#Scheduler.Heartbeat>)
  - [func \(s \*Scheduler\) IsLeading\(\) bool](<#Scheduler.IsLeading>)
  - [func \(s \*Scheduler\) LeaderSince\(\) time.Time](<#Scheduler.LeaderSince>)
  - [func \(s \*Scheduler\) MarkSteppingDown\(reason string\)](<#Scheduler.MarkSteppingDown>)
  - [func \(s \*Scheduler\) RecordReacquireSince\(stepDownAt time.Time\)](<#Scheduler.RecordReacquireSince>)
  - [func \(s \*Scheduler\) Run\(ctx context.Context\) error](<#Scheduler.Run>)
  - [func \(s \*Scheduler\) SetAlertConcurrency\(n int\)](<#Scheduler.SetAlertConcurrency>)
  - [func \(s \*Scheduler\) SetAlerter\(a Alerter\)](<#Scheduler.SetAlerter>)
  - [func \(s \*Scheduler\) SetDispatchTenantLimit\(n int\)](<#Scheduler.SetDispatchTenantLimit>)
  - [func \(s \*Scheduler\) SetDispatchTickBudget\(d time.Duration\)](<#Scheduler.SetDispatchTickBudget>)
  - [func \(s \*Scheduler\) SetDispatcher\(d Dispatcher\)](<#Scheduler.SetDispatcher>)
  - [func \(s \*Scheduler\) SetLeading\(on bool\)](<#Scheduler.SetLeading>)
  - [func \(s \*Scheduler\) SetPoolStarvationThreshold\(d time.Duration\)](<#Scheduler.SetPoolStarvationThreshold>)
  - [func \(s \*Scheduler\) SetRecorder\(r Recorder\)](<#Scheduler.SetRecorder>)
  - [func \(s \*Scheduler\) SetStepTimeout\(d time.Duration\)](<#Scheduler.SetStepTimeout>)
  - [func \(s \*Scheduler\) StalledFor\(limit time.Duration\) \(bool, time.Time\)](<#Scheduler.StalledFor>)
  - [func \(s \*Scheduler\) Step\(ctx context.Context\) error](<#Scheduler.Step>)
  - [func \(s \*Scheduler\) SteppingDown\(\) bool](<#Scheduler.SteppingDown>)
  - [func \(s \*Scheduler\) Wake\(\)](<#Scheduler.Wake>)
- [type Store](<#Store>)
- [type TaskGraph](<#TaskGraph>)
  - [func NewTaskGraph\(tasks \[\]domain.TaskSpec\) \*TaskGraph](<#NewTaskGraph>)
  - [func \(g \*TaskGraph\) Lookup\(taskID string\) \(int, bool\)](<#TaskGraph.Lookup>)
- [type TenantStopper](<#TenantStopper>)
- [type TriggerDecision](<#TriggerDecision>)
  - [func EvaluateTriggerRule\(rule domain.TriggerRule, upstreams \[\]domain.TaskState\) TriggerDecision](<#EvaluateTriggerRule>)


## Constants

<a name="DefaultDispatchTickBudget"></a>DefaultDispatchTickBudget is the default for scheduler.dispatch.tick\_budget: the wall time one tick may spend dispatching before it stops and leaves the rest for the next tick \(\#1596\). Well below the 30 s step timeout, so a tick that dispatches a wide fan\-out under the Kubernetes client's rate limit still reaches every other run and finishes in seconds; long enough that a healthy tick, which dispatches in milliseconds, never meets it.

```go
const DefaultDispatchTickBudget = 5 * time.Second
```

<a name="InfraConfirmMaxWait"></a>InfraConfirmMaxWait is the liveness valve of the infra confirmation gate \(ADR 0052 amendment, part 2\): how long after a provisional infra mark's ended\_at the planner keeps the task active waiting for the pod reconciler to confirm the mark or settle a durable SUCCESS over it. Past it the planner acts on the guess as before. It must exceed a stopped task pod's termination grace plus two maintenance intervals \(the reconciler needs the container to exit and then one sweep\), and stay below the orphan\-run threshold; both orderings are rungs of the boot\-time resilience ladder. The store reads it too, to guard the re\-place rail.

```go
const InfraConfirmMaxWait = 2 * time.Minute
```

<a name="LockID"></a>LockID is the fixed Postgres advisory\-lock id gating scheduler leadership \("LeoFlow" in hex\), per ADR 0009.

```go
const LockID int64 = 0x4C656F466C6F77
```

<a name="CampaignAndRun"></a>
## func [CampaignAndRun](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/leadership.go#L56>)

```go
func CampaignAndRun(ctx context.Context, leader *Leader, sched *Scheduler, logger *slog.Logger)
```

CampaignAndRun acquires scheduler leadership \(polling every leaderCheckInterval\) and runs the loop only while leader, so a single replica schedules at a time \(ADR 0009\). If leadership is lost mid\-run \(the advisory lock dropped because the connection blipped, was idle\-reaped, or recycled\), it steps down and re\-campaigns rather than scheduling alongside the new leader. It returns when ctx is done.

<a name="CanTransition"></a>
## func [CanTransition](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/state_machine.go#L95>)

```go
func CanTransition(from, to domain.TaskState) bool
```

CanTransition reports whether a task instance may move from one state to another under the Dexaflow state machine.

<a name="CanTransitionDagRun"></a>
## func [CanTransitionDagRun](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/state_machine.go#L100>)

```go
func CanTransitionDagRun(from, to domain.DagRunState) bool
```

CanTransitionDagRun reports whether a dag run may move from one state to another.

<a name="DagRunTransitions"></a>
## func [DagRunTransitions](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/state_machine.go#L236>)

```go
func DagRunTransitions() [][2]domain.DagRunState
```

DagRunTransitions lists every legal dag run state transition as \(from, to\) pairs, sorted.

<a name="FinalizeRun"></a>
## func [FinalizeRun](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/plan.go#L480>)

```go
func FinalizeRun(run RunState) (domain.DagRunState, bool)
```

FinalizeRun reports the terminal dag\-run state once every task is terminal. A failed task that still has retry budget \(or an infra re\-place budget\) counts as non\-terminal, so the run keeps running until it resolves. The boolean is false while any task is still non\-terminal.

<a name="InfraReplaceMaxDelay"></a>
## func [InfraReplaceMaxDelay](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/dispatch_backoff.go#L76>)

```go
func InfraReplaceMaxDelay() time.Duration
```

InfraReplaceMaxDelay is the longest the planner may park an infra\-failed task before re\-placing it: the backoff before the last permitted re\-place \(attempt infraMaxAttempts\) plus the full de\-synchronizing jitter window. It is the upper bound on how long a run whose only live task is infra\-parked shows no activity, so the executor's orphan\-run threshold must sit above it or the orphan reaper fails a run that is still recovering. The two values live in packages that depend in one direction only \(the scheduler imports the executor\), so this is exported for the server to hand to the executor's boot\-time resilience ladder rather than read from there.

<a name="PlannerTransitions"></a>
## func [PlannerTransitions](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/state_machine.go#L224>)

```go
func PlannerTransitions() [][2]domain.TaskState
```

PlannerTransitions lists the task state transitions the planner can ask for, as \(from, to\) pairs, sorted.

<a name="PoolKey"></a>
## func [PoolKey](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/plan.go#L178>)

```go
func PoolKey(tenant, pool string) string
```

PoolKey composes the cross\-DAG admission\-budget key for a \(tenant, pool\) pair. Pools are tenant\-scoped, so a pool name is only meaningful within its tenant; the key namespaces the pool budget and occupancy maps by tenant. The NUL separator cannot occur in a tenant UUID or an Airflow pool name, so the join is unambiguous. The scheduler store builds its budget map with the same key.

<a name="TaskTransitions"></a>
## func [TaskTransitions](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/state_machine.go#L230>)

```go
func TaskTransitions() [][2]domain.TaskState
```

TaskTransitions lists every legal task state transition as \(from, to\) pairs, sorted, for tests that check the storage queries against the table.

<a name="Alerter"></a>
## type [Alerter](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/scheduler.go#L624-L629>)

Alerter dispatches a DAG's on\-failure alert rules for a run that finalized in the failed state. Implementations resolve each rule's managed connection to an endpoint and send \(Slack/webhook\). The scheduler calls it from a detached goroutine, so an implementation may block on network I/O without stalling the tick; it MUST treat every send as best\-effort, a delivery failure is logged, never propagated, so alerting can never fail a run.

```go
type Alerter interface {
    // AlertRunFailed sends every on_failure rule and reports whether all of them
    // were delivered. The caller stamps delivery only on true, so a failed send
    // stays retryable instead of being marked and lost.
    AlertRunFailed(ctx context.Context, run RunState) bool
}
```

<a name="AsyncDispatchFailures"></a>
## type [AsyncDispatchFailures](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/async_dispatch.go#L43-L47>)

AsyncDispatchFailures handles a dispatch that failed inside a buffered dispatch worker exactly as handleDispatchFailure handles a synchronous one: cluster backpressure \(a quota 403, a 429\) is re\-offered without spending the dispatch budget, an unavailable Kubernetes API \(a 503, a 504, a 500, a connection refused or reset, the client deadline\) is re\-offered without spending it until dispatchUnavailableWindow passes \(ADR 0093\), any other error \(a rejection\) is re\-offered with a counted attempt and a growing backoff, and the task fails as dispatch\_failed only once dispatchMaxAttempts is spent. It is the dispatch.FailureSink the buffered dispatcher reports to.

```go
type AsyncDispatchFailures struct {
    // contains filtered or unexported fields
}
```

<a name="NewAsyncDispatchFailures"></a>
### func [NewAsyncDispatchFailures](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/async_dispatch.go#L50>)

```go
func NewAsyncDispatchFailures(store AsyncDispatchStore, logger *slog.Logger) *AsyncDispatchFailures
```

NewAsyncDispatchFailures builds the handler over store.

<a name="AsyncDispatchFailures.HandleDispatchFailure"></a>
### func \(\*AsyncDispatchFailures\) [HandleDispatchFailure](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/async_dispatch.go#L66>)

```go
func (a *AsyncDispatchFailures) HandleDispatchFailure(ctx context.Context, runID, taskID string, disp executor.Disposition, cause error) error
```

HandleDispatchFailure re\-offers or fails a task whose buffered dispatch failed with the given disposition. A task that is no longer scheduled or queued \(the agent reported, an operator acted\) is left alone.

<a name="AsyncDispatchFailures.MarkTaskDispatchFailed"></a>
### func \(\*AsyncDispatchFailures\) [MarkTaskDispatchFailed](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/async_dispatch.go#L59>)

```go
func (a *AsyncDispatchFailures) MarkTaskDispatchFailed(ctx context.Context, runID, taskID, reason string) error
```

MarkTaskDispatchFailed fails the task outright; the buffered dispatcher uses it for a failure that is not a dispatch error \(a worker panic\).

<a name="AsyncDispatchStore"></a>
## type [AsyncDispatchStore](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/async_dispatch.go#L15-L31>)

AsyncDispatchStore is what AsyncDispatchFailures needs from storage.

```go
type AsyncDispatchStore interface {
    // DispatchAttempts returns the task's consecutive dispatch failures while it
    // is still scheduled or queued; active is false once it has moved on.
    DispatchAttempts(ctx context.Context, runID, taskID string) (attempts int, active bool, err error)
    // RequeueDispatch moves a scheduled or queued task back to scheduled, held
    // until nextAt, adding one dispatch attempt when countAttempt, and clearing
    // the start of an outage window (ADR 0093) when clearUnavailable.
    RequeueDispatch(ctx context.Context, runID, taskID string, countAttempt, clearUnavailable bool, nextAt time.Time) (bool, error)
    MarkTaskDispatchFailed(ctx context.Context, runID, taskID, reason string) error
    // FailDispatchRefused fails a refused task and spends its retry budget, as
    // on the sync path.
    FailDispatchRefused(ctx context.Context, runID, taskID, reason string) error
    // RecordDispatchUnavailable re-offers a task whose pod create met an
    // unavailable Kubernetes API without counting an attempt, as on the sync
    // path (ADR 0093).
    RecordDispatchUnavailable(ctx context.Context, runID, taskID string, now time.Time, minDelay, maxDelay time.Duration) (since, nextAt time.Time, applied bool, err error)
}
```

<a name="Dispatcher"></a>
## type [Dispatcher](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/scheduler.go#L346-L348>)

Dispatcher launches a task instance for execution. The scheduler dispatches a task as it becomes queued; the concrete implementation builds the executor request and routes it to the right executor.

```go
type Dispatcher interface {
    Dispatch(ctx context.Context, runID, dagID, dagVersionID string, task domain.TaskSpec) (executor.Disposition, error)
}
```

<a name="Leader"></a>
## type [Leader](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/leader.go#L17-L19>)

Leader acquires and releases the advisory lock that restricts the scheduler loop to a single replica. It must run on a dedicated single\-connection pool so the session holding the lock is stable.

```go
type Leader struct {
    // contains filtered or unexported fields
}
```

<a name="NewLeader"></a>
### func [NewLeader](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/leader.go#L22>)

```go
func NewLeader(pool *pgxpool.Pool) *Leader
```

NewLeader builds a Leader over a dedicated \(single\-connection\) pool.

<a name="Leader.HoldsLock"></a>
### func \(\*Leader\) [HoldsLock](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/leader.go#L50>)

```go
func (l *Leader) HoldsLock(ctx context.Context) (bool, error)
```

HoldsLock reports whether this leader's session still holds the advisory lock. The lock is session\-scoped, so if the dedicated connection dropped \(network blip, idle reap, lifetime recycle\) and was replaced, the new session does not hold it and another replica may have taken over — this returns false, letting the caller step down instead of running on as a stale leader \(the split\-brain guard\). A query error \(connection down\) is surfaced so the caller treats it as lost leadership too.

<a name="Leader.Release"></a>
### func \(\*Leader\) [Release](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/leader.go#L36>)

```go
func (l *Leader) Release(ctx context.Context) error
```

Release frees the scheduler advisory lock.

<a name="Leader.TryAcquire"></a>
### func \(\*Leader\) [TryAcquire](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/leader.go#L27>)

```go
func (l *Leader) TryAcquire(ctx context.Context) (bool, error)
```

TryAcquire attempts to take the scheduler advisory lock without blocking.

<a name="LeaderHealthReader"></a>
## type [LeaderHealthReader](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/leader_health.go#L24-L29>)

LeaderHealthReader answers "is a live scheduler leading?" from shared DB state, for a process that does NOT run the scheduler itself — the split api role \(ADR 0049\), whose /api/v2/monitor/health must not report a fake\-healthy scheduler from a nil in\-process handle \(finding F1\). A live scheduler leader holds the leadership advisory lock \(ADR 0009\) on its own session; when its process dies the session drops and the lock releases, so lock presence is a real cross\-process liveness signal with no extra heartbeat table.

It intentionally does NOT filter by pg\_backend\_pid \(that is Leader.HoldsLock's job, confirming THIS session's own hold\) — here any live holder means a scheduler is up. Limitation: it detects a dead/absent leader, not a leader that is alive but stalled \(lock held, loop wedged\); the in\-process Heartbeat still covers stalls in the all/scheduler role. Tick\-accurate cross\-process health would need a persisted heartbeat \(future work\).

```go
type LeaderHealthReader struct {
    // contains filtered or unexported fields
}
```

<a name="NewLeaderHealthReader"></a>
### func [NewLeaderHealthReader](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/leader_health.go#L32>)

```go
func NewLeaderHealthReader(pool *pgxpool.Pool) *LeaderHealthReader
```

NewLeaderHealthReader builds a reader over the given \(api\-role\) pool.

<a name="LeaderHealthReader.Heartbeat"></a>
### func \(\*LeaderHealthReader\) [Heartbeat](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/leader_health.go#L40>)

```go
func (r *LeaderHealthReader) Heartbeat() (healthy bool, last time.Time)
```

Heartbeat implements api.Heartbeater. healthy is true iff some live session holds the scheduler leadership lock. The timestamp is best\-effort "now" when healthy \(the reader has no cross\-process tick time\); it is unused by the handler when the status is unhealthy.

<a name="PlannedTransition"></a>
## type [PlannedTransition](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/plan.go#L12-L15>)

PlannedTransition is a decided state change for a task instance within a run.

```go
type PlannedTransition struct {
    TaskID string
    To     domain.TaskState
}
```

<a name="PlanRun"></a>
### func [PlanRun](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/plan.go#L27>)

```go
func PlanRun(run RunState) []PlannedTransition
```

PlanRun computes the task transitions for one dag run. It first handles retries — a failed task with retry budget moves to up\_for\_retry, and an up\_for\_retry task resets \(none, try\_number\+1\) — then plans the rest off the resulting effective states: none \-\> scheduled \(or skipped / upstream\_failed per the trigger rule\) and scheduled \-\> queued. A failed task that can still recover — app\-retriable, or infra\-failed with re\-place budget left \(even while parked in its re\-place backoff\) — is treated as still active, so downstream tasks wait rather than seeing a failure; a downstream is condemned to upstream\_failed only once its upstream is terminally failed. The result is deterministic: identical inputs yield identical output.

<a name="PoolReservation"></a>
## type [PoolReservation](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/plan.go#L139-L142>)

PoolReservation names the task a pool is reserved for \(ADR 0066 §4\).

```go
type PoolReservation struct {
    RunID  string
    TaskID string
}
```

<a name="PoolWait"></a>
## type [PoolWait](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/plan.go#L36-L40>)

PoolWait is a scheduled task of more than one slot held only by its pool this tick: it passed its dispatch backoff and its DAG's max\_active\_tasks, and did not fit the pool's free slots \(ADR 0066 §4\). The scheduler tracks how long each has waited to decide when a pool is reserved.

```go
type PoolWait struct {
    TaskID string
    Pool   string // budget key, PoolKey(tenant, pool)
    Slots  int
}
```

<a name="Recorder"></a>
## type [Recorder](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/scheduler.go#L312-L341>)

Recorder records scheduler metrics. observability.Metrics implements it.

```go
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
```

<a name="RunState"></a>
## type [RunState](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/scheduler.go#L41-L178>)

RunState is the scheduler's snapshot of a dag run: its topology and the current state of each task.

```go
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
    // MaxActiveTasks is the DAG's task-concurrency cap per DAG run (Airflow 3's
    // max_active_tasks, ADR 0053 Stage 1 as amended by #2427), loaded from the
    // spec. Zero or negative means unlimited, the admission gate is a no-op, so a
    // DAG that never sets it (and all of Lite) plans exactly as before.
    MaxActiveTasks int
    // ActiveTaskCount is the number of this run's task instances that are
    // queued or running. PlanRun subtracts it from MaxActiveTasks to derive the
    // run's admission headroom; other runs of the DAG do not count, as in
    // Airflow 3. The Step loop populates it (runActiveTasks); it defaults to
    // zero, leaving the gate unlimited when MaxActiveTasks is unset.
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
```

<a name="ScheduledDAG"></a>
## type [ScheduledDAG](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/scheduler.go#L184-L207>)

ScheduledDAG is a cron\-scheduled DAG and the logical date of its latest run. Catchup and StartDate drive the per\-tick catchup decision \(\#129\): when a leader has been down across multiple slots, catchup=true backfills every missed slot while catchup=false jumps straight to the most recent one.

```go
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
```

<a name="Scheduler"></a>
## type [Scheduler](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/scheduler.go#L365-L447>)

Scheduler advances dag runs by applying the planning rules each tick.

```go
type Scheduler struct {
    // contains filtered or unexported fields
}
```

<a name="NewScheduler"></a>
### func [NewScheduler](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/scheduler.go#L450>)

```go
func NewScheduler(store Store, logger *slog.Logger, interval time.Duration) *Scheduler
```

NewScheduler builds a Scheduler over the given store, ticking every interval.

<a name="Scheduler.ClearSteppingDown"></a>
### func \(\*Scheduler\) [ClearSteppingDown](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/scheduler.go#L566>)

```go
func (s *Scheduler) ClearSteppingDown()
```

ClearSteppingDown ends the step\-down window opened by MarkSteppingDown. Idempotent, calling it when no step\-down is active is a no\-op.

<a name="Scheduler.ConfineUndefinedPools"></a>
### func \(\*Scheduler\) [ConfineUndefinedPools](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/scheduler.go#L602>)

```go
func (s *Scheduler) ConfineUndefinedPools()
```

ConfineUndefinedPools makes tasks that name an undefined pool draw on their tenant's default\_pool \(see RunState.ConfineUndefinedPools\). Main calls it when server.pools\_read\_only is on. Call once before the scheduler ticks.

<a name="Scheduler.EnableEagerPromotion"></a>
### func \(\*Scheduler\) [EnableEagerPromotion](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/eager_promotion.go#L37>)

```go
func (s *Scheduler) EnableEagerPromotion()
```

EnableEagerPromotion turns on scheduler.eager\_promotion: same\-tick re\-planning of a run after its own state changes, and early ticks on Wake. Off by default \(ADR 0062\), in which case the tick promotes one step per interval and Wake is inert. Call once before the scheduler starts running.

<a name="Scheduler.EnablePools"></a>
### func \(\*Scheduler\) [EnablePools](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/scheduler.go#L597>)

```go
func (s *Scheduler) EnablePools()
```

EnablePools turns on the cross\-DAG named\-pool admission gate \(ADR 0053 Stage 3\). main calls it when scheduler.pools.enabled is on, which unset means the cluster edition. Left unset, the pool gate stays a no\-op and the tick never queries pool budgets, so planning is byte\-identical. Call once before the scheduler starts ticking.

<a name="Scheduler.Heartbeat"></a>
### func \(\*Scheduler\) [Heartbeat](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/scheduler.go#L707>)

```go
func (s *Scheduler) Heartbeat() (bool, time.Time)
```

Heartbeat reports whether the scheduling loop is live and when it last ticked. Only a leader is expected to tick, so a non\-leader \(a follower, or an instance that stepped down after losing the lock\) reports healthy without ticking, it is correctly idle, not stalled. A leader is healthy during the startup grace \(before its first tick\) and while ticks stay within a small multiple of the loop interval; a stalled leader goes unhealthy so the UI/monitor surfaces it.

<a name="Scheduler.IsLeading"></a>
### func \(\*Scheduler\) [IsLeading](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/scheduler.go#L545>)

```go
func (s *Scheduler) IsLeading() bool
```

IsLeading reports whether this instance currently holds scheduler leadership. Background sweeps that mutate cluster state, the pod reconciler and the staging\-volume GC, gate on this so that at replicaCount\>1 only the leader sweeps; otherwise every replica would reconcile and delete the same pods.

<a name="Scheduler.LeaderSince"></a>
### func \(\*Scheduler\) [LeaderSince](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/scheduler.go#L533>)

```go
func (s *Scheduler) LeaderSince() time.Time
```

LeaderSince reports when this instance last acquired scheduler leadership, or the zero time if it is not currently leading. The agent\-lost reaper uses it to suppress reaping within a grace window after a \(re\-\)election \(\#858\).

<a name="Scheduler.MarkSteppingDown"></a>
### func \(\*Scheduler\) [MarkSteppingDown](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/scheduler.go#L557>)

```go
func (s *Scheduler) MarkSteppingDown(reason string)
```

MarkSteppingDown records that a graceful step\-down has begun. The campaign loop calls this BEFORE canceling the scheduler's run\-context, so any in\-flight reaper/Step that returns "context canceled" inside the window logs at WARN \(expected\) instead of ERROR. It also increments the step\-down counter labeled by reason, so operators can alert on the \*rate\* of churn \(rate\(...\[5m\]\)\) instead of grep'ing log content. ClearSteppingDown closes the window; outside it, context.Canceled stays ERROR, the tripwire that catches an unexpected cancel a flat downgrade would silently swallow.

<a name="Scheduler.RecordReacquireSince"></a>
### func \(\*Scheduler\) [RecordReacquireSince](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/scheduler.go#L572>)

```go
func (s *Scheduler) RecordReacquireSince(stepDownAt time.Time)
```

RecordReacquireSince records the time spent stepped down \(\#311\). It is called by the campaign loop immediately after a successful re\-acquire, with the timestamp captured at the moment of step\-down. A zero stepDownAt \(no prior step\-down, first acquisition at boot\) is ignored.

<a name="Scheduler.Run"></a>
### func \(\*Scheduler\) [Run](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/scheduler.go#L639>)

```go
func (s *Scheduler) Run(ctx context.Context) error
```

Run drives the scheduling loop until ctx is canceled. The loop is crash\-proof: a panic or error in a tick is recovered and logged, so the scheduler keeps ticking, it may fall behind, but it never dies \(the critical invariant\).

With eager promotion on, a Wake also starts a tick, no sooner than wakeGap after the start of the previous one, and restarts the interval so it stays the upper bound between ticks. With it off the wake channel is nil, which never fires, so the loop ticks on the interval alone.

<a name="Scheduler.SetAlertConcurrency"></a>
### func \(\*Scheduler\) [SetAlertConcurrency](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/scheduler.go#L611>)

```go
func (s *Scheduler) SetAlertConcurrency(n int)
```

SetAlertConcurrency caps how many on\-failure alert dispatches may run at once \(\#424\). n \< 1 is treated as 1. Mainly a config/test seam; the default is defaultAlertConcurrency.

<a name="Scheduler.SetAlerter"></a>
### func \(\*Scheduler\) [SetAlerter](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/scheduler.go#L606>)

```go
func (s *Scheduler) SetAlerter(a Alerter)
```

SetAlerter attaches the on\-failure alerter \(optional; \#424\). Without it, or for a DAG with no alert rules, the scheduler finalizes failures silently.

<a name="Scheduler.SetDispatchTenantLimit"></a>
### func \(\*Scheduler\) [SetDispatchTenantLimit](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/scheduler.go#L492>)

```go
func (s *Scheduler) SetDispatchTenantLimit(n int)
```

SetDispatchTenantLimit caps how many tasks one tenant may dispatch in one tick \(scheduler.dispatch.max\_per\_tenant\_per\_tick\), so a tenant with a wide fan\-out or many runs leaves dispatch room for the others. 0 or less means no cap. Call before the scheduler starts ticking.

<a name="Scheduler.SetDispatchTickBudget"></a>
### func \(\*Scheduler\) [SetDispatchTickBudget](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/scheduler.go#L481>)

```go
func (s *Scheduler) SetDispatchTickBudget(d time.Duration)
```

SetDispatchTickBudget sets how long one tick may spend dispatching \(scheduler.dispatch.tick\_budget\). 0 or less disables the budget, which dispatches every ready task in the tick however long it takes. Call before the scheduler starts ticking.

<a name="Scheduler.SetDispatcher"></a>
### func \(\*Scheduler\) [SetDispatcher](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/scheduler.go#L590>)

```go
func (s *Scheduler) SetDispatcher(d Dispatcher)
```

SetDispatcher attaches the executor dispatcher \(optional; without it the scheduler advances state only and launches nothing\).

<a name="Scheduler.SetLeading"></a>
### func \(\*Scheduler\) [SetLeading](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/scheduler.go#L517>)

```go
func (s *Scheduler) SetLeading(on bool)
```

SetLeading marks whether this instance currently holds scheduler leadership. The leadership manager sets it true while the loop runs and false when it steps down \(lost lock\) or stops. Becoming leader resets the tick clock so the startup grace applies afresh and a stale pre\-step\-down heartbeat is not mistaken for a stall. It governs Heartbeat: only a leader is expected to tick.

<a name="Scheduler.SetPoolStarvationThreshold"></a>
### func \(\*Scheduler\) [SetPoolStarvationThreshold](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/pool_reservation.go#L24>)

```go
func (s *Scheduler) SetPoolStarvationThreshold(d time.Duration)
```

SetPoolStarvationThreshold sets scheduler.pool\_starvation\_threshold: how long a task held only by its pool waits before the pool is reserved for it. Zero \(the default for a Scheduler built without it\) disables reservations. Call before the scheduler ticks.

<a name="Scheduler.SetRecorder"></a>
### func \(\*Scheduler\) [SetRecorder](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/scheduler.go#L586>)

```go
func (s *Scheduler) SetRecorder(r Recorder)
```

SetRecorder attaches a metrics recorder \(optional\).

<a name="Scheduler.SetStepTimeout"></a>
### func \(\*Scheduler\) [SetStepTimeout](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/scheduler.go#L510>)

```go
func (s *Scheduler) SetStepTimeout(d time.Duration)
```

SetStepTimeout overrides the per\-tick timeout \(optional; mainly for tests\).

<a name="Scheduler.StalledFor"></a>
### func \(\*Scheduler\) [StalledFor](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/scheduler.go#L726>)

```go
func (s *Scheduler) StalledFor(limit time.Duration) (bool, time.Time)
```

StalledFor reports whether this process leads and its scheduling loop has not started an iteration for longer than limit, with the time of the last one. A follower, and a leader before its first tick, are never stalled. A database outage does not stall the loop: Step marks its start before it reads anything and fails fast, so only a loop that stopped iterating \(a blocked goroutine\) is reported. It backs the opt\-in liveness check of server.liveness\_scheduler\_stall \(\#539\).

<a name="Scheduler.Step"></a>
### func \(\*Scheduler\) [Step](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/scheduler.go#L746>)

```go
func (s *Scheduler) Step(ctx context.Context) error
```

Step runs one deterministic scheduling iteration over every active run. Each run is advanced in isolation \(see advanceSafely\): a panic or error in one run is contained, so it never blocks the other runs or new\-run creation. The reaper runs independently of createDueRuns success, they share no dependency, and silencing the reaper when scheduling has a hiccup would let orphans accumulate exactly when the operator is most likely to notice the counter is wrong. The first non\-nil infra\-level error is returned \(logged by the caller\); the later phases still execute.

<a name="Scheduler.SteppingDown"></a>
### func \(\*Scheduler\) [SteppingDown](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/scheduler.go#L583>)

```go
func (s *Scheduler) SteppingDown() bool
```

SteppingDown exposes the current step\-down state for tests and callers that want to classify an error themselves: the scheduler's own log sites pass it to logSchedulerError, and the execution reaper receives it \(as the inStepDown callback\) so an expected step\-down cancel logs at WARN, not ERROR \(\#311\).

<a name="Scheduler.Wake"></a>
### func \(\*Scheduler\) [Wake](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/eager_promotion.go#L44>)

```go
func (s *Scheduler) Wake()
```

Wake asks the loop to tick now instead of at the next interval, typically because a task just settled and its downstreams may be ready. It never blocks: wakes coalesce into at most one pending tick. With eager promotion off the loop does not listen and Wake does nothing. A woken follower runs its usual empty tick, so leader\-only semantics are unchanged.

<a name="Store"></a>
## type [Store](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/scheduler.go#L211-L309>)

Store is the scheduler's view of persistent state. The concrete implementation is sqlc\-backed; tests use a fake.

```go
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
```

<a name="TaskGraph"></a>
## type [TaskGraph](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/task_graph.go#L20-L31>)

TaskGraph is the index\-addressed form of a run's task list: each task\_id resolved to a position, and each task's upstreams resolved to positions, so the planner and dispatch look tasks up in O\(1\) instead of scanning the list.

A graph depends only on the task list \(ids and dependencies\), which is immutable per dag\_version, so the store builds it the first time the scheduler reads a version and every run of that version shares it read\-only. A graph built for one task list must only be used with a list of the same ids in the same order; PlanRun falls back to building one when RunState.Graph is nil.

Positions mirror the map\-keyed planner this replaced: a task\_id that appears more than once \(compile validation rejects that, but the planner never trusted it\) shares one state slot, the slot of its first occurrence; lookup returns that first occurrence, and its upstreams are those of its last occurrence.

```go
type TaskGraph struct {
    // contains filtered or unexported fields
}
```

<a name="NewTaskGraph"></a>
### func [NewTaskGraph](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/task_graph.go#L35>)

```go
func NewTaskGraph(tasks []domain.TaskSpec) *TaskGraph
```

NewTaskGraph indexes a task list. It is O\(T \+ E\) and allocation\-bounded by the list, so building it once per version is cheap.

<a name="TaskGraph.Lookup"></a>
### func \(\*TaskGraph\) [Lookup](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/task_graph.go#L77>)

```go
func (g *TaskGraph) Lookup(taskID string) (int, bool)
```

Lookup returns the position of the task with the given id \(its first occurrence\), or false when the task is not in the list.

<a name="TenantStopper"></a>
## type [TenantStopper](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/scheduler.go#L1132-L1134>)

TenantStopper is the optional store seam of the stop suspension mode \(\#2181\): it fails the queued and running task instances of a tenant suspended in stop mode and returns how many it failed. A store without it leaves stop applying only what drain does.

```go
type TenantStopper interface {
    FailStoppedTenantTaskInstances(ctx context.Context, tenantID string) (int64, error)
}
```

<a name="TriggerDecision"></a>
## type [TriggerDecision](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/state_machine.go#L13>)

TriggerDecision is the scheduler's decision for a task given its trigger rule and the states of its upstream tasks.

```go
type TriggerDecision int
```

<a name="DecisionWait"></a>Possible scheduler decisions for a task.

```go
const (
    // DecisionWait means the upstreams are not yet settled; re-evaluate later.
    DecisionWait TriggerDecision = iota
    // DecisionSchedule means dependencies are satisfied; move the task to scheduled.
    DecisionSchedule
    // DecisionSkip means the trigger rule can no longer be satisfied; skip the task.
    DecisionSkip
    // DecisionUpstreamFailed means a required upstream failed; propagate the failure.
    DecisionUpstreamFailed
)
```

<a name="EvaluateTriggerRule"></a>
### func [EvaluateTriggerRule](<https://github.com/dexadata/dexaflow/blob/main/internal/scheduler/state_machine.go#L136>)

```go
func EvaluateTriggerRule(rule domain.TriggerRule, upstreams []domain.TaskState) TriggerDecision
```

EvaluateTriggerRule decides what to do with a task given its trigger rule and the current states of its upstream tasks. A task with no upstreams \(a root task\) is always scheduled.

Generated by [gomarkdoc](<https://github.com/princjef/gomarkdoc>)
