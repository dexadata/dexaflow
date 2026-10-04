---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /adr/0052-durable-task-outcome.html
# --- end AUTO redirect aliases ---
title: "ADR 0052: Durable task outcome — decouple the task result from report delivery"
linkTitle: 0052 · Durable task outcome — decouple the task result from report delivery
weight: 520
description: "ADR 0052: Durable task outcome — decouple the task result from report delivery"
---

**Status:** Proposed
**Date:** 2026-08-13
**Relates:** ADR 0051 (separate the orchestration and execution state machines — this ADR is its Phase 2), ADR 0031 (scheduler reconciliation loop + two-layer reaping), ADR 0015 (Kubernetes-only container execution), ADR 0004 (thin agent), ADR 0002 (pod-per-task — an assumption this ADR is careful not to deepen)
**Issues:** #543 (agent exit code conflates task outcome with report delivery); follow-up to #542 (in-process report retry)
**Amendment proposed:** 2026-10-04, a durable SUCCESS overrides an infra guess (#1124, #900). See the section at the end.

## Context

A task pod's true outcome and the *delivery* of that outcome are tangled into one
value: the agent process's exit code.

The agent runs the user process, then delivers the result over gRPC. If the
terminal report — or one of the pre-report pushes (return value, extra links,
XCom) — fails, the agent routes to `fail` and exits non-zero
(`internal/agent/runner.go:408-420`). So a pod **killed mid-report** (OOM,
eviction) shows Kubernetes phase `Failed` **even when the user task itself
succeeded**.

The reconciler is the backstop for a pod that never delivered, but it can only
read **pod phase** (`classifyPod`, `internal/executor/reconcile.go:39-55`, invoked
in the loop at `:103-116`): a `Failed` pod is settled as a task failure via
`reportFailure`. It has no way to recover a success
from phase alone, because the only durable signal — the pod — says `Failed`.

### This is a classic distributed-systems problem, and there are two schools

At bottom this is the *atomicity of a side effect and its record*: the work
happened, but the record of it did not survive the worker's death. The industry
answers it in one of two ways.

- **School A — recover the lost outcome.** Extract the truth from a durable,
  per-worker artifact left behind at exit. Best-effort: it only works while the
  node/kubelet survives to surface the artifact. It cannot survive node loss.
- **School B — the outcome does not exist until the orchestrator durably records
  it; on ambiguity, re-drive.** The source of truth is a store the orchestrator
  owns; a result is real only once written there; a lost report means *not done*,
  so the task is retried. Correctness comes from **at-least-once execution +
  idempotency**, not from autopsying the worker. Correct under *any* failure,
  node loss included. This is the durable-execution lineage (Temporal/Cadence)
  and the Borg/Spanner "durable intent + idempotent re-drive" pattern.

Apache Airflow — our UI/API compatibility target — is worse than School A here:
its KubernetesExecutor does not even try to recover. A pod that dies without
reporting becomes a **zombie task**, is marked `failed`, and is re-run; the whole
"tasks must be idempotent" doctrine exists precisely because the framework
re-drives on ambiguity. It accepts the false-negative and pushes the burden to
the user.

**leoflow is already on School B.** The reconciliation loop and two-layer reaper
(ADR 0031) re-drive on ambiguity, and ADR 0051 Phase 1 re-places an infra fault
*without consuming the user's retry budget*. That is the correctness foundation,
and it holds under node loss. This ADR does **not** replace it. This ADR adds a
**School-A optimization on top of the School-B floor**: when the kubelet survives,
recover a lost *success* so an expensive or non-idempotent task is not needlessly
re-run.

> "Have the reconciler settle a `Succeeded` pod as success" is nearly a no-op:
> when the success report fails the agent exits non-zero, so the pod is `Failed`,
> never `Succeeded`. The bug is the conflation, not the reconciler's reading of a
> `Succeeded` phase.

#542 adds an in-process retry of the report, which fixes the common transient
blip while the pod is still alive. It cannot fix the case where the pod is gone
before the retry budget is spent. That surviving-kubelet backstop is this ADR.

## Decision

**Correctness rests on School B — idempotent re-drive (ADR 0031 + ADR 0051 Phase
1) — which this ADR does not change. On top of it, the agent writes the true task
outcome to a durable, pod-local location *before* it attempts to deliver the
report; the reconciler reads that record as the source of truth over pod phase,
recovering a lost success so a costly or non-idempotent task is not re-run
needlessly.**

The record is an **optimization that reduces spurious re-runs**, not the
mechanism that guarantees correctness. Where the record is absent (node loss, old
agent), the School-B floor still settles the task safely by re-drive.

### The durable outcome record

A compact JSON document written to the container **termination message**
(`/dev/termination-log`, surfaced by Kubernetes on
`pod.Status.ContainerStatuses[].State.Terminated.Message`):

```json
{"v":1,"outcome":"success"}
{"v":1,"outcome":"failed","exit_code":1}
{"v":1,"outcome":"reschedule","reschedule_at":"2026-08-13T12:34:56Z"}
```

- `outcome` ∈ `success | failed | reschedule` — the *task's* result, independent
  of whether the report was delivered.
- `exit_code` — the user process exit code, for a `failed` outcome.
- `reschedule_at` — RFC3339 next-poke time, **required** for a `reschedule`
  outcome (see "Reschedule carries its next-poke time" below).
- Kept well under the Kubernetes termination-message cap (~4 KiB); it carries the
  outcome, never logs or the return value (those keep their existing paths).

### Prerequisite: the container must pin the termination-message policy

The whole contract rests on the container surfacing `/dev/termination-log` on pod
status. Today `BuildPod` (`internal/executor/kubernetes.go:81-88`) sets **neither**
`TerminationMessagePolicy` nor `TerminationMessagePath`; it works only by the
Kubernetes API default (`File` / `/dev/termination-log`), which an admission
webhook or PodSecurity policy could mutate without notice. This ADR makes it an
**explicit, tested contract**: `BuildPod` pins
`TerminationMessagePolicy: corev1.TerminationMessageReadFile` and
`TerminationMessagePath: "/dev/termination-log"` on the task container, with a
unit test asserting both. The k3d/chaos step additionally asserts the agent can
*write* the file under the operator-configurable `RunAsNonRoot` +
`ReadOnlyRootFilesystem` security context (`buildSecurityContext`,
`kubernetes.go:221-234`). We do **not** set `FallbackToLogsOnError`: it would let
a failed pod populate the message from the log tail, which the reader would then
try (and safely fail) to decode — avoided outright.

### Write ordering: the success record follows the pushes

The record is written **path-specifically at the point the outcome becomes true**,
not on a single "user process exited" event. To avoid missing a terminal sink, the
write is keyed off the `state` the agent is about to report inside the terminal
path — not bolted onto individual call sites:

- The `success` record is written **only after every pre-report push (return
  value, extra links, custom XComs) has been accepted** — immediately before
  `report(SUCCESS)`. A kill *during* the pushes therefore leaves **no** success
  record → fallback → failure → idempotent re-drive.
- The `failed` record must cover **every** failure sink: the `fail(...)` calls
  (`runner.go:409-418`) **and** the execution-timeout path `failWithReason(...)`
  (`runner.go:396, 426-431`), which reports `FAILED` without going through
  `fail()`. Keying the write off the reported state (rather than enumerating call
  sites) is what guarantees the timeout sink is not missed.
- The `reschedule` record is written immediately before `reportReschedule(...)`,
  carrying the parsed next-poke time.

**This ordering is forward-looking, not a present-day fix.** Return-value,
extra-links, and custom-XCom persistence is **not implemented today** — those
pushes short-circuit on `codes.Unimplemented` and store nothing
(`runner.go:463-468`; XCom lands in a later phase). So "success only after the
pushes" currently guards a gap that **cannot occur yet** (the pushes always
"succeed" by being skipped). The constraint hardens the write site now so that
when persistence lands, a recovered success cannot have missing downstream data;
it buys nothing until then. Called out so the win is not mistaken for a
present-day correctness improvement.

