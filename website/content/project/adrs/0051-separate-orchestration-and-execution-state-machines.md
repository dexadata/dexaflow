---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /adr/0051-separate-orchestration-and-execution-state-machines.html
# --- end AUTO redirect aliases ---
title: "ADR 0051: Separate the orchestration and execution state machines"
linkTitle: 0051 · Separate the orchestration and execution state machines
weight: 510
description: "ADR 0051: Separate the orchestration and execution state machines"
---

**Status:** Accepted
**Date:** 2026-08-12 (proposed); accepted 2026-08-17
**Accepted because:** the N:1 execution direction (a task is atomic to one pod;
a DAG spans 1..N pods; a pod is dedicated *or* a shared warm worker) makes the
two machines genuinely different in cardinality — so pod phase can never be a
proxy for a single task's outcome. See "Why now" below. This is the foundation
that makes the warm-pool safe; it is a prerequisite for ADR 0053 (admission +
placement) and for PR-N1 (the warm-worker / shared-pod executor).
**Relates:** ADR 0031 (scheduler architecture — reconciliation loop, two-phase dispatch, two-layer reaping), ADR 0027 (editions: executors + delivery), ADR 0015 (Kubernetes-only container execution), ADR 0002 (pod-per-task), ADR 0004 (thin agent), ADR 0010 (observability), ADR 0049 (split API/scheduler roles)
**Issues:** #543 (agent exit code conflates task outcome with report delivery), infra-vs-task retry conflation (this ADR)
**Amendment accepted:** 2026-10-04, an attempt epoch fences an infra re-place from the attempt it replaced (#1130, #911). See the section at the end.

## Why now — the motivation, strengthened

When this ADR was proposed, the case for the seam read as cleanup: name the
second state machine the scheduler had quietly absorbed, stop infra faults from
billing the user's `retries`, and untangle #543. All true, all still the
immediate wins — but they undersell the decision. The decisive reason to draw
this seam is **cardinality**, and it only becomes visible under the N:1
execution direction the product is now committed to.

**The N:1 direction.** A task is **atomic to one pod** — it always runs whole,
on exactly one pod. But a **DAG spans 1..N pods**, and a pod is either
**dedicated** (runs one task, exits at its end) **or a shared warm worker**
(a long-lived process that runs many task-attempts and does *not* exit per task).
So the moment pods are reused, orchestration and execution stop being two views
of the same lifecycle and become two machines with **genuinely different
cardinality**: one pod ⟷ many tasks.

**Why that is dispositive.** Under a dedicated pod you can *almost* get away with
reading the substrate — one pod, one task, one exit, so pod phase looks like the
task's outcome (it is not — that conflation is exactly #543 — but the 1:1
cardinality hides the error). Under a shared worker the illusion collapses
completely: **a pod's phase describes the pod, never any single task that ran on
it.** A warm worker that is `Running` has already finished tasks; one that dies
carries an unbounded set of in-flight attempts down with it. There is no
function from pod phase, and no function from pod identity, to *one task's*
result. Pod-as-proxy was always wrong; N:1 makes it unrepresentable.

**The invariant this forces.** Orchestration must consume a task's outcome
**only through the execution seam** — the `ExecutionOutcome` up the seam, and its
durable record (`ExecutionStore`) — and **must never read pod phase or pod
identity to decide a task's result.** That is not a stylistic preference; it is
the single rule that keeps the two machines correct once their cardinality
diverges. Everything the Decision specifies (the opaque `Correlation` bag,
`Unexecutable` off the task-retry rail, the at-most-once guards) is the machinery
that makes this one invariant hold.

**Foundation, not a peer.** This is why 0051 is accepted now rather than left to
mature alongside the work it enables:

