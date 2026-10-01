---
title: "internal/scheduler"
linkTitle: "internal/scheduler"
weight: 2
---

```go
import "github.com/dexadata/dexaflow/internal/scheduler"
```

Package scheduler implements the Leoflow scheduling state machine and loop.

## Index

- [Constants](<#constants>)
- [func CanTransition\(from, to domain.TaskState\) bool](<#CanTransition>)
- [func CanTransitionDagRun\(from, to domain.DagRunState\) bool](<#CanTransitionDagRun>)
- [func FinalizeRun\(run RunState\) \(domain.DagRunState, bool\)](<#FinalizeRun>)
- [func InfraReplaceMaxDelay\(\) time.Duration](<#InfraReplaceMaxDelay>)
- [func PoolKey\(tenant, pool string\) string](<#PoolKey>)
- [type Alerter](<#Alerter>)
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
- [type Recorder](<#Recorder>)
- [type RunState](<#RunState>)
- [type ScheduledDAG](<#ScheduledDAG>)
- [type Scheduler](<#Scheduler>)
  - [func NewScheduler\(store Store, logger \*slog.Logger, interval time.Duration\) \*Scheduler](<#NewScheduler>)
  - [func \(s \*Scheduler\) ClearSteppingDown\(\)](<#Scheduler.ClearSteppingDown>)
  - [func \(s \*Scheduler\) EnablePools\(\)](<#Scheduler.EnablePools>)
  - [func \(s \*Scheduler\) Heartbeat\(\) \(bool, time.Time\)](<#Scheduler.Heartbeat>)
  - [func \(s \*Scheduler\) IsLeading\(\) bool](<#Scheduler.IsLeading>)
  - [func \(s \*Scheduler\) LeaderSince\(\) time.Time](<#Scheduler.LeaderSince>)
  - [func \(s \*Scheduler\) MarkSteppingDown\(reason string\)](<#Scheduler.MarkSteppingDown>)
  - [func \(s \*Scheduler\) RecordReacquireSince\(stepDownAt time.Time\)](<#Scheduler.RecordReacquireSince>)
  - [func \(s \*Scheduler\) Run\(ctx context.Context\) error](<#Scheduler.Run>)
  - [func \(s \*Scheduler\) SetAlertConcurrency\(n int\)](<#Scheduler.SetAlertConcurrency>)
  - [func \(s \*Scheduler\) SetAlerter\(a Alerter\)](<#Scheduler.SetAlerter>)
  - [func \(s \*Scheduler\) SetDispatcher\(d Dispatcher\)](<#Scheduler.SetDispatcher>)
  - [func \(s \*Scheduler\) SetLeading\(on bool\)](<#Scheduler.SetLeading>)
  - [func \(s \*Scheduler\) SetRecorder\(r Recorder\)](<#Scheduler.SetRecorder>)
  - [func \(s \*Scheduler\) SetStepTimeout\(d time.Duration\)](<#Scheduler.SetStepTimeout>)
  - [func \(s \*Scheduler\) Step\(ctx context.Context\) error](<#Scheduler.Step>)
  - [func \(s \*Scheduler\) SteppingDown\(\) bool](<#Scheduler.SteppingDown>)
- [type Store](<#Store>)
- [type TriggerDecision](<#TriggerDecision>)
  - [func EvaluateTriggerRule\(rule domain.TriggerRule, upstreams \[\]domain.TaskState\) TriggerDecision](<#EvaluateTriggerRule>)


## Constants

<a name="LockID"></a>LockID is the fixed Postgres advisory\-lock id gating scheduler leadership \("LeoFlow" in hex\), per ADR 0009.

```go
const LockID int64 = 0x4C656F466C6F77
```

<a name="CanTransition"></a>
## func [CanTransition](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/state_machine.go#L46>)

```go
func CanTransition(from, to domain.TaskState) bool
```

CanTransition reports whether a task instance may move from one state to another under the Leoflow state machine.

<a name="CanTransitionDagRun"></a>
## func [CanTransitionDagRun](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/state_machine.go#L51>)

```go
func CanTransitionDagRun(from, to domain.DagRunState) bool
```

CanTransitionDagRun reports whether a dag run may move from one state to another.

<a name="FinalizeRun"></a>
## func [FinalizeRun](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/plan.go#L367>)

```go
func FinalizeRun(run RunState) (domain.DagRunState, bool)
```

FinalizeRun reports the terminal dag\-run state once every task is terminal. A failed task that still has retry budget \(or an infra re\-place budget\) counts as non\-terminal, so the run keeps running until it resolves. The boolean is false while any task is still non\-terminal.

<a name="InfraReplaceMaxDelay"></a>
## func [InfraReplaceMaxDelay](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/dispatch_backoff.go#L52>)

```go
func InfraReplaceMaxDelay() time.Duration
```

InfraReplaceMaxDelay is the longest the planner may park an infra\-failed task before re\-placing it: the backoff before the last permitted re\-place \(attempt infraMaxAttempts\) plus the full de\-synchronizing jitter window. It is the upper bound on how long a run whose only live task is infra\-parked shows no activity, so the executor's orphan\-run threshold must sit above it or the orphan reaper fails a run that is still recovering. The two values live in packages that depend in one direction only \(the scheduler imports the executor\), so this is exported for the server to hand to the executor's boot\-time resilience ladder rather than read from there.

<a name="PoolKey"></a>
## func [PoolKey](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/plan.go#L103>)

```go
func PoolKey(tenant, pool string) string
```

PoolKey composes the cross\-DAG admission\-budget key for a \(tenant, pool\) pair. Pools are tenant\-scoped, so a pool name is only meaningful within its tenant; the key namespaces the pool budget and occupancy maps by tenant. The NUL separator cannot occur in a tenant UUID or an Airflow pool name, so the join is unambiguous. The scheduler store builds its budget map with the same key.

<a name="Alerter"></a>
## type [Alerter](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/scheduler.go#L434-L439>)

Alerter dispatches a DAG's on\-failure alert rules for a run that finalized in the failed state. Implementations resolve each rule's managed connection to an endpoint and send \(Slack/webhook\). The scheduler calls it from a detached goroutine, so an implementation may block on network I/O without stalling the tick; it MUST treat every send as best\-effort — a delivery failure is logged, never propagated, so alerting can never fail a run.

```go
type Alerter interface {
    // AlertRunFailed sends every on_failure rule and reports whether all of them
    // were delivered. The caller stamps delivery only on true, so a failed send
    // stays retryable instead of being marked and lost.
    AlertRunFailed(ctx context.Context, run RunState) bool
}
```

<a name="Dispatcher"></a>
## type [Dispatcher](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/scheduler.go#L248-L250>)

Dispatcher launches a task instance for execution. The scheduler dispatches a task as it becomes queued; the concrete implementation builds the executor request and routes it to the right executor.

```go
type Dispatcher interface {
    Dispatch(ctx context.Context, runID, dagID, dagVersionID string, task domain.TaskSpec) (executor.Disposition, error)
}
```

<a name="Leader"></a>
## type [Leader](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/leader.go#L17-L19>)

Leader acquires and releases the advisory lock that restricts the scheduler loop to a single replica. It must run on a dedicated single\-connection pool so the session holding the lock is stable.

```go
type Leader struct {
    // contains filtered or unexported fields
}
```

<a name="NewLeader"></a>
### func [NewLeader](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/leader.go#L22>)

```go
func NewLeader(pool *pgxpool.Pool) *Leader
```

NewLeader builds a Leader over a dedicated \(single\-connection\) pool.

<a name="Leader.HoldsLock"></a>
### func \(\*Leader\) [HoldsLock](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/leader.go#L50>)

```go
func (l *Leader) HoldsLock(ctx context.Context) (bool, error)
```

HoldsLock reports whether this leader's session still holds the advisory lock. The lock is session\-scoped, so if the dedicated connection dropped \(network blip, idle reap, lifetime recycle\) and was replaced, the new session does not hold it and another replica may have taken over — this returns false, letting the caller step down instead of running on as a stale leader \(the split\-brain guard\). A query error \(connection down\) is surfaced so the caller treats it as lost leadership too.

<a name="Leader.Release"></a>
### func \(\*Leader\) [Release](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/leader.go#L36>)

```go
func (l *Leader) Release(ctx context.Context) error
```

Release frees the scheduler advisory lock.

<a name="Leader.TryAcquire"></a>
### func \(\*Leader\) [TryAcquire](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/leader.go#L27>)

```go
func (l *Leader) TryAcquire(ctx context.Context) (bool, error)
```

TryAcquire attempts to take the scheduler advisory lock without blocking.

<a name="LeaderHealthReader"></a>
## type [LeaderHealthReader](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/leader_health.go#L24-L29>)

LeaderHealthReader answers "is a live scheduler leading?" from shared DB state, for a process that does NOT run the scheduler itself — the split api role \(ADR 0049\), whose /api/v2/monitor/health must not report a fake\-healthy scheduler from a nil in\-process handle \(finding F1\). A live scheduler leader holds the leadership advisory lock \(ADR 0009\) on its own session; when its process dies the session drops and the lock releases, so lock presence is a real cross\-process liveness signal with no extra heartbeat table.

It intentionally does NOT filter by pg\_backend\_pid \(that is Leader.HoldsLock's job, confirming THIS session's own hold\) — here any live holder means a scheduler is up. Limitation: it detects a dead/absent leader, not a leader that is alive but stalled \(lock held, loop wedged\); the in\-process Heartbeat still covers stalls in the all/scheduler role. Tick\-accurate cross\-process health would need a persisted heartbeat \(future work\).

```go
type LeaderHealthReader struct {
    // contains filtered or unexported fields
}
```

<a name="NewLeaderHealthReader"></a>
### func [NewLeaderHealthReader](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/leader_health.go#L32>)

```go
func NewLeaderHealthReader(pool *pgxpool.Pool) *LeaderHealthReader
```

NewLeaderHealthReader builds a reader over the given \(api\-role\) pool.

<a name="LeaderHealthReader.Heartbeat"></a>
### func \(\*LeaderHealthReader\) [Heartbeat](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/leader_health.go#L40>)

```go
func (r *LeaderHealthReader) Heartbeat() (healthy bool, last time.Time)
```

Heartbeat implements api.Heartbeater. healthy is true iff some live session holds the scheduler leadership lock. The timestamp is best\-effort "now" when healthy \(the reader has no cross\-process tick time\); it is unused by the handler when the status is unhealthy.

<a name="PlannedTransition"></a>
## type [PlannedTransition](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/plan.go#L12-L15>)

PlannedTransition is a decided state change for a task instance within a run.

```go
type PlannedTransition struct {
    TaskID string
    To     domain.TaskState
}
```

<a name="PlanRun"></a>
### func [PlanRun](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/plan.go#L27>)

```go
func PlanRun(run RunState) []PlannedTransition
```

PlanRun computes the task transitions for one dag run. It first handles retries — a failed task with retry budget moves to up\_for\_retry, and an up\_for\_retry task resets \(none, try\_number\+1\) — then plans the rest off the resulting effective states: none \-\> scheduled \(or skipped / upstream\_failed per the trigger rule\) and scheduled \-\> queued. A failed task that can still recover — app\-retriable, or infra\-failed with re\-place budget left \(even while parked in its re\-place backoff\) — is treated as still active, so downstream tasks wait rather than seeing a failure; a downstream is condemned to upstream\_failed only once its upstream is terminally failed. The result is deterministic: identical inputs yield identical output.

<a name="Recorder"></a>
## type [Recorder](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/scheduler.go#L226-L243>)

Recorder records scheduler metrics. observability.Metrics implements it.

```go
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
```

<a name="RunState"></a>
## type [RunState](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/scheduler.go#L40-L136>)

RunState is the scheduler's snapshot of a dag run: its topology and the current state of each task.

```go
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
```

<a name="ScheduledDAG"></a>
## type [ScheduledDAG](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/scheduler.go#L142-L154>)

ScheduledDAG is a cron\-scheduled DAG and the logical date of its latest run. Catchup and StartDate drive the per\-tick catchup decision \(\#129\): when a leader has been down across multiple slots, catchup=true backfills every missed slot while catchup=false jumps straight to the most recent one.

```go
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
```

<a name="Scheduler"></a>
## type [Scheduler](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/scheduler.go#L267-L300>)

Scheduler advances dag runs by applying the planning rules each tick.

```go
type Scheduler struct {
    // contains filtered or unexported fields
}
```

<a name="NewScheduler"></a>
### func [NewScheduler](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/scheduler.go#L303>)

```go
func NewScheduler(store Store, logger *slog.Logger, interval time.Duration) *Scheduler
```

NewScheduler builds a Scheduler over the given store, ticking every interval.

<a name="Scheduler.ClearSteppingDown"></a>
### func \(\*Scheduler\) [ClearSteppingDown](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/scheduler.go#L381>)

```go
func (s *Scheduler) ClearSteppingDown()
```

ClearSteppingDown ends the step\-down window opened by MarkSteppingDown. Idempotent — calling it when no step\-down is active is a no\-op.

<a name="Scheduler.EnablePools"></a>
### func \(\*Scheduler\) [EnablePools](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/scheduler.go#L412>)

```go
func (s *Scheduler) EnablePools()
```

EnablePools turns on the cross\-DAG named\-pool admission gate \(ADR 0053 Stage 3\). It is Pro\-only: main calls it exactly when the edition is "pro". Left unset in Lite/non\-Pro, where the pool gate stays a no\-op and the tick never queries pool budgets, so Lite plans byte\-identically. Call once before the scheduler starts ticking.

<a name="Scheduler.Heartbeat"></a>
### func \(\*Scheduler\) [Heartbeat](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/scheduler.go#L487>)

```go
func (s *Scheduler) Heartbeat() (bool, time.Time)
```

Heartbeat reports whether the scheduling loop is live and when it last ticked. Only a leader is expected to tick, so a non\-leader \(a follower, or an instance that stepped down after losing the lock\) reports healthy without ticking — it is correctly idle, not stalled. A leader is healthy during the startup grace \(before its first tick\) and while ticks stay within a small multiple of the loop interval; a stalled leader goes unhealthy so the UI/monitor surfaces it.

<a name="Scheduler.IsLeading"></a>
### func \(\*Scheduler\) [IsLeading](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/scheduler.go#L360>)

```go
func (s *Scheduler) IsLeading() bool
```

IsLeading reports whether this instance currently holds scheduler leadership. Background sweeps that mutate cluster state — the pod reconciler and the staging\-volume GC — gate on this so that at replicaCount\>1 only the leader sweeps; otherwise every replica would reconcile and delete the same pods.

<a name="Scheduler.LeaderSince"></a>
### func \(\*Scheduler\) [LeaderSince](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/scheduler.go#L348>)

```go
func (s *Scheduler) LeaderSince() time.Time
```

LeaderSince reports when this instance last acquired scheduler leadership, or the zero time if it is not currently leading. The agent\-lost reaper uses it to suppress reaping within a grace window after a \(re\-\)election \(\#858\).

<a name="Scheduler.MarkSteppingDown"></a>
### func \(\*Scheduler\) [MarkSteppingDown](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/scheduler.go#L372>)

```go
func (s *Scheduler) MarkSteppingDown(reason string)
```

MarkSteppingDown records that a graceful step\-down has begun. The campaign loop calls this BEFORE canceling the scheduler's run\-context, so any in\-flight reaper/Step that returns "context canceled" inside the window logs at WARN \(expected\) instead of ERROR. It also increments the step\-down counter labeled by reason, so operators can alert on the \*rate\* of churn \(rate\(...\[5m\]\)\) instead of grep'ing log content. ClearSteppingDown closes the window; outside it, context.Canceled stays ERROR — the tripwire that catches an unexpected cancel a flat downgrade would silently swallow.

<a name="Scheduler.RecordReacquireSince"></a>
### func \(\*Scheduler\) [RecordReacquireSince](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/scheduler.go#L387>)

```go
func (s *Scheduler) RecordReacquireSince(stepDownAt time.Time)
```

RecordReacquireSince records the time spent stepped down \(\#311\). It is called by the campaign loop immediately after a successful re\-acquire, with the timestamp captured at the moment of step\-down. A zero stepDownAt \(no prior step\-down — first acquisition at boot\) is ignored.

<a name="Scheduler.Run"></a>
### func \(\*Scheduler\) [Run](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/scheduler.go#L444>)

```go
func (s *Scheduler) Run(ctx context.Context) error
```

Run drives the scheduling loop until ctx is canceled. The loop is crash\-proof: a panic or error in a tick is recovered and logged, so the scheduler keeps ticking — it may fall behind, but it never dies \(the critical invariant\).

<a name="Scheduler.SetAlertConcurrency"></a>
### func \(\*Scheduler\) [SetAlertConcurrency](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/scheduler.go#L421>)

```go
func (s *Scheduler) SetAlertConcurrency(n int)
```

SetAlertConcurrency caps how many on\-failure alert dispatches may run at once \(\#424\). n \< 1 is treated as 1. Mainly a config/test seam; the default is defaultAlertConcurrency.

<a name="Scheduler.SetAlerter"></a>
### func \(\*Scheduler\) [SetAlerter](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/scheduler.go#L416>)

```go
func (s *Scheduler) SetAlerter(a Alerter)
```

SetAlerter attaches the on\-failure alerter \(optional; \#424\). Without it, or for a DAG with no alert rules, the scheduler finalizes failures silently.

<a name="Scheduler.SetDispatcher"></a>
### func \(\*Scheduler\) [SetDispatcher](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/scheduler.go#L405>)

```go
func (s *Scheduler) SetDispatcher(d Dispatcher)
```

SetDispatcher attaches the executor dispatcher \(optional; without it the scheduler advances state only and launches nothing\).

<a name="Scheduler.SetLeading"></a>
### func \(\*Scheduler\) [SetLeading](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/scheduler.go#L332>)

```go
func (s *Scheduler) SetLeading(on bool)
```

SetLeading marks whether this instance currently holds scheduler leadership. The leadership manager sets it true while the loop runs and false when it steps down \(lost lock\) or stops. Becoming leader resets the tick clock so the startup grace applies afresh and a stale pre\-step\-down heartbeat is not mistaken for a stall. It governs Heartbeat: only a leader is expected to tick.

<a name="Scheduler.SetRecorder"></a>
### func \(\*Scheduler\) [SetRecorder](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/scheduler.go#L401>)

```go
func (s *Scheduler) SetRecorder(r Recorder)
```

SetRecorder attaches a metrics recorder \(optional\).

<a name="Scheduler.SetStepTimeout"></a>
### func \(\*Scheduler\) [SetStepTimeout](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/scheduler.go#L325>)

```go
func (s *Scheduler) SetStepTimeout(d time.Duration)
```

SetStepTimeout overrides the per\-tick timeout \(optional; mainly for tests\).

<a name="Scheduler.Step"></a>
### func \(\*Scheduler\) [Step](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/scheduler.go#L507>)

```go
func (s *Scheduler) Step(ctx context.Context) error
```

Step runs one deterministic scheduling iteration over every active run. Each run is advanced in isolation \(see advanceSafely\): a panic or error in one run is contained, so it never blocks the other runs or new\-run creation. The reaper runs independently of createDueRuns success — they share no dependency, and silencing the reaper when scheduling has a hiccup would let orphans accumulate exactly when the operator is most likely to notice the counter is wrong. The first non\-nil infra\-level error is returned \(logged by the caller\); the later phases still execute.

<a name="Scheduler.SteppingDown"></a>
### func \(\*Scheduler\) [SteppingDown](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/scheduler.go#L398>)

```go
func (s *Scheduler) SteppingDown() bool
```

SteppingDown exposes the current step\-down state for tests and callers that want to classify an error themselves: the scheduler's own log sites pass it to logSchedulerError, and the execution reaper receives it \(as the inStepDown callback\) so an expected step\-down cancel logs at WARN, not ERROR \(\#311\).

<a name="Store"></a>
## type [Store](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/scheduler.go#L158-L223>)

Store is the scheduler's view of persistent state. The concrete implementation is sqlc\-backed; tests use a fake.

```go
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
```

<a name="TriggerDecision"></a>
## type [TriggerDecision](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/state_machine.go#L8>)

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
### func [EvaluateTriggerRule](<https://github.com/dexadata/leoflow/blob/main/internal/scheduler/state_machine.go#L87>)

```go
func EvaluateTriggerRule(rule domain.TriggerRule, upstreams []domain.TaskState) TriggerDecision
```

EvaluateTriggerRule decides what to do with a task given its trigger rule and the current states of its upstream tasks. A task with no upstreams \(a root task\) is always scheduled.

Generated by [gomarkdoc](<https://github.com/princjef/gomarkdoc>)