### The reconciler reads it as source of truth (attempt-guarded)

`classifyPod` (`internal/executor/reconcile.go:39`) is extended: when a terminated
container carries a decodable outcome record, it is trusted over the pod phase.

**This is a reconciler-seam expansion, not merely a read.** Today `classifyPod`
returns a 3-valued phase enum (`podPending | podFailed | podSucceeded`,
`reconcile.go:14-25`) and `Reconcile` only ever *settles* a `podFailed` pod, via a
single `FailureReporter.FailTask` (`reconcile.go:67-69, 113-117`) — a `Succeeded`
pod is merely GC'd on age, with no success- or reschedule-settle path at all.
Consuming the record therefore requires: (a) a richer `classifyPod` return that
carries the outcome, `exit_code`, and `reschedule_at`; (b) new settle methods
beyond `FailTask` for the success and reschedule paths; (c) **two new
`try_number`-guarded queries** — a succeed and a reschedule settle — alongside the
existing `FailTaskInstanceIfActive`; and (d) new branches in the `Reconcile` loop.
This is scoped in Step 2 below.

- Record says `success` on a `Failed` pod → settle the task **succeeded** (the
  report was lost, the work was not).
- Record says `failed` → settle failed with the recorded `exit_code` (now
  authoritative rather than inferred).