- It is the **foundation that makes the warm-pool (N:1 impl #2) safe.** Losing a
  warm pod is *one* infra event that must fan out to *all* in-flight
  task-instances on it, each re-placed without consuming the user's retry budget
  — the exact generalization of `Unexecutable` / `DispatchAttempts` this ADR
  draws. Without the seam there is nowhere correct for that fan-out to live.
- It is a **prerequisite for ADR 0053 (admission + placement).** Placement
  ("reuse a warm pod of the same DAG, else spin one, else defer") is an execution
  concern; admission (`max_active_tasks`, pools) is an orchestration concern. They
  compose only across a named seam. 0053 assumes this split exists.
- It is a **prerequisite for PR-N1 (the warm-worker executor).** The shared pod
  is simply a *second outcome transport* — a long-lived worker reporting each
  attempt in-band and durably — behind the same `WorkItem` / `ExecutionOutcome`
  contract. That substitution is only invisible to orchestration because the
  seam already hides the transport.

**ADR 0052 already accepted this split one layer up.** 0052 (durable task
outcome, this ADR's Phase 2) explicitly separates the outcome *contract* from its
*transport*: dedicated pod → termination message (impl #1); shared warm worker →
in-band per-attempt record (impl #2); "orchestration consumes the outcome through
the execution seam (`ExecutionStore`), never pod phase or the transport
directly." Accepting 0051 **formalizes the state-machine separation underneath
that contract** — 0052 split contract-vs-transport on this exact basis, and 0051
is the machine boundary that makes the split load-bearing rather than incidental.

## Context

Leoflow runs **one** state machine — the DAG/task reconciliation loop in
`internal/scheduler` (ADR 0031) — but that machine has quietly absorbed a
**second, distinct** one: the pod/execution lifecycle. The execution state
machine has no first-class representation; it is reconstructed asynchronously
from agent gRPC `ReportState` calls, or inferred from pod phase by the reapers.
The two machines answer different questions:

- **Orchestration:** given the DAG's edges, trigger rules, and a task's retry
  policy, *what should happen next?* It knows tasks and dependencies.
- **Execution:** given one unit of work, *place it on a substrate, watch it, and
  return exactly one outcome.* It knows pods, subprocesses, and node loss.

Today the second machine leaks through the first. Four pieces of evidence:

1. **The `Executor` interface returns only a dispatch error, not a task
   outcome.** `Executor.Execute` (`internal/executor/executor.go:100`) returns a
   single `error` that "reflects dispatch, and the agent reports the final state
   over gRPC" (its own doc comment, `executor.go:97-99`). There is no
   `ExecutionOutcome` type. The real task result is reconstructed later from the
   agent's `ReportState`, or — when the agent never reports — *inferred* from pod
   phase by a reaper. The seam that should carry an outcome carries only "did the
   dispatch call error."

2. **Three of the four reapers are pure pod/execution concerns living inside the
   scheduler.** `internal/scheduler` holds four reapers; only `reap.go`
   (run-finalization, #120) is orchestration. The other three are about the
   substrate: `heartbeat_reap.go` (agent-lost, #128),
   `pod_lost_reap.go` (pod-lost — and explicitly **Kubernetes-only, a no-op with
   no PodManager**, i.e. on Lite/subprocess: `pod_lost_reap.go:63-64, 92-93`),
   and `stale_queued_reap.go` (dispatch-lost, detected via pod liveness). Three
   execution-layer sweeps are wired into the orchestration loop because there is
   nowhere else for them to live.

3. **Infrastructure faults consume the task's retry budget — the money finding.**
   All three execution-fault reapers write a plain `failed`, which lands on the same
   `retriable()` rail as an application exception
   (`internal/scheduler/plan.go:104-107` — `run.Tries[taskID] < run.MaxTries[taskID]`).
   So an eviction, an OOM-kill, or a node loss burns one of the user's declared
   `retries` attempts, exactly as if the user's code had thrown. This is a bug:
   an infra fault is not a task failure.

   The striking part is that **Leoflow already does the right thing on two
   adjacent paths** — the split below *generalizes existing logic, it does not
   invent it*:

   - **Synchronous dispatch failure** uses a *separate* counter,
     `DispatchAttempts` (`scheduler.go:77-81`), and its handler
     (`handleDispatchFailure`, `scheduler.go:898-920`) is documented "A dispatch
     failure is infrastructure, not a task failure, so it never consumes the
     task's `try_number`" (ADR 0031 Amendment A). It backs off, and on exhaustion
     fails as the *distinct* terminal state `dispatch_failed`.
   - **Reschedule** (sensor `up_for_reschedule`) preserves `try_number` outright
     — "Re-dispatch once reschedule_at passes, WITHOUT consuming retry budget"
     (`plan.go:85-95`).

   Two of the four ways a task can be re-placed already refuse to bill the user's
   retry budget. The three execution-fault reapers are the outlier. The seam this ADR draws
   is the one that already exists in two places, made uniform.

4. **#543: the agent's exit code conflates "task outcome" with "report
   delivery."** The agent process exit status is used as both "did the task
   succeed" and "did the report get delivered." A pod killed mid-report shows
   `Failed` even when the task itself succeeded (`internal/agent/runner.go:408-420`
   — a non-zero exit or run error routes to `fail`, ahead of the terminal
   `report(...SUCCESS)`), and the reconciler cannot recover a success from pod
   phase because a failed pod is the only durable signal it has to retry the
   report from (`internal/executor/reconcile.go:105-116`). The outcome and its
   delivery are tangled into one value.

None of this is a design failure — it is the natural accretion of a second
concern onto a machine that never named it. This ADR names it.

## Decision

Introduce a clean seam between two layers that already exist implicitly, and make
the seam a first-class contract.

### The two layers

- **Orchestration state machine** — stays in `internal/scheduler`. Owns
  dependency planning, trigger rules, **task-retry policy** (`retriable`,
  `readyToRetry`, `MaxTries`), run finalization (`reap.go`), scheduling,
  catchup/cron, native alerting (`AlertsConfig`), and leader election (ADR 0009).
  It knows **tasks and edges. It never knows about pods.**

- **Execution state machine** — a new/expanded execution layer, seeded by
  today's `internal/executor`. Owns the **substrate**: it places work, owns the
  pod lifecycle, the `PodManager`, the reconciler (`reconcile.go`), and the three
  execution-fault reapers (`heartbeat_reap.go`, `pod_lost_reap.go`, `stale_queued_reap.go`).
  It performs **infra-retry** — bounded, backed off, and it **does not burn the
  task-retry budget** (exactly as `DispatchAttempts` already does). It delivers
  **exactly one outcome** per work item.

### The seam contract

Down the seam goes a **`WorkItem`**:

- the substrate payload — image, args, env, resources;
- plus an **opaque `Correlation` bag**: `{dag_id, run_id, task_id, try_number,
  tenant_id, trace_id}`.

Up the seam comes an **`ExecutionOutcome`**, one of:

- **`Succeeded`** — the task ran and finished cleanly.
- **`Failed(appReason)`** — the task's own code failed (the exception rail;
  *consumes* `try_number` via `retriable()`).
- **`Unexecutable(infraReason)`** — the substrate could not run it, or lost it
  (eviction, OOM, node loss, dispatch-lost). Routes to the **infra-retry**
  counter; **never** consumes `try_number`.
- **`Rescheduled`** — a deferral (sensor reschedule); preserves `try_number`,
  exactly as `plan.go:85-95` does today.

The execution layer treats `Correlation` as **opaque**: it propagates it to pod
labels, task env, and trace spans, and **never interprets it**. The orchestration
layer is the only reader of those coordinates. This is the invariant that keeps
the two machines decoupled — the substrate carries the DAG's coordinates as
metadata without ever understanding the DAG.

`Unexecutable` is the type that fixes finding #3: the three execution-fault reapers stop
writing plain `failed` and instead surface `Unexecutable`, which the
orchestration layer routes to a bounded, no-budget re-place counter — the
generalization of `DispatchAttempts` from "dispatch call failed" to "the substrate
lost the work at any point." `dispatch_failed` becomes one member of a small,
named family of infra-terminal states rather than a special case.

### What this is (and is not)

This **refines** the existing design. The `Executor` interface with its
subprocess and Kubernetes implementations (ADR 0027) is the seed of the execution
layer; this ADR **re-homes** the reapers/reconciler behind it and **hardens the
interface** to return an outcome instead of a bare dispatch error. It is **not a
rewrite** and **not a new coordination substrate.**

## Key properties

**Substrate-agnostic — Lite-safe, Postgres-safe, no CRD.** The contract is a
`WorkItem` down and an `ExecutionOutcome` up; a subprocess has no CRD but it still
has an outcome. **Postgres stays the single source of truth** (ADR 0031); the only
schema change is **one additive migration**: a `last_failure_kind` column on
`task_instances` that records app-vs-infra so the orchestration layer can route
without re-deriving. No new datastore, no etcd, no per-task custom resource. This
is a hard requirement, not a preference — see the rejected CRD alternative, whose
Lite blocker is dispositive.

**Best tool per problem, one contract.** Each execution adapter uses the best
substrate for its edition: a **subprocess** for Lite (µs fork, zero isolation,
dev-only per ADR 0027), **k8s-native pods** driven by a controller for Pro
(pod-per-task, real isolation, ADR 0002/0015), and the seam stays open to future
adapters. All of them satisfy the same `WorkItem`/`ExecutionOutcome` contract, so
the orchestration layer is written once and is edition-blind — the same property
ADR 0031 already guarantees for the state machine, now extended to the outcome.

**Observability is preserved and improved.** The DAG coordinates ride as metadata
the execution layer *propagates but does not interpret*. Leoflow already stamps
them at every layer:

- **pod labels** `leoflow.io/{dag-id,task-id,run-id,try-number,tenant-id}`
  (`internal/executor/kubernetes.go:65-71`);
- **structured JSONL log fields** keyed on `tenant_id/dag_id/run_id/task_id`
  (`internal/logs/logs.go:188-195`);
- **OTLP tracing** (`internal/observability`, ADR 0010).

The target the naming unlocks: a **run → task → pod OpenTelemetry span tree**, and
three-pillars correlation — Prometheus metrics with **low-cardinality**
`dag_id`/`task_id` labels, Loki logs, Tempo traces, joined by the shared IDs and
by exemplars. **Market precedent:** Argo Workflows' pods also do not understand
the DAG; they carry workflow-coordinate labels the controller reads. Carrying the
DAG's coordinates as opaque metadata across an execution boundary is the standard
shape, not an invention.

**The at-most-once guards are preserved verbatim.** Crash-consistency across the
seam is non-negotiable. The existing guards — a source-state CAS on reaper writes
(`WHERE state = 'running'` for the pod/agent reapers, `WHERE state = 'queued'` for
the dispatch-lost reaper), `ON CONFLICT DO NOTHING` on the durable-outcome path,
and `ErrStaleReport` on out-of-order agent reports — stay exactly as they are (ADR 0031's
leader-overlap correctness section). The seam changes *who* writes the outcome and
*what type* it is, not the concurrency discipline that makes the write safe.

## Consequences

**Fixes two live bugs.**

- The **infra-vs-task retry conflation** (finding #3): an eviction/OOM/node-loss
  stops billing the user's `retries`. A `retries: 0` task no longer dies on a
  node blip.
- **#543**: separating the outcome from its delivery lets a success survive a pod
  killed mid-report, and gives the reconciler a defined path to recover it.

**Materially simplifies the scheduler.** Three execution-concern reapers, the
reconciler, and the `PodManager` leave `internal/scheduler`. What remains there is
purely orchestration: plan, finalize, schedule, alert, elect. The scheduler stops
reasoning about pods.

**Orthogonal to multi-tenancy and to scale — be explicit.** This ADR does **not**
touch tenant isolation (#508 / #209): `tenant_id` rides the `Correlation` bag as
one more opaque coordinate, unchanged. It does **not** touch horizontal scale
(#525): the split is a layering seam, not a topology change, and it composes
cleanly with the API/scheduler process split (ADR 0049) without depending on it.
Anyone reading this expecting a tenancy or scale decision should look elsewhere.

**Honest hard parts.** Naming the seam does not make these free:

- **App-vs-infra classification.** Deciding whether a given failure is
  `Failed(appReason)` or `Unexecutable(infraReason)` is the crux. Pod phase +
  termination reason (`Evicted`, `OOMKilled`, `DeadlineExceeded`, node
  `NotReady`) is the signal; misclassifying an app failure as infra would let a
  genuinely-broken task retry forever.
- **Bounded infra-retry policy — the poison-placement guard.** Infra-retry must
  be bounded and backed off exactly like `DispatchAttempts`, or a task that is
  *unplaceable* (bad image, unsatisfiable resources) becomes an infinite re-place
  loop. Exhaustion must terminate in a named infra-terminal state, visible to the
  operator.
- **The durable outcome signal is best-effort on node loss.** The pod termination
  log is capped at **4 KB**, and an `emptyDir`-backed outcome file is
  **unreadable once the node is gone**. So `Succeeded` recovery from a lost node
  cannot be guaranteed; **node-loss stays best-effort** and falls back to
  `Unexecutable` — which is the correct conservative default (re-place without
  billing the user), but it means a task that *actually* succeeded on a node that
  then vanished may be re-placed. This is an accepted limitation, called out so it
  is not mistaken for solved.
- **Crash-consistency across the new boundary.** Every guard listed above must
  hold when the outcome write and the state transition straddle the seam and a
  crash lands between them. The reconciliation model (idempotent, DB-derived
  every tick) is what makes this tractable, but each phase's PR must prove it.

## Alternatives considered

1. **Full CRD-native control plane** (each task/run a custom resource; the
   controller reconciles etcd). Rejected. It is the etcd worst-case workload
   (high-churn, short-lived objects); **Argo itself had to add a SQL backend**
   after hitting exactly this; and it has a **hard Lite blocker — Lite has no
   Kubernetes API server** (ADR 0027: Lite is a self-contained binary, and its
   `subprocess` executor has no cluster at all). A contract that cannot be
   satisfied on Lite fails the editions requirement outright. Postgres-as-truth
   (ADR 0031) already gives durable, queryable state without etcd.

2. **Pro-only read-only CRD *projection*** (mirror runs/tasks as CRs for
   `kubectl`/GitOps *visibility*, DB stays the truth). **Viable later** and worth
   its own ADR — it buys operator ergonomics on Pro — but it is **orthogonal** to
   this decision: it is a read model, not the execution seam. Not adopted here,
   not foreclosed.

3. **Status quo** (leave the two machines fused). Rejected: it is the direct cause
   of #543 and of the retry-budget bug, and it keeps three execution-concern
   reapers and the `PodManager` welded into the scheduler.

## Phased path

Each phase ships **independently**, **failing-test-first** (ADR 0011), and is
**ADR-gated where it changes observable behavior**. Ordered by value-over-blast:

- **Phase 0 — name the outcome.** Introduce `ExecutionOutcome` and the additive
  `last_failure_kind` column. Purely additive; nothing routes on it yet. No
  behavior change.
- **Phase 1 — fix the retry conflation** (highest value, lowest blast). Route
  `Unexecutable` faults from the three execution-fault reapers to a bounded, no-budget
  re-place counter — the generalization of `DispatchAttempts`. This is the bug
  fix users feel: infra faults stop eating `retries`.
- **Phase 2 — durable outcome + #543.** Separate the task outcome from its report
  delivery; give the reconciler a defined success-recovery path within the
  best-effort limits stated above.
- **Phase 3 — re-home the reapers.** Move `heartbeat_reap.go`,
  `pod_lost_reap.go`, `stale_queued_reap.go`, the reconciler, and `PodManager`
  behind the execution layer, leaving `internal/scheduler` orchestration-only.

## References

- ADR 0031 — Scheduler architecture (reconciliation loop; Amendment A:
  `DispatchAttempts`, `dispatch_failed`, "infra, not a task failure").
- ADR 0027 — Editions: `subprocess` (Lite) vs Kubernetes (Pro) executors.
- ADR 0015 / ADR 0002 — Kubernetes-only, pod-per-task execution.
- ADR 0004 — Thin agent (reports state over gRPC).
- ADR 0010 — Observability (OTLP tracing).
- `internal/executor/executor.go:100` — the `Executor` interface returning only a
  dispatch error.
- `internal/scheduler/plan.go:85-95, 104-107` — reschedule preserving
  `try_number`; the `retriable()` rail.
- `internal/scheduler/scheduler.go:77-81, 898-920` — the `DispatchAttempts`
  precedent this ADR generalizes.
- `internal/scheduler/pod_lost_reap.go:63-64, 92-93` — the Kubernetes-only,
  Lite-no-op pod reaper.
- `internal/agent/runner.go:408-420` / `internal/executor/reconcile.go:105-116` —
  #543: outcome vs report-delivery conflation.
- `internal/executor/kubernetes.go:65-71` / `internal/logs/logs.go:188-195` — the
  DAG-coordinate labels and JSONL fields the execution layer propagates.
- Argo Workflows — pods carry workflow-coordinate labels the controller reads; the
  project added a SQL backend after etcd pressure.

## Amendment (2026-10-04): an attempt epoch fences an infra re-place from the attempt it replaced

**Status of this amendment:** Accepted (proposed and accepted 2026-10-04 by
the project owner). The ADR above stays Accepted; this section extends it.
**Target release:** v0.5.1 ships PR A0 to A5 below; PR A6 (rejecting legacy
tokens) ships in a later minor release.
**Decided at acceptance:** the attempt epoch is claimed at dispatch, at the
cost of one extra write per dispatch (see "the dispatch itself" below).
**Issues:** #1130, #911 (primary); #901, #863 (same root cause); #896 (the drill
that surfaced the class).

### What is wrong

Phase 1 made an infra fault re-place the task without consuming the user's
retry budget, and it did so by design: `ResetTaskInstanceInfraReplace` leaves
`try_number` untouched and bumps `infra_attempts` instead
(`internal/storage/queries/runs.sql:404-445`). The ADR treated `try_number` as
the user-visible retry count, which is right. But every fence in the execution
path also uses `try_number` as the **attempt identity**, and that is the part
Phase 1 broke. After an infra re-place the superseded attempt and its
replacement share `(task_instance, run, task, try)` and nothing in the system
can tell them apart:

| Fence | Where | Keyed on |
| --- | --- | --- |
| Agent identity and token claims | `internal/auth/agent_token.go:29-47` (`AgentIdentity`), `:50-69` (`agentClaims`) | `try_number` |
| Report fence | `runs.sql:506-546` (`ReportTaskResult`) via `internal/storage/agent_store.go:151-179` | `try_number` + active state |
| Heartbeat fence and the `should_terminate` kill switch | `runs.sql:735-752`, `internal/agentrpc/server.go:347-360` | `try_number` + active state |
| Secret liveness | `runs.sql:754-774` (`IsTaskInstanceLive`) | `try_number` + active state |
| Agent reschedule | `runs.sql:548-561` (`RescheduleTaskInstance`) | **no attempt guard at all** |
| Reconciler settles | `runs.sql:472-504`, `internal/executor/reconcile.go:292-302, 470-493` | `id` + `try_number` (pod label) |
| Pod teardown and presence | `internal/executor/pod_terminate.go:41-45, 171-189`, `pod_informer.go:128-135` | label selector `run-id,task-id,try-number` |
| Reaper marks | `runs.sql:80-90, 883-893, 924-934` | `id` + source state only |
| Log stream key | `internal/logs/logs.go:117-123`, `internal/logs/object.go:85` | `{task}/{try}.log` |
| Attempt history | `migrations/016_task_instance_history.up.sql` (`UNIQUE (task_instance_id, try_number)`), `ON CONFLICT DO NOTHING` in every reset rail | `try_number` |

`DeleteTaskPod`'s own comment states the invariant everything relies on, "a
retry bumps try_number in place ... so a newer live attempt can never match
this selector" (`pod_terminate.go:35-40`). It holds for the retry rail. It is
false for the infra rail, and the consequences are concrete:

1. **A stale pod settles the live replacement (#1130).** The reconciler
   re-classifies every terminal pod on every sweep until it is collected at
   `podGCGracePeriod = 10m` (`reconcile.go:330, 394-431`). A terminal pod of the
   superseded attempt that carries a SUCCESS record calls
   `SucceedTaskInstanceIfActive(id, try)` (`runs.sql:483-492`), and once the
   replacement is `queued` or `running` the guard matches it. The replacement is
   marked `success` from work it never did; its own pod then receives
   `should_terminate` on its next heartbeat and is killed mid-run.
2. **A superseded agent is accepted as the live one (#911).** The report fence
   is `try_number = $N AND state IN (...)` (`runs.sql:544-546`). On Lite the
   subprocess agent is detached on purpose (`internal/executor/subprocess.go:192`,
   `context.WithoutCancel`) and keeps retrying its RUNNING report without a
   budget (`internal/agent/runner.go:973-1001`), so after a dispatch-lost re-place
   its RUNNING report can land on the reset row and start user code concurrently
   with the replacement.
3. **Teardown can hit the replacement (#901).** `DeleteTaskPod` lists by the
   shared selector and deletes whatever matches (`pod_terminate.go:83-104`).
4. **Logs and history collapse (#863).** Every attempt of one try writes the
   same object key (the object sink `Put`s over it), and the history archive
   drops every re-place after the first on `ON CONFLICT DO NOTHING`.
5. **Reaper marks are not attempt-scoped.** `MarkTaskAgentLost` and
   `MarkTaskPodLost` match `id AND state='running'`; the candidate's
   `try_number` is read (`runs.sql:847-867, 895-922`) but never written into the
   guard, so a candidate that went stale between list and mark can fail the
   attempt that replaced it.

A sixth defect sits next to these and is fixed by the same change. **No reset
rail clears `last_heartbeat_at`** (`runs.sql:323-445, 619-683`; the column is
written only by `RecordTaskHeartbeat`, `runs.sql:747-752`). A re-placed or
retried attempt that reports RUNNING (`runner.go:570`) inherits the previous
attempt's heartbeat until its own first beat, one full interval later
(`runner.go:589, 808-827`). For an attempt that was reaped as `agent_lost` that
inherited value is already past the threshold by definition, so a maintenance
sweep landing in that window lists it (`runs.sql:864-865`) and
`IsAgentLost` (`internal/executor/heartbeat_reap.go:51-56`) fails the
replacement immediately, spending another infra attempt. The window is not
specific to the infra rail: a clear of any task that heartbeated before (the
ordinary clear-and-rerun) and a retry whose `retry_delay` exceeds the threshold
minus one heartbeat interval inherit an equally stale value, and there the
spurious `agent_lost` mark costs an infra attempt on a task that was never lost. This is read from the
source, not reproduced; PR A0 below starts with the failing test.

### Decision

Introduce an **attempt epoch**: a per-row counter that identifies one
execution attempt, distinct from the user-facing `try_number`.

**A dedicated column, not `infra_attempts`.** `task_instances.attempt_epoch
INT NOT NULL DEFAULT 0`. `infra_attempts` is a *budget* (`plan.go:323-325`),
and a budget is something an operator may legitimately want to reset (a clear,
a policy change); an identity must never go backwards. Coupling the two would
forbid ever resetting the budget. `infra_attempts` stays exactly as it is.

**Monotonic, bumped on every rail that can start a new execution.** Each of
these increments `attempt_epoch` and clears `last_heartbeat_at` in the same
statement:

- `ResetTaskInstanceInfraReplace` (the rail that motivated this);
- `ResetTaskInstanceForRetry`, `ResetTaskInstanceToNone`,
  `ResetFailedTaskInstance`, `ResetAllFailedTaskInstances` (the retry and
  clear rails; `try_number` already differs, but one uniform rule is simpler to
  test than a list of exceptions, and it makes `attempt_epoch` alone a unique
  attempt identity within a row);
- `RedispatchRescheduledTaskInstance` (`runs.sql:447-463`): a reschedule poke
  reuses `try_number` today, which is why the reconciler has to collect poke
  pods immediately (`reconcile.go:413-423`); the epoch turns that into defense
  in depth instead of the only guard;
- `RequeueForRedispatch` (`runs.sql:973-998`): the reclaimed warm worker
  "demonstrably will not run" the attempt, and the epoch fences it if it does;
- `RecordDispatchFailure` (`runs.sql:947-958`): a synchronous dispatch failure
  is ambiguous (a create that timed out may still have created the pod), so the
  next dispatch gets a new epoch;
- **the dispatch itself.** `launchQueued` creates the pod before it records
  `queued` (`internal/scheduler/scheduler.go:1053-1064`), and pod names carry a
  random suffix (`internal/executor/kubernetes.go:785-793`). A dispatch whose
  `queued` write failed (a database error, or leadership lost between the two
  statements) is therefore dispatched again on a later tick with no reset rail
  in between, and both pods would share `(try, epoch)`. The epoch is claimed
  where the dispatcher resolves the row: `ResolveTask` becomes an
  `UPDATE ... SET attempt_epoch = attempt_epoch + 1 ... RETURNING` guarded to
  the pre-dispatch states, and the token, label and annotation are minted from
  the claimed value. With the claim at dispatch every execution has its own
  epoch whatever path led to it; the reset-rail bumps above stay, so that a
  reset alone already fences the attempt it superseded before the next
  dispatch runs.

  **Decided:** the claim at dispatch is accepted. It costs one extra write
  per dispatch (the `ResolveTask` read becomes a guarded `UPDATE ...
  RETURNING`), which is the price of every execution carrying its own epoch
  whatever path led to it, including a dispatch repeated after a lost
  `queued` write.

The epoch is never reset. The attempt identity becomes
`(task_instance_id, try_number, attempt_epoch)`; `try_number` stays in every
predicate so the legacy path below is a strict narrowing of today's.

**Where the epoch goes.**

- **Agent identity and token claims.** `AgentIdentity` gains `AttemptEpoch`
  plus a presence bit; `agentClaims` gains `AttemptEpoch *int64
  json:"attempt_epoch,omitempty"`. The claim name follows the existing
  snake_case claims (`try_number`, `dag_version_id`). A pointer is required so
  "absent" (a legacy token) is distinguishable from epoch 0. `mintAgentToken`
  (`agent_token.go:83-115`) writes it; `identityFromClaims` (`:179-195`) reads
  it; `RenewAgentToken` (`:134-158`) **preserves it verbatim, including its
  absence**. Renewal must never upgrade a legacy token to the row's current
  epoch: that would hand a superseded attempt the identity of its replacement.
  Dispatch mints it from the resolved row (`internal/dispatch/dispatch.go:256-263`);
  the exchange transport carries it through the pod identity annotation
  (`internal/executor/kubernetes.go:326-335`, `PodIdentity` gains
  `json:"epoch,omitempty"`) and its resolver
  (`internal/kubeexchange/kubeexchange.go:194-213`); `WorkAssignment` gains an
  additive `attempt_epoch` field (`proto/agent.proto:235-243`). The agent
  treats the token as opaque, so **no agent binary change is needed for the
  fence**.
- **The report fence.** `ReportTaskResult`, `RecordTaskHeartbeat`,
  `IsTaskInstanceLive`, `BindWarmAttempt`, `RequeueForRedispatch` and
  `RescheduleTaskInstance` add the epoch predicate below.
  `RescheduleTaskInstance` also gains the `try_number` guard it is missing
  today. A superseded agent therefore gets `ErrStaleReport`, which already maps
  to `should_terminate` (`server.go:316-326, 356-360`) and a clean exit. That is
  the #911 fix, and it holds on Lite, where there is no pod to delete.
- **Pod labels and the teardown selector.** `BuildPod` stamps
  `leoflow.io/attempt-epoch` (`kubernetes.go:80-87`). `DeleteTaskPod`,
  `TaskPodPresence` and the informer's `CachedPodActive` take one attempt
  value `{run, task, try, epoch}`. They keep the `run-id,task-id,try-number`
  server-side selector and filter the epoch in Go, treating an absent label as
  epoch 0 (a label selector cannot express "equals 0 or absent"). Deletion is
  then by name with a UID precondition, as #901 proposed, so a list-then-delete
  can never act on a pod it did not list.
- **The reconciler.** `tryNumberOf` (`reconcile.go:292-302`) becomes
  `attemptOf`, reading both labels. `FailTaskInstanceIfActive`,
  `SucceedTaskInstanceIfActive` and `RescheduleTaskInstanceByIDIfActive` add
  `attempt_epoch = $epoch`. This is the #1130 fix: the superseded pod's record
  no longer matches the replacement.
- **Reaper marks.** `MarkTaskAgentLost`, `MarkTaskPodLost` and
  `MarkTaskDispatchLost` add `try_number` and `attempt_epoch` from the candidate
  row, which the list queries already return or will return.
- **Logs and history.** `logs.Ref` gains `AttemptEpoch`. The key stays
  `{try}.log` for epoch 0, so every log written before the upgrade is still
  found, and becomes `{try}.e{epoch}.log` otherwise. `task_instance_history`
  gains `attempt_epoch` and its unique constraint becomes
  `(task_instance_id, try_number, attempt_epoch)`. The Airflow-compatible tries
  and logs endpoints still address a **try**: they collapse a try's epochs into
  one entry (the latest epoch's state) and serve its log as the epochs'
  streams in order, each preceded by one system line naming the re-place. The
  per-epoch rows are what a native attempts view reads (#863).
- **The outcome record.** See the ADR 0052 amendment: the record carries the
  epoch for transport independence, but on the dedicated-pod path the
  reconciler fences on the **label**, which the control plane wrote and the
  task cannot change (`AutomountServiceAccountToken: false`,
  `kubernetes.go:112`).

**The predicate, including legacy tokens.**

```sql
AND try_number = sqlc.arg(try_number)
AND attempt_epoch = COALESCE(sqlc.narg(attempt_epoch)::int, 0)
```

A token or pod without an epoch is treated as epoch 0. Everything minted
before the upgrade belongs to a row whose epoch the migration set to 0, and
because the epoch is claimed at dispatch, every attempt dispatched after the
upgrade carries an epoch of at least 1, so no legacy credential or unlabeled pod
can match it. A reset-only bump would not be enough: a row that was re-placed,
re-poked or requeued before the upgrade sits at epoch 0 with no rail left to
run, and its first post-upgrade dispatch would share epoch 0 with the
superseded legacy attempt. The one residual case is two legacy attempts that
already aliased one row before the upgrade (today's bug); they stay
indistinguishable until that row's next dispatch, exactly as today.

### Rollout and compatibility

- **Migration.** One additive migration (027 on current `main`; renumber if a
  concurrent migration lands first): the two columns with `DEFAULT 0`, the
  unique-constraint swap on history (safe, since `(id, try)` was already
  unique), and nothing else. Down drops them. No backfill.
- **In-flight attempts at upgrade.** They hold legacy tokens and run in
  unlabeled pods on rows at epoch 0, so they report, heartbeat, resolve
  secrets and get settled exactly as today. If one of them is reaped and
  re-placed after the upgrade, the replacement is epoch 1 and the legacy
  attempt is fenced out. No in-flight attempt is killed by the upgrade itself.
- **Mixed versions during a rolling upgrade.** A new verifier with a token
  from an old minter: legacy rule above. An old verifier with a new token:
  the JSON decoder ignores the unknown claim and fences on `try_number` as
  today. Old agent binaries are unaffected (the token is opaque to them, and
  the new proto fields are additive). The one degraded cell is an **old leader
  dispatching a row that a new leader already bumped** (leadership returning
  to an old replica mid-rollout): its legacy token fails the epoch-0 rule,
  the attempt is told to terminate, and it is reaped and re-placed. That is
  one redundant infra re-place for that attempt, never a wrong outcome. An old
  leader also resets and re-places without bumping; the next dispatch by a new
  leader claims a fresh epoch, so that window adds no aliasing beyond today's.
  The same holds after a rollback followed by a second upgrade.
- **Retiring legacy tokens.** The release that ships the fence accepts legacy
  tokens under the epoch-0 rule and meters them
  (`agent_legacy_attempt_token_total`). The next minor release rejects a
  task-scoped token without the claim as `Unauthenticated`. Renewal preserves
  the absence, so with `auth.max_attempt_credential_lifetime` enabled no
  legacy token outlives that ceiling; operators who disabled it are told so in
  the release notes.
- **Rollback.** The old binary ignores the column and the claim; behavior
  returns to today's. The down migration is only needed to reclaim the column.

### Consequences

- `try_number` is once again only the user's retry count, and the infra rail
  can re-place as often as its budget allows without any fence aliasing.
- The `DeleteTaskPod` invariant comment becomes true for every rail, and the
  operator page that asserts the pin (`website/content/operate/scheduler-resilience.md`,
  per #901) can keep its wording.
- Cost: one integer column on two tables, one label per pod, one claim per
  token, and an extra predicate on queries that already filter by primary key
  or by `(dag_run_id, task_id)`. No new index is needed.

### Alternatives considered

- **Bump `try_number` on an infra re-place.** Rejected by #863 and by Phase 1
  itself: it bills the user's retry budget and mislabels the retry count shown
  in the Airflow-compatible API.
- **Use `infra_attempts` as the epoch.** Rejected above: it is a budget, and it
  does not move on the reschedule, reclaim or dispatch-failure rails, which
  reuse `try_number` too.
- **Fix each symptom locally** (delete by name for #901, a log suffix for #863,
  a Lite liveness gate for #911, a "pod older than the attempt" check for
  #1130). Rejected: four patches, each re-deriving attempt identity from
  timestamps or pod names, and the next fence written against `try_number`
  reopens the class.

### Implementation plan

Each PR is one logical change, failing test first (ADR 0011). PR A0 to A5
target v0.5.1, together with PR B1, B2, B3 and B5 of the ADR 0052 amendment.
PR A6 ships in a later minor release, after the release that meters legacy
tokens.

- **PR A0: reset rails clear `last_heartbeat_at`.** Independent of the epoch;
  ship first. Test first (integration, `internal/storage`): a TI that
  heartbeated, is marked `agent_lost`, re-placed and transitioned to `running`
  must not appear in `ListAgentLostCandidates`. Same test for the retry rail.
- **PR A1: schema and identity plumbing, no behavior change.** Migration;
  every rail listed above bumps `attempt_epoch`; `ResolveTask` claims it.
  Tests: one integration test per rail asserting the epoch strictly
  increases, including two dispatches of one row with no reset between them;
  migration up/down/up test; history keeps both rows for two infra
  re-places on one try.
- **PR A2: token claim.** `agentClaims`, `AgentIdentity`, mint, renew,
  dispatch, exchange annotation and resolver, `WorkAssignment` and `TaskSpec`
  proto fields. Tests in `internal/auth`: round trip; renewal preserves absent,
  0 and N; a legacy token decodes with the presence bit unset. Exchange
  resolver test with and without the annotation field.
- **PR A3: report fence (#911).** The predicate on the six agent-path queries,
  including the missing `try_number` guard on `RescheduleTaskInstance`; the
  legacy metric. Tests: `internal/agentrpc` server test, a report and a
  heartbeat carrying a stale epoch get `should_terminate`; integration test
  reproducing the Lite chain (dispatch-lost, re-place, the old token's RUNNING
  report is rejected); legacy token accepted at epoch 0, rejected at epoch 1.
- **PR A4: pods, reconciler and reapers (#1130, #901).** Label, epoch-filtered
  selectors with name and UID deletes, `attemptOf`, epoch on the three
  reconciler settles and three reaper marks. Test first, as #1130 specifies, in
  `internal/executor/reconcile_test.go`: a terminal pod with a SUCCESS record,
  labels `try-number=1` and no epoch, against a TI at try 1 epoch 1 in
  `queued`; `SucceedTask` must not settle it. Plus: `DeleteTaskPod` with two
  pods of one try and different epochs deletes only the matching one; a
  reaper mark with a stale epoch is a no-op. End to end: extend
  `test/e2e/chaos-runtime.sh` scenario D to force the re-place, keep the
  terminal pod, and assert the second pod is not settled from the first
  pod's record.
- **PR A5: logs and history (#863).** `Ref` epoch, the key scheme, the
  collapsed tries endpoint and the concatenating log reader. Tests: two infra
  attempts on one try produce two objects and one tries entry whose log holds
  both streams in order; an epoch-0 object written before the change is still
  served.
- **PR A6, a later minor release (not v0.5.1): reject legacy tokens.** Test: a task
  token without the claim is `Unauthenticated`; a warm-worker credential is
  unaffected.