- Record says `reschedule` → route to `up_for_reschedule` using the record's
  `reschedule_at`; the reconciler **never settles a reschedule as a failure**
  (excludes the bare exit-75 path, #386).
- **No record** (old agent, non-graceful kill before the write, node loss) → fall
  back to today's phase-based behavior; the School-B floor re-drives safely.

**Idempotent, attempt-guarded settle.** The reconciler's settle must not clobber a
*different attempt*. The current reconciler path — `FailTaskInstanceIfActive`
(`internal/storage/queries/runs.sql:341-344`) — guards on `id AND state IN
(...)`, with **no `try_number`**, unlike the agent path `ReportTaskResult`
(`runs.sql:346+`), which guards on state **and** try_number precisely because
retries bump `try_number` **in place** on the same row. Because the pod annotation
carries a specific attempt, a stale reconciler acting on a previous attempt's
lingering `Failed` pod could match `id AND state='running'` on the **new** running
attempt. Today that only fails a live retry (recoverable); once the reconciler also
writes **success**, the same stale match could mark a live retry *succeeded* and
fire downstream on incomplete work — strictly worse than the bug being fixed.
This ADR therefore specifies a **new try_number-guarded settle for the reconciler**
(both the success and failure paths): `WHERE id=$1 AND try_number=$2 AND state IN
(...)`, threading the `leoflow.io/try-number` pod label (already set in `BuildPod`)
into the settle. Whichever of the reconciler and a late agent report writes first
wins; the other is a no-op. `on_failure_callback` (#424) should fire via the
standard `failed`-state handling for a reconciler-driven failure, exactly as for
an agent-driven one — Step 2 must assert this, since the settle is a bare `UPDATE`
and the callback dispatch keys off the transition, not the query. (The claim in
the earlier draft that we could "reuse the existing guard" was wrong — that guard
is not on the reconciler path.)

## Key properties

- **Correctness is School B; this is an optimization.** Absent the record, tasks
  still settle safely by idempotent re-drive; the record only spares needless
  re-runs when the kubelet survived.
- **Recovers surviving-kubelet kills only.** OOM kill, node-pressure eviction, and
  `ActiveDeadline` — where the kubelet lives to flush the termination message — are
  recovered. **Node loss / non-graceful shutdown successes remain unrecoverable**
  by this mechanism (dead kubelet → no ContainerStatuses update → no record); they
  degrade to phase → failure → idempotent re-drive. Stated honestly rather than
  over-claimed.
- **Kubernetes-only.** Lite (subprocess, no pod, in-process delivery) gains
  nothing and is unchanged — #542's in-process retry is Lite's primary fix.
- **Additive and back-compatible.** No schema change; an agent that does not write
  the record degrades to phase-based behavior.
- **Transport is bound to the dedicated-pod case; the contract is not.** The
  termination message is a per-container-*exit* artifact: it can carry a task's
  outcome only because today the pod runs exactly one task and exits at that task's
  end (ADR 0002). The planned model keeps a task **atomic to one pod** but lets that
  pod be **dedicated** (exits at the task's end — termination message still works)
  **or shared** with sibling tasks (a warm worker that does *not* exit per task —
  the termination message cannot carry a per-task outcome). The orchestration side
  consumes the outcome through the execution seam (`ExecutionStore`), **never pod
  phase or the transport directly**, so a shared pod simply adds a *second*
  transport — a long-lived worker reporting each task-attempt's outcome in-band and
  durably, which is precisely Temporal's worker model — without touching the
  orchestration side. This ADR is careful not to deepen the pod-per-task assumption.

## Consequences

- The most damaging false-negative in the execution path — a task that succeeded
  but lost its report — is closed for the common kill classes; the residue is
  safe re-drive. This is the concrete answer to Airflow's zombie-task false
  negative.
- Contained but not trivial. On the execution side: a write in the agent's
  terminal path and a policy pin in `BuildPod`. On the orchestration side: a richer
  `classifyPod` return, new success/reschedule settle methods, two new
  `try_number`-guarded queries, and new `Reconcile` branches. No new coordination
  substrate and no control-plane schema change — but this **expands the reconciler
  seam**, it is not "just a read".
- The termination message becomes a **contract** between agent and reconciler,
  versioned by `v` so the format can evolve.
- The reconciler grows one dependency on a Kubernetes-surfaced field; the fallback
  keeps it correct where the field is absent.

## Alternatives considered

- **Trust pod phase (the naive backstop).** Rejected: a lost success report makes
  the pod `Failed`, so phase can never recover the success. This is the no-op the
  issue calls out.
- **A file in a shared `emptyDir`.** Viable, but needs a shared mount and a reader
  with pod-filesystem access (a sidecar or exec), plus cleanup. The termination
  message is Kubernetes-native, rides the pod status the reconciler already lists,
  and needs no extra mount.
- **A pod annotation/label patch written by the agent (downward API / API write).**
  Rejected: it needs a live kubelet and a Kubernetes API write, and the task pod
  sets `AutomountServiceAccountToken: false` (`kubernetes.go:80`) to deny the
  agent API access by design. Same node-loss limit as the termination message,
  with more privilege.
- **Push the outcome to the control plane before exit.** That *is* the report —
  circular; the whole problem is that this delivery can fail.
- **Temporal / Cadence durable execution (School B, pure).** The Temporal Service
  owns all durable state as an event-sourced history in a database
  (Cassandra/MySQL/PostgreSQL); the worker holds no authoritative state. An
  activity is complete **only once the Service durably records the completion** the
  worker reports (`RespondActivityTaskCompleted`, keyed by a task token), not when
  the worker's code returns. A worker that finishes the work but dies before that
  record is written is simply invisible to the Service: Temporal **does not detect
  or salvage the lost success** — it relies on the Start-To-Close / Heartbeat
  timeout to notice the missing signal and **retries the activity** per policy.
  Execution is **at-least-once**; safety of the repeat is the developer's job via
  **idempotency keys** (there is no exactly-once *side-effect* primitive — the docs
  call it "effectively exactly-once"). We do not adopt this wholesale: leoflow is
  Airflow-compatible — a task is an **opaque user process in a pod**, not an
  activity in a durable-execution SDK that owns the code's structure and can replay
  it. But we adopt its **principle as our correctness floor** (School B: re-drive on
  ambiguity + idempotent tasks), which we already have via ADR 0031 + ADR 0051. Its
  long-lived worker (one process polling and reporting many attempts in-band) is
  exactly the *shared-pod* transport our N:1 future points at — further reason the
  outcome must be transport-abstract, not pinned to the termination message.
  (Sources: docs.temporal.io — architecture, activity-execution, detecting-activity-failures; temporal.io/blog/idempotency-and-durable-execution.)
- **Argo Workflows wait/emissary sidecar.** Argo's emissary executor runs the user
  command as a sub-process and writes its exit code to a file on a shared `emptyDir`
  (`/var/run/argo/ctr/<container>/exitcode`); a `wait` sidecar reads it, uploads
  artifacts, and reports outputs via a `WorkflowTaskResult` resource, while the
  controller reads **pod status from an informer** as the success/fail source of
  truth. This is an *intra-pod* artifact plus an extra container per task, and it
  has the **same node-loss blind spot** as ours: if the whole pod/node dies the
  sidecar dies too, no exitcode is written, and the controller marks the node
  **Error ("pod deleted")** — it never reconstructs a success; recovery is left to
  `retryStrategy` (notably `retryPolicy: OnError`), a full re-run assuming
  idempotency. We reject it because the termination message rides the pod status we
  **already `List`**, needing no sidecar, shared mount, or extra container — and it
  buys us nothing Argo's sidecar would past the same kubelet-alive boundary.
  (Sources: argoproj/argo-workflows — workflow-executors.md, emissary.go, operator.go; argo-workflows.readthedocs.io — tolerating-pod-deletion, retries.)
- **Apache Airflow KubernetesExecutor zombie handling (the anti-pattern we beat).**
  Airflow's source of truth is `TaskInstance.state` in the metadata DB, written by
  the **in-pod process itself**. A pod that dies before committing `success`
  becomes a **zombie**: the scheduler notices missing heartbeats (the "task
  instance heartbeat timeout", historically `scheduler_zombie_task_threshold`,
  default 300s) and **marks it failed or retries it — it never reconstructs a
  success from a dead pod**. `adopt_or_reset_orphaned_tasks` covers a dead
  *scheduler*, not a dead worker. This is exactly the false-negative this ADR
  closes for the surviving-kubelet case, and the basis of the "kill the zombie
  task" story: leoflow settles a succeeded-but-unreported task correctly instead of
  failing-and-re-running it. (Sources: airflow.apache.org — core-concepts/tasks,
  administration-and-deployment/scheduler.)

## Phased path

1. **This ADR** — the durable-outcome mechanism, the seam contract, and the
   School-B-floor / School-A-optimization framing.
2. **Core (no cluster).** Execution side: `BuildPod` pins the termination-message
   policy (tested); the agent writes the record keyed off the reported state,
   covering `fail(...)` **and** the `failWithReason(...)` timeout sink, the success
   record after the pushes. Orchestration side: a richer `classifyPod` return; new
   success/reschedule settle methods; two new `try_number`-guarded queries (succeed
   + reschedule) beside `FailTaskInstanceIfActive`; new `Reconcile` branches;
   reschedule-exclusion with `reschedule_at`; idempotent double-settle; and an
   assertion that `on_failure_callback` fires on the reconciler-driven `failed`
   transition. Unit-tested with a fake clientset and synthetic pod statuses.
3. **E2E / chaos (k3d).** Inject a pod kill mid-report on the operator/split E2E
   and the runtime chaos harness (#524); assert the correct terminal state, and
   assert the agent can write the termination file under the hardened security
   context.

## References

- ADR 0051 — Separate the orchestration and execution state machines (this is its Phase 2)
- ADR 0031 — Scheduler reconciliation loop + two-layer reaping (the School-B re-drive floor)
- #543 — Pro: durable task-outcome signal read by the reconciler
- #542 — in-process report retry (the primary, Lite-and-Pro fix); #541 — settle idempotency groundwork
- #424 — native on_failure_callback gating
- `internal/agent/runner.go` (terminal path), `internal/executor/reconcile.go` (`classifyPod`, `reportFailure`), `internal/executor/kubernetes.go` (container spec / policy prerequisite), `internal/storage/queries/runs.sql` (the `ReportTaskResult` vs `FailTaskInstanceIfActive` guard asymmetry)

## Proposed amendment (2026-10-04): a durable SUCCESS overrides an infra guess

**Status of this amendment:** Proposed, alongside the ADR itself.
**Issues:** #1124, #900 (primary); #948 (the trust posture this must respect);
#896 (the drill); depends on the ADR 0051 attempt-epoch amendment (#1130, #911).

### What is wrong

The Decision above says the record is "trusted over the pod phase". In code it
is trusted only while the row is still active: every reconciler settle guards
on `state IN ('scheduled','queued','running')` (`internal/storage/queries/runs.sql:472-504`).
A reaper that reaches the attempt first writes `state='failed',
last_failure_kind='infra'` (`runs.sql:883-893`, `924-934`, `80-90`), and from
then on the record can never win. The reconciler's own doc comment accepts this
("the recovered success is dropped and the task degrades to the safe retry
path", `internal/executor/reconcile.go:338-343`). The two issues show the cost
is not a rare tie but a structural window:

- **#900, the non-monotonic middle.** After a restart the reapers hold for the
  settling grace (`defaultSettlingGrace = 2 x 90s`, `internal/executor/reaper.go:44, 55`),
  then the agent-lost reaper fires on heartbeat staleness alone, with no
  presence read (`internal/executor/heartbeat_reap.go:103-127`). An agent that
  is alive but has not reconnected yet (it is still retrying its SUCCESS
  report, `internal/agent/runner.go:973-1001`) is marked `agent_lost`, and its
  still-Running pod is deleted (`heartbeat_reap.go:168-179`). The record the
  agent already wrote (`runner.go:856-857, 878-892`) lives in a file inside a
  container that is being killed; the pod object that would surface it is
  removed when the container stops, long before a 30 s reconcile sweep can
  read it. A short outage gets a direct SUCCESS, a long one gets SUCCESS via
  the reconciler (the pod exits on its own and is preserved, #928,
  `internal/executor/pod_terminate.go:144-149`), and the middle gets a
  redundant re-run.
- **#1124, the expired token.** The attempt token is sliding: every
  liveness-proven heartbeat re-mints it (`internal/agentrpc/server.go:370-375`,
  `384-406`), but a store error deliberately does not renew (`server.go:361-368`)
  and an expired token is rejected in `identify` before renewal is reached
  (`server.go:671-681`, `internal/auth/agent_token.go:162-172`). After a long
  enough control-plane outage the agent's terminal report returns
  `Unauthenticated`, which is not retried (`runner.go:1009-1016`). The agent
  exits, the pod is terminal, the record is preserved; but the reaper has
  already marked the row `failed`/infra, so `SucceedTaskInstanceIfActive`
  (`runs.sql:483-492`) silently no-ops. The planner then re-places the task on
  the same `try_number` (`internal/scheduler/plan.go:169-199`), up to
  `infraMaxAttempts = 6` (`internal/scheduler/dispatch_backoff.go:25`).
  A grouped dbt task restarts from model zero.

Two further facts shape the fix. First, the planner re-places an infra failure
after `dispatchBackoff(infra_attempts+1)` plus up to 30 s of jitter
(`plan.go:340-351`), which is 5 to 35 s for the first re-place, while the
reconciler sweeps every 30 s. Even a correct override query would usually find
the row already reset to `none`, so the query alone fixes little; the re-place
must also wait for the evidence. Second, once the row is re-placed, the
superseded attempt's record must never touch it, which is exactly the ADR 0051
epoch fence.

### Decision

**An infra mark is a guess; a durable SUCCESS record is ground truth.** The
`agent_lost`, `pod_lost` and `dispatch_lost` marks are inferences from absence
(no heartbeat, no pod, no transition). A SUCCESS record is the task's own
statement, written only after the user process exited 0 with no run error and
every output push was accepted (`runner.go:639-664`). When both exist for the
same attempt, the record wins. Four parts make that safe.

#### 1. The guarded override settle

A new query beside the existing settles:

```sql
-- name: SucceedTaskInstanceOverInfraMark :execrows
UPDATE task_instances ti
SET state = 'success', ended_at = now(), error_message = NULL,
    exit_code = 0, last_failure_kind = NULL
FROM dag_runs dr
WHERE ti.id = sqlc.arg(id)
  AND ti.try_number = sqlc.arg(try_number)
  AND ti.attempt_epoch = sqlc.arg(attempt_epoch)
  AND ti.state = 'failed' AND ti.last_failure_kind = 'infra'
  AND dr.id = ti.dag_run_id AND dr.state = 'running';
```

- `state='failed' AND last_failure_kind='infra'` admits only a reaper's guess.
  An application failure (`last_failure_kind` NULL or `app`) is never
  rewritten, and neither is any other terminal state.
- `try_number` and `attempt_epoch` come from the **pod labels**, which the
  control plane wrote, not from the record. Once the planner re-places the row
  (epoch + 1), the superseded attempt's record can no longer match it, so the
  `state='failed'` clause never has to carry the "do not clobber a live
  re-place" burden on its own (#900's original sketch relied on it alone,
  which is not enough while the reconciler re-reads the same terminal pod for
  ten minutes).
- `dr.state = 'running'`: the reconciler never mutates a finalized run. A
  record that arrives after finalization is ignored and metered; the operator's
  clear-task is the recovery.
- The reconciler calls it only for a **SUCCESS** record, and only after
  `SucceedTaskInstanceIfActive` affected zero rows (`reconcile.go:495-508`
  gains the fallback). `SucceedTaskInstanceIfActive` is `:exec` today
  (`runs.sql:483`) and becomes `:execrows` so that zero rows can be told
  apart; the new query is `:execrows` so the metric counts real overrides.
- A user's verdict is never overridden. The mark-state endpoint
  (`SetTaskInstanceState`, `internal/storage/repository.go:870-884`) writes
  the state through `UpdateTaskInstanceStateByRunTask` and leaves
  `last_failure_kind` as it was, so a user who marks a reaped task `failed`
  today leaves `failed`/`infra` behind: the planner still re-places it, and
  this query would turn it into `success`. The mark-state write therefore
  clears `last_failure_kind` (and stamps `infra_confirmed_at`) in the same
  statement, making a user's `failed` an application failure that neither
  rail touches.
- The scheduler's state machine has no `failed` to `success` edge
  (`internal/scheduler/state_machine.go:31`). The override is a reconciler
  write outside the planner, like the existing settles, and the ADR records it
  as such rather than adding the edge for the planner.

**A FAILED record never overrides an infra mark.** The reaper's own teardown
kills the agent, and the agent's cancellation path writes a FAILED record
(`runner.go:639-650` via `fail`, `:687-699`). A FAILED record after an infra
mark is therefore as likely to be an artifact of the reap as the task's own
verdict, and converting it to an application failure would bill the user's
retry budget for the platform's kill. Only SUCCESS is asymmetric: nothing the
platform does to an attempt can make it record success.

Every override appends one system line to the attempt's log ("outcome recovered
from the durable record over agent_lost") through the same sink the reapers use
(`heartbeat_reap.go:186-200`), and increments
`reconcile_infra_override_total{mark}`, so a recovered success is never silent.

#### 2. The re-place waits for the evidence

The ADR 0051 invariant says orchestration must not read pods, so the planner
cannot look for a record itself. Instead the execution layer **confirms** the
guess:

- Reaper marks on Kubernetes write `infra_confirmed_at = NULL` (provisional).
  New column, same migration family as the epoch.
- Each reconciler sweep lists provisional infra failures (bounded, like the
  reaper candidate queries) and, per row, looks at the pods of that exact
  `(try, epoch)` in the cache it already holds. A SUCCESS record: override
  (part 1). No pod at all, or only pods whose task container has terminated
  without a SUCCESS record: `ConfirmInfraFailure` stamps
  `infra_confirmed_at = now()`, guarded on the same
  `(id, try, epoch, failed, infra)` tuple. A pod whose task container has not
  terminated yet (a pod stopped in place by part 3 is still inside its
  termination grace, and may already show phase `Failed` with reason
  `DeadlineExceeded` before its container status does): leave it for the
  next sweep. The test is the container's `state.terminated`, the same field
  `outcomeRecord` reads (`reconcile.go:102-112`), never the pod phase.
- The planner treats a provisional infra failure as **active**: no re-place,
  and no downstream condemnation, regardless of the remaining infra budget.
  `planRetryTransitions` (`plan.go:169-199`) keeps it at the effective
  `up_for_retry` it already uses for a re-placeable task, `FinalizeRun`
  (`plan.go:366-392`) counts it as non-terminal, and the failed-to-none
  re-place additionally requires confirmation.
- **Liveness valve.** If confirmation has not arrived within
  `infraConfirmMaxWait` of `ended_at`, the planner proceeds as today and meters
  `infra_confirm_valve_open`. The value must exceed the task pod's termination
  grace plus two maintenance intervals; it becomes a rung of the boot-time
  ladder (`internal/executor/resilience_ladder.go:15-40`), next to the
  settling-grace rungs it mirrors. It also joins the "infra re-place delay <
  orphan threshold" rung: a run whose only live task is a provisional mark has
  no activity to show the orphan-run reaper, so `max(InfraReplaceMaxDelay,
  infraConfirmMaxWait)` must stay below `OrphanThreshold` (5 minutes by
  default, `reaper.go:43`). A DAG that declares a termination grace longer than
  that bound gets the valve, not the record, for that task.
- **Lite** has no reconciler and no record transport (the termination-log path
  is unset, `runner.go:897-899`), so the Lite wiring stamps
  `infra_confirmed_at` at mark time and behaves exactly as today.

The re-place backoff is measured from `ended_at` (`plan.go:340-351`), and so
is the wait for confirmation, so the two overlap rather than add. For a genuine
loss with no pod left, confirmation lands on the next sweep, at most one
maintenance interval (30 s) after the mark, and the first re-place moves from
5 to 35 s to at most about 30 to 35 s. For a pod that was stopped in place,
confirmation waits for the container to exit, so the bound is the pod's
termination grace (30 s by default) plus one interval, about a minute. That is
the price of the record's read; Lite pays none of it (below).

#### 3. Teardown stops the pod in place instead of deleting it

`deletePodsBySelector` already refuses to delete a terminal pod (#928,
`pod_terminate.go:83-104, 144-149`). The remaining hole is a pod that is still
Running when it is reaped, which is #900's exact case: deleting it destroys
the pod object, and with it the termination message, as soon as the container
stops. The teardown therefore changes for a pod whose containers have started
(`status.startTime` set):

- **Stop in place:** patch `spec.activeDeadlineSeconds` to `1`.
  `ValidatePodUpdate` (`k8s.io/kubernetes`, `pkg/apis/core/validation`)
  allows exactly two updates of this field on a live pod: setting it when it
  is unset, and lowering it; raising it or removing it is rejected. Task pods
  carry one only when the task declares a timeout or the credential ceiling
  is on (`podActiveDeadline`, `internal/executor/kubernetes.go:234-242`), and
  the patch to `1` is valid in both cases. The deadline counts from
  `status.startTime`, so `1` has always elapsed. The kubelet kills the containers with
  the pod's normal termination grace, the pod goes `Failed` with reason
  `DeadlineExceeded`, and the **object stays**, carrying the task container's
  termination message. The #474 property (a reaped attempt stops running user
  code) is unchanged; only the deleter changes, to the reconciler, which is
  already the designated deleter of terminal pods and collects them at
  `podGCGracePeriod` (`reconcile.go:330, 425-427`). Terminal pods do not count
  against compute quota, so holding them for the grace period costs nothing.
- **Delete** a pod that never started (no `startTime`): it has no container
  and no record, and a Pending pod can still start the task, so it must go,
  exactly as `terminalForTeardown` argues today.
- **RBAC:** the executor Role gains `patch` on `pods`
  (`helm/dexaflow/templates/rbac.yaml:22-24`), and
  `scripts/rbac-covers-executor.sh` is updated in the same PR. If the patch
  fails for any reason other than `NotFound` (`Forbidden` from a
  hand-maintained Role, or an admission webhook that rejects pod updates), the
  teardown falls back to delete and meters `reap_teardown_delete_fallback`;
  correctness then degrades to today's, never below it. The verb adds no
  meaningful privilege: the same Role can already `create` arbitrary pods in
  the task namespace.

`DeleteRunPods` (the orphan-run reaper) uses the same helper and gets the same
behavior.

#### 4. The expired token: the record path is sufficient; no grace window

`ReportState` does **not** accept an expired token, not even a
signature-valid one for a terminal report. On Kubernetes, parts 1 to 3 already
recover every case #1124 describes: the agent wrote the record before
reporting, an `Unauthenticated` report ends the retry loop, the container
exits, the stopped or exited pod is preserved, and the override settles it.
Accepting an expired token would buy nothing more there and would cost:

- a second acceptance rule in `AuthenticateAgent` or beside it, which every
  agent RPC (secret resolution included) is one refactor away from sharing;
- a longer replay window for a leaked token, which is precisely what the short
  TTL of ADR 0055 exists to bound;
- the dead man's switch is intentional: a credential that lapsed while the
  control plane could not observe the attempt should stay lapsed.

If this is ever revisited (Lite is the only place it would help), the
constraints are: `ReportState` only, terminal states only, signature and
audience valid, `exp + grace` bounded by `oiat + max_attempt_credential_lifetime`,
and the full ADR 0051 epoch fence. Recorded here so the next proposal starts
from them.

### The trust caveat (#948)

The task's own process can write the termination-message file
(`reconcile.go:196-201` already bounds the reason for this reason), so a task
can produce a SUCCESS record without having succeeded. The override must not
let it do anything its author cannot already do.

What the author already controls: the task's exit code, and therefore its own
attempt's outcome through the normal report. A task that exits 0 succeeds.
What the override adds: a task that also made the platform lose track of it
(stopped heartbeating, for example by killing its agent, which shares its
container and uid) can still have its own SUCCESS honored. That is the same
claim the author could have made by exiting 0. The bounds that keep it there:

- **Scope is the attempt, from labels.** The task instance id is the
  `leoflow.io/task-instance-id` annotation and the attempt is the
  `try-number`/`attempt-epoch` labels, all written by the control plane at pod
  creation; the task pod has no API credentials to change them
  (`kubernetes.go:112`). A record can only ever settle the attempt that wrote
  it. A record whose own `attempt_epoch` field disagrees with the label is
  treated as no record.
- **Only an infra guess is rewritten.** An application failure the agent
  reported stays failed; a record cannot erase it.
- **Only a running run.** A finalized run is never reopened by a record.
- **No downstream effect the author could not cause.** Downstream tasks see a
  success they would have seen had the task exited 0 with the control plane
  reachable.
- **Attribution.** The override is visible as such (log line, metric, and the
  settle source on the attempt). #948's producer-side marker changes who is
  credited for a **reason** string; it is not an authenticity check on the
  **outcome**, and none is possible: any key the agent holds is readable by the
  task in the same container. This amendment does not depend on it.

The residual exposure is therefore misattribution of a task's own claim, the
same class #948 already documents, and not privilege.

### Downstream tasks

The confirmation gate (part 2) is what keeps downstream consistent: while a
mark is provisional the planner treats the task as active, so no downstream
task is marked `upstream_failed` on a guess, whatever the remaining infra
budget. In the normal path the override therefore lands before any downstream
decision and the run simply continues.

Two paths can still meet a downstream `upstream_failed`:

- **The valve opened** (no confirmation within `infraConfirmMaxWait`, so the
  planner acted on the guess) and a record surfaces later while the run is
  still running. The planner gains one narrow rule: a task in
  `upstream_failed` whose upstreams are no longer failed returns to `none`
  (a transition the state machine already allows,
  `internal/scheduler/state_machine.go:33`, but never emits; #896 item 3). It is level-triggered and only fires in a running run.
- **The run already finalized.** The override's `dr.state='running'` clause
  refuses it; the record is metered and the operator clears the task.

The B4 rule covers only `upstream_failed`. Once the valve has opened and the
planner has treated the task as terminally failed, a downstream whose trigger
rule fires on failure (`one_failed`, `all_failed`, `all_done`,
`none_success`, and the like) may already have run, and a later override would
leave the run recording both the failure branch and the success. B4 also
fires for any `upstream_failed` whose upstream later reads success, including
one a user marked `success` by hand, which Airflow does not do. **Open question
for acceptance:** add `AND ti.ended_at > now() - infraConfirmMaxWait` (and
`ti.infra_confirmed_at IS NULL`) to the override guard, so the override is
legal only while the planner provably still treats the task as active. The
planner's view and the override then never disagree, and B4 is unnecessary.

### Changes to the text above

When accepted: "Additive and back-compatible. No schema change" in Key
properties becomes "two additive columns (via the ADR 0051 amendment)", the
reconciler doc comment at `reconcile.go:338-343` loses its "dropped" sentence,
and the record schema gains an optional `attempt_epoch` field. `v` stays `1`:
`Decode` ignores unknown fields (`internal/taskoutcome/record.go:164-187`), so
an old reader still decodes a new record, and a new reader treats an absent
field as "use the label".

### Implementation plan

Depends on PR A0 to A4 of the ADR 0051 amendment (the epoch must exist before
any settle relies on it). Each PR is failing test first.

- **PR B1: the override query and reconciler branch.** Test first, the one
  #1124 asks for, written so it goes red the day the fix lands: in
  `internal/storage`, mark a TI `running`, run `MarkTaskAgentLost`, call
  `SucceedTask`; today the row stays `failed`/infra. Then the new behavior,
  integration: override succeeds for a matching `(try, epoch)`; is a no-op for
  a different epoch, for an app failure, for a finalized run, and for a FAILED
  record. Unit, `internal/executor`: the reconciler falls back to the override
  only for a SUCCESS verdict and only after a zero-row active settle; the log
  marker and metric fire once.
- **PR B2: stop-in-place teardown.** Fake-clientset tests: a started
  non-terminal pod is patched, not deleted; a never-started pod is deleted; a
  `Forbidden` patch falls back to delete and meters it; a terminal pod is
  untouched. Chart template test and `rbac-covers-executor.sh` for the new
  verb.
- **PR B3: confirmation gate.** Migration (next free number); reapers write
  provisional on Kubernetes and confirmed on Lite; `ConfirmInfraFailure`;
  planner treats provisional as active; valve and ladder rung. Tests:
  `internal/scheduler` plan tests (provisional with spent budget keeps
  downstream waiting; confirmed re-places; valve proceeds after the bound);
  reconciler tests (confirms on absent pod, leaves a terminating pod, overrides
  on SUCCESS); ladder rejects a valve below termination grace plus two
  intervals.
- **PR B4: downstream re-derivation.** Plan test: a running run with
  `upstream_failed` downstream of a task that became `success` returns the
  downstream to `none`; a finalized run is untouched.
- **PR B5: epoch in the record.** `taskoutcome.Record.AttemptEpoch`, the
  `TaskSpec` field the agent reads it from, the mismatch rule. Tests: an old
  record decodes; a mismatching epoch falls back to phase.
- **End to end.** `test/e2e/chaos-runtime.sh` gains #900's row: an outage
  between the settling grace and the token TTL (about 4 minutes) with three
  tasks finishing during it; assert zero redundant re-runs, every attempt
  `success` at `max(try_number)=1`, and `reconcile_infra_override_total` equal
  to the number of attempts the agent-lost reaper marked. And #1124's row: an
  outage past the token TTL; same assertions. Add both to the #896 cluster
  matrix (`t_outage` in {30, 90, 240, 400, 700} s, expecting no dip at 240 and
  400).
