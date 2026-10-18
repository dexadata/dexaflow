---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /adr/0053-admission-and-placement.html
# --- end AUTO redirect aliases ---
title: "ADR 0053: Admission + placement — one scheduler-side layer for task concurrency and pod assignment"
linkTitle: 0053 · Admission + placement — one scheduler-side layer for task concurrency and pod assignment
weight: 530
description: "ADR 0053: Admission + placement — one scheduler-side layer for task concurrency and pod assignment"
---

**Status:** Accepted
**Date:** 2026-08-17
**Relates:** ADR 0002 (pod-per-task — amended here: the "no `pool`/`queue` abstraction" consequence is superseded), ADR 0051 (separate the orchestration and execution state machines — the foundation this layer sits on), ADR 0052 (durable task outcome — a warm pod uses its in-band per-attempt transport, impl #2), ADR 0055 (secret scoping + token liveness — the security gate that orders PR-N1), ADR 0031 (scheduler reconciliation loop, `max_active_runs`, `DispatchAttempts`), ADR 0027 (editions: Lite subprocess vs Pro Kubernetes), ADR 0015 (Kubernetes-only container execution)
**Issues:** `max_active_tasks` enforcement (PR-4; memory-note only today, no issue yet); real task pools (PR-8; #31 **closed** as stub-only, needs a fresh issue); quota/APF backpressure classification (PR-2; #127 closed & different, needs a fresh issue). Warm-worker implementation (PR-N1) is tracked separately and **gated** behind ADR 0055 (secret scoping #59 + token liveness/revocation, remainder of #476/ADR 0045).

## Context

Three things that look like separate features are the same subsystem, and Leoflow
has built exactly **one third of it**:

1. **Run-level concurrency exists.** `max_active_runs` per DAG is enforced —
   `hasHeadroom` gates how many runs of a DAG may be active before a due slot is
   created (`internal/scheduler/scheduler.go:666-679`; `#200`), and `PlanRun`
   advances tasks off the resulting run set (`internal/scheduler/plan.go:22-60`).

2. **Task-level concurrency does not.** There is **no** `max_active_tasks`
   enforcement anywhere in the backend. The Airflow-compat UI even *renders the
   label* — `maxActiveTasks` ships in every locale bundle
   (`internal/ui/assets/i18n/locales/*/common.json`) — but no Go code reads it. A
   DAG that fans out 500 parallel tasks gets 500 dispatches; the only cap is the
   run count, which is the wrong axis.

3. **Pools are a decorative stub.** `GET /api/v2/pools` returns a schema-valid
   empty collection so the 3.2.1 UI does not 404 (`internal/api/api_stubs.go:34`,
   `#31`), and there is **no `pool` column read anywhere** in the domain, storage,
   or scheduler. `#31` was *closed as stub-only*. ADR 0002 went further and made
   this a stated non-goal: *"No `pool` or `queue` abstraction in the API. These
   concepts from Celery-era orchestrators do not map to K8s and would mislead
   users."*

That ADR-0002 clause was right for a **1:1 dedicated-pod** world where the
Kubernetes scheduler owns every placement decision and Leoflow owns none.
`spec/shared-cluster-multitenant-spec.md` changes the world. Its cardinality
invariant (§1) is that **a task lives in exactly one pod (atomic), a DAG spans
1..N pods**, and the evolution is that a pod may be **dedicated** (unique to one
task — today) *or* **shared** (a warm worker hosting sibling tasks of the same
DAG). The moment a pod can be *reused*, Leoflow acquires a placement decision of
its own — *which* pod does this task go to — and that decision is meaningless
without a concurrency layer above it deciding *whether* the task may run at all.

**The two are one subsystem, joined at `queued`.** Admission decides whether a
ready task becomes `queued`; placement decides where an admitted task's work
lands. They share a boundary (the `queued` transition in `PlanRun`), share the
scale variables (§2's **N** runs · **T** tasks/run · **D** active DAGs), and only
make sense together: admission without placement throttles into a dispatcher that
still creates a pod per task; placement without admission is an unbounded warm
pool with no backpressure. Building them as three tickets ("add `max_active_tasks`",
"implement pools", "add warm workers") would draw the seam in the wrong place
three times. This ADR names the subsystem so it is drawn once.

There is also a **cluster-facing** admission concern the current dispatcher gets
wrong. Above Leoflow's own gates sits the shared cluster's: a tight
`ResourceQuota` answers a pod `CREATE` with **403**, and API Priority & Fairness
answers with **429**. Today a fan-out of R ready tasks issues R pod `CREATE`s in a
burst with no task-level throttle (§2.2), and a quota 403 drives the task to the
*permanent* terminal `dispatch_failed` (spec B2) — Leoflow **fails the user's task
because the cluster asked it to slow down.** That is a backpressure signal
misread as a fatal error. Self-throttling against the cluster is part of
admission, not a separate networking concern.

### Why the algorithmics make this the lever, not a nicety

§2.3 of the spec is the load-bearing argument. Under the 1:1 model, live pods
`P = Σ dispatched tasks` grows with **task volume**, and the dominant apiserver
cost — the pod-lost reaper's per-running-TI-per-second namespace `LIST` (§2.2,
the headline finding) — grows with it. Under N:1 warm pools,
`P = Σ (warm pools per DAG)` grows with **concurrent-DAG count**, decoupled from
task volume; cold start is paid per pool spin-up and amortized across M tasks, and
(if the pod-lost check is reworked per-pod) the apiserver term collapses from
Θ(running TIs) to Θ(pools) = Θ(D). **Placement moves the dominant infra cost from
task volume to concurrent-DAG count.** That is the whole reason the placement
decision is worth building rather than leaving to `kubectl`-style implicit
pod-creation.

## Decision

Build **admission + placement as one scheduler-side subsystem** with two stages
that meet at the `queued` transition. Admission lives in the shared `PlanRun`
path; placement lives behind the execution seam (ADR 0051) on the
Kubernetes-only path.

### Stage 1 — Admission (concurrency gates), in `PlanRun` before `queued`

A ready task passes an ordered chain of gates before `PlanRun` emits its
`scheduled → queued` transition. The gates are counters against a cap; a task that
fails a gate stays `scheduled` and is re-evaluated next tick (the same
"leave it parked, downstream waits" discipline `PlanRun` already uses for
`readyToDispatch`/`readyToReschedule`, `plan.go:48-54, 98-108`):

```
task ready → [ max_active_runs   ]  RUN-level, per DAG   — EXISTS (hasHeadroom, #200)
           → [ max_active_tasks  ]  TASK-level, per DAG  — NEW (PR-4)
           → [ named pool slots  ]  cross-DAG            — NEW (PR-8; /pools is a stub today)
           → admitted → queued
```

- **`max_active_tasks` (per-DAG):** caps concurrently-active (`queued` +
  `running`) task instances across all runs of one DAG. This is the axis
  `max_active_runs` cannot express — it bounds the fan-out, not the run count.
- **Named pools (cross-DAG):** a pool is a named slot budget that tasks from
  *different* DAGs draw against (the Airflow semantic the stub UI already
  advertises). The `pool` column becomes a real, read column on the task spec;
  `/pools` becomes a real endpoint over real storage. A task with no pool uses an
  implicit default pool, so the gate is always well-defined.

**Above Leoflow's gates: cluster backpressure is retriable-forever, distinct from
permanent errors (PR-2 — a hard requirement).** When a pod `CREATE` returns a
`ResourceQuota` **403** or an APF **429**, the dispatcher must classify it as a
**retriable-forever infra condition**, backed off, *distinct from a permanent
error*, and it **must not** drive the task to `dispatch_failed`. This makes Leoflow
**self-throttle against the shared cluster** — it holds the task and re-offers it
until the cluster has headroom, instead of failing the task (bad for the user) or
hammering the apiserver (bad for neighbors). This reuses exactly the shape ADR
0051 Phase 1 / ADR 0031 Amendment A already established: an infra condition routes
to a bounded backoff counter, **never** the user's `try_number`. The distinction
from `DispatchAttempts`' *bounded* exhaustion is deliberate — a quota 403 is not a
poison placement, it is a *temporary* "no room," so the correct policy is
retry-forever-with-backoff, gated by admission rather than a fatal counter.

### Stage 2 — Placement (pod as target), behind the execution seam

For a task **admitted** by Stage 1 and belonging to DAG *d*, placement chooses the
pod — the O(1)-amortized algorithm from §2.3:

```
slot ← freeSlotIndex[d].pop()                 # O(1) amortized: a warm pod of the SAME DAG with a free slot
if slot exists:        dispatch the attempt in-band to that warm worker      # shared pod
else if pool[d].size < max[d]:  create a new warm pod (dedicated until it gains siblings)
else:                  leave the task 'scheduled'; admission re-offers next tick
```

- **Same-DAG reuse only.** A warm pod hosts 1..M tasks of **one** DAG (the
  cardinality invariant); cross-DAG reuse is never allowed — it would break
  per-DAG image/dependency isolation (ADR 0002/0003) and secret scope.
- **A new pod is dedicated until it gains siblings** — identical to today's model,
  so the 1:1 path is the degenerate case of the N:1 one, not a separate code path.
- **Bookkeeping is O(pools) = O(D), not O(tasks).** The free-slot index is keyed
  by DAG; the per-task cost is a pop/push. Idle pools scale to zero
  (`min-idle=0`), preserving ADR 0002's zero-idle-cost property when there is no
  work — the warm pool is an amortization of cold start, not a persistent worker
  pool (ADR 0002's rejected alternative stays rejected).
- **Outcome transport follows the pod kind (ADR 0052).** A dedicated pod exits at
  the task's end and reports via the **termination message** (impl #1, today). A
  shared warm worker does *not* exit per task, so it reports each attempt's outcome
  **in-band, per attempt, durably** (impl #2). The orchestration layer consumes
  the outcome through the ADR 0051 execution seam **either way** and never reads
  pod phase or pod identity to decide a task's result — which is precisely what
  makes N:1 safe.

### Pod ownership and GC (bare Pod today; per-DAG owner for warm pods later)

Placement raises a concrete substrate question the drafts left implicit: *what
Kubernetes object owns a task pod, and how is it garbage-collected?* The answer
differs by pod kind, and getting it wrong re-fuses concerns ADR 0051 and ADR 0052
just separated.

**Dedicated task pod (today + this release): a bare `Pod`, kept. Reject `Job`.**
The dedicated task pod stays a bare `Pod` created via `client-go`, GC'd
event-drivenly by the informer (ADR 0054's PR-10: terminal + age → delete). A
`Job` wrapper is **rejected** — it buys only `ttlSecondsAfterFinished`, which the
informer reproduces, and it introduces **three collisions**:

1. **`Job` status is derived from pod phase**, which re-introduces exactly the
   *phase-as-truth* conflation ADR 0052 overrules. A `Job` that reads its pod's
   `Failed` phase would settle a lost-report success as a failure — the #543 bug,
   re-imported at the substrate layer.
2. **`ttlSecondsAfterFinished` races the ADR 0052 success-recovery read.** The
   reconciler recovers a lost success by reading the terminated container's
   termination message off pod status; a `Job` TTL that deletes the finished pod
   can win that race and take the durable record with it before it is read.
3. **`Job` pod-recreation breaks the `try_number`-owns-the-attempt invariant.** A
   `Job` recreates its pod on failure with its own backoff, outside Leoflow's
   retry accounting — so an attempt Leoflow believes it owns is silently re-run by
   the `Job` controller, colliding with the `try_number`-guarded settle (ADR 0052)
   and the infra-vs-task retry split (ADR 0051).

**No `ownerReferences` on dedicated pods.** For an ephemeral pod that exits at its
task's end, cascade-GC via `ownerReferences` is near-worthless, and there is no
stable owner object to point at (a run is not a Kubernetes object). The informer's
terminal-plus-age delete is the GC mechanism; it needs no owner. (This is why ADR
0054 removes `ownerReferences` from PR-10's scope.)

**Warm pod (PR-N1): owned by a per-DAG, Θ(D), low-churn owner object.** A warm pod
outlives any single task, so it *does* need cascade GC — but keyed by DAG, not by
task. Its owner is a per-DAG object at Θ(active DAGs) grain, and `ownerReferences`
on the warm pods give cascade GC when the pool is torn down:

- **Default: a native ConfigMap GC-anchor** — `leoflow-pool-<dag>` in the task
  namespace, the warm pods' `ownerReference`. Deleting the anchor cascade-deletes
  the pool. This adds `configmaps:[create,delete]` to the executor `Role`, caught
  by `scripts/rbac-covers-executor.sh` on every CI run. No CRD, no cluster-scoped
  privilege.
- **Upgrade to a pool-CR only if/when ADR 0051 alternative 2 (the read-only CRD
  *projection* for `kubectl`/GitOps visibility) ships** — then the CRD is
  justified by the visibility feature it carries, not smuggled in as a GC anchor.
- **Never a per-task CR.** That is Θ(tasks) high-churn custom-resource load — the
  etcd worst-case ADR 0051 rejected outright. The per-DAG grain dodges it: pool
  churn is Θ(D), not Θ(tasks).

### Lite containment (hard constraint, §0.1)

Admission gates live in `PlanRun`, which Lite and Pro **share**. Therefore, per
the spec's §0.1 rule, the admission layer **must default to today's exact Lite
behavior behind an edition/config flag** and leave Lite byte-for-byte unchanged:

- **`max_active_tasks`** is additive — with no cap configured the gate is a no-op,
  so an existing Lite (or Pro) DAG behaves exactly as today. Enabling it is opt-in.
- **Named pools are a Pro concept.** Lite is a single subprocess executor with
  no cluster and no cross-node slot budget to arbitrate; **Lite has no pools.** The
  pool gate is gated on the Pro edition and defaults off. Lite's admission chain is
  `max_active_runs → (optional max_active_tasks) → admitted`, unchanged in shape.
- **Placement / warm pools are Kubernetes-only**, entirely behind the execution
  seam (ADR 0051). Lite dispatches a subprocess in-process, as it does today; the
  free-slot index, warm-pool lifecycle, in-band transport, and ConfigMap GC-anchor
  are never constructed on the Lite path. This is containment technique (1) — put
  the change behind the executor interface — layered under technique (2) — the Pro
  flag on the shared `PlanRun` gate.

The net Lite guarantee: with the flag at its default, no observable Lite behavior
changes. Any change that would alter that requires the maintainer conversation
§0.1 mandates.

### Amends ADR 0002

ADR 0002's consequence — *"No `pool` or `queue` abstraction in the API"* — is
**superseded** by this ADR (see ADR 0002's 2026-08 Amendment). The ephemeral-default
and zero-idle-cost framing is **kept** (warm pools scale to zero; a task is still
atomic to one pod). What changes is only that a pod may be a *reused* placement
target and that Leoflow now owns a task-level concurrency and placement decision
the Kubernetes scheduler cannot make for it.

## Key properties

- **One subsystem, one seam.** Admission and placement meet at the `queued`
  transition. Admission is edition-shared (`PlanRun`); placement is Pro-only
  (execution seam). Neither is useful alone; both are specified together so the
  `queued` boundary is drawn once.
- **O(1) amortized placement; O(D) bookkeeping.** Per-task cost is a slot
  pop/push against a per-DAG index; pool state is Θ(active DAGs), not Θ(tasks).
  This is the property that decouples infra cost from task volume (§2.3).
- **Cost-efficient by default; warmth is opt-in.** The default is the cheapest
  posture — `min-idle=0`, scale-to-zero — so an idle platform costs nothing: a DAG
  that is not running holds no warm pods, no reserved capacity, no standing cost.
  Warmth (`min-idle > 0` for a hot DAG, to hide cold start) is an **explicit,
  per-DAG opt-in the operator consciously pays for** — never a default, and never
  the Cloud-Composer-style always-on minimum node pool. Taken to its limit the
  cheapest default is *no warm pool at all* (pure on-demand); the warm pool must
  **earn its place on measured cold-start cost** (the PR-0 harness), not be
  assumed. This is the design principle behind the `min-idle=0` default in Stage 2
  — a resource that is not being used must not be wasted.
- **Bare Pod today, per-DAG owner for warm pods.** Dedicated pods are bare `Pod`s
  GC'd by the informer with no `ownerReferences`; `Job` is rejected for the three
  collisions above; warm pods are cascade-GC'd via a per-DAG ConfigMap anchor at
  Θ(D) grain, never a per-task CR.
- **Cluster backpressure is a first-class, non-terminal condition.** Quota 403 /
  APF 429 → retriable-forever with backoff, never `dispatch_failed`, never a
  `try_number` charge. Leoflow becomes a self-throttling good tenant (§3, §4.2).
- **Retry-budget discipline is inherited, not reinvented.** Every no-budget rail
  this ADR needs already exists: `DispatchAttempts` (dispatch-failed backoff, ADR
  0031 Amendment A), the infra-attempt counter (ADR 0051 Phase 1). Admission's
  backpressure counter is one more member of that family — bounded backoff for a
  *temporary* condition, terminating in a named, operator-visible state only if it
  must terminate at all.
- **Same-DAG-only reuse preserves isolation and secret scope.** A warm pod never
  crosses DAG boundaries, so per-DAG image/dependency isolation and per-tenant
  secret scope are unweakened by reuse.
- **Airflow-compatible surface.** `max_active_tasks` and pools are Airflow
  concepts the compat UI already renders; this makes the backend honour what the
  UI promises, instead of showing a control that does nothing.

## Consequences

- **Fixes the fan-out-vs-quota bug (spec B2).** A 500-task fan-out under a tight
  quota stops producing 500 permanent `dispatch_failed`s; it self-throttles and
  drains as the cluster frees room. This is the operational blocker PR-2 targets.
- **`max_active_tasks` becomes real.** The UI label stops lying; DAG authors get
  the fan-out cap Airflow users expect.
- **`/pools` and the `pool` column become load-bearing.** A closed-as-stub feature
  (`#31`) is reopened as real storage + a real endpoint + a read column. This is a
  schema addition (a `pool` column on the task spec, pool definitions in storage)
  — additive, defaulting to an implicit default pool so existing DAGs are
  unaffected.
- **The scheduler grows a concurrency layer, but stays orchestration-only.**
  Admission is planning (`PlanRun`), which is squarely orchestration. Placement
  lives behind the execution seam and does **not** re-fuse pod knowledge into the
  scheduler — it respects the ADR 0051 boundary. The scheduler still never reads
  pod phase to decide a task outcome.
- **The executor `Role` grows one grant only when warm pods land.** The per-DAG
  ConfigMap GC-anchor adds `configmaps:[create,delete]` to the namespaced executor
  `Role` at PR-N1 — checked by `scripts/rbac-covers-executor.sh`, no cluster-scoped
  privilege, no CRD. Dedicated-pod GC (this release) needs no new grant.
- **Warm workers are gated on security, and this ADR does not unlock them.** The
  admission half (PR-4/PR-8) and the placement *interface* can land without warm
  pods; the shared-pod warm worker (PR-N1, impl #2) holds a long-lived tenant
  token and therefore **widens the B1 exfiltration window**. Per spec §4.4 it is
  **gated behind ADR 0055 (secret scoping #59 + token liveness/revocation, remainder
  of #476/ADR 0045)** and must not precede it. This ADR specifies the layer; it does
  not authorize turning on shared pods.

### Honest hard parts

- **Admission fairness under contention.** With `max_active_tasks` and pool caps,
  tasks compete for slots. A naive "first ready wins" gate can starve a DAG whose
  tasks always lose the race. The gate needs a defined ordering (at minimum stable,
  ideally priority/FIFO-aware) so admission is not accidentally unfair. Deciding
  that ordering is real design, not a config default.
- **Backpressure vs. poison placement.** A quota 403 (temporary, retry forever)
  and an unschedulable pod (bad image, unsatisfiable resources — poison, must
  terminate) can look similar at the `CREATE` call. Misclassifying poison as
  backpressure is an infinite re-offer loop; misclassifying backpressure as poison
  is the B2 bug we are fixing. The classifier must distinguish 403/429 (retry
  forever) from `Invalid`/`Forbidden`-for-policy and from a pod that admits but
  never schedules (bound the latter, exactly as `DispatchAttempts` bounds dispatch
  failure).
- **Warm-pod reaping fans one infra event out to many TIs.** Losing a warm pod is
  **one** infra event affecting **all** in-flight task instances on it; each must
  be re-placed **without** consuming its user retry budget (ADR 0051 Phase 1
  generalizes to the fan-out; the infra marker stays per-TI). GC is coarser too —
  a warm pod is owned by a per-DAG ConfigMap anchor (PR-N1) via `ownerReferences`,
  not by a single run. Both are §4.3 concerns this ADR flags but leaves to PR-N1.
- **The free-slot index is scheduler state that must survive leader failover.** An
  in-memory per-DAG index is a cache of a truth that must be reconstructible from
  Postgres (the single source of truth, ADR 0031). A new leader must rebuild it
  from live pod/TI state without double-placing. This is tractable under the
  reconciliation model (DB-derived every tick) but each phase must prove it.

## Alternatives considered

1. **Three separate features (`max_active_tasks`, pools, warm workers as
   independent tickets).** Rejected. They share the `queued` seam, the scale
   variables, and the retry-budget discipline; built separately, the boundary is
   drawn three times and inconsistently. The spec (§4.2, §6 PR-4+PR-8 explicitly
   *merged*) treats them as one layer for exactly this reason.

2. **Keep ADR 0002's "no pools" stance; rely on `max_active_runs` + the
   Kubernetes scheduler.** Rejected. `max_active_runs` bounds the wrong axis (run
   count, not fan-out), and the kube-scheduler cannot express *task-level*
   concurrency or *reuse* — it places pods, it does not decide whether a pod should
   exist. This is precisely the "where Kubernetes cannot, Leoflow must" boundary
   (§3). It also leaves the B2 quota bug unfixed.

3. **Let the cluster's quota/APF be the only task-concurrency control.** Rejected.
   That is the current behavior, and it produces the B2 bug: the cluster's "no room"
   (403/429) is a *backpressure* signal, but the dispatcher reads it as a *fatal*
   error and permanently fails the user's task. Leoflow must own admission so it can
   translate backpressure into self-throttle instead of failure.

4. **A persistent worker pool (Celery-style) instead of warm-per-DAG pods.**
   Rejected — same grounds as ADR 0002's original rejection. Warm pools scale to
   zero (`min-idle=0`), keep per-DAG isolation, and are an *amortization* of cold
   start, not a standing fleet of idle workers sharing one Python process.

5. **Admission in the dispatcher instead of `PlanRun`.** Rejected. Admission is a
   planning decision — "should this task become `queued`" — and belongs with the
   other planning gates (`max_active_runs`, `readyToDispatch`,
   `readyToReschedule`) that already live in `PlanRun`. Putting it in the
   dispatcher would split the concurrency decision across two layers and duplicate
   the "leave it parked, downstream waits" discipline `PlanRun` already owns.

6. **Wrap the task pod in a `Job` for GC/retry.** Rejected. A `Job` buys only
   `ttlSecondsAfterFinished` (which the informer reproduces) and imports three
   collisions: `Job` status is derived from pod phase (re-introducing the
   phase-as-truth ADR 0052 overrules); `ttlSecondsAfterFinished` races the ADR 0052
   success-recovery read of the termination message; and `Job` pod-recreation runs
   attempts outside Leoflow's `try_number` accounting, breaking the
   `try_number`-owns-the-attempt invariant. The bare `Pod` + informer GC keeps all
   three under Leoflow's control.

## Phased path

Each phase ships **independently**, **failing-test-first** (ADR 0011), behind the
edition/config flag, and is **ADR-gated where it changes observable behavior**.
Ordered by value-over-blast and by the §5.1 security gate:

- **Phase 1 — cluster backpressure classification (PR-2; highest value, lowest
  blast).** Classify quota 403 / APF 429 as retriable-forever, distinct from
  permanent errors; a fan-out under a tight quota self-throttles instead of failing.
  Fixes B2. Touches shared dispatch classification → **Lite's subprocess errors
  must classify exactly as before** (§0.1 🔴 discuss-first item).
- **Phase 2 — `max_active_tasks` (PR-4).** Per-DAG task-level admission gate in
  `PlanRun`, additive, no-op when unconfigured. File the enforcement issue
  (memory-note only today).
- **Phase 3 — named pools (PR-8).** Real `pool` column (read), real `/pools`
  storage + endpoint; implicit default pool so existing DAGs are unaffected.
  Reopen the closed-as-stub `#31`. Pro-only gate; Lite has no pools.
- **Phase 4 — placement interface (Kubernetes-only).** Introduce the per-DAG
  free-slot index and the placement decision behind the execution seam, with the
  dedicated-pod path as the degenerate `max[d]=1` case — no warm reuse yet, so no
  security exposure. Dedicated pods stay bare `Pod`s GC'd by the informer, no
  `ownerReferences`. Proves failover-safe index reconstruction.
- **Phase 5 — warm-worker shared pods (PR-N1). GATED.** In-band per-attempt
  durable outcome (ADR 0052 impl #2) + warm-pool lifecycle (§4.3) + the per-DAG
  ConfigMap GC-anchor with `ownerReferences` cascade GC. **Blocked on ADR 0055
  (secret scoping #59 + token liveness/revocation, remainder of #476/ADR 0045)** —
  must not land until a warm pod cannot widen the B1 exfiltration window.

## References

- `spec/shared-cluster-multitenant-spec.md` §2.3 (the N:1 regime change — infra
  cost from task volume → concurrent-DAG count), §4.2 (admission + placement as one
  subsystem), §4.3 (warm-pool lifecycle), §4.4 (the security gate that orders the
  roadmap), §0.1 (Lite containment), §6 (PR-2, PR-4+PR-8 merged, PR-N1), §7 (ADR
  moves — this is the "New ADR — Admission + placement layer").
- ADR 0002 — pod-per-task (the "no pools" consequence amended here).
- ADR 0051 — separate the orchestration and execution state machines (the seam
  placement sits behind; the no-budget infra-retry rail admission generalizes; the
  read-only CRD projection, alternative 2, that would later justify a pool-CR).
- ADR 0052 — durable task outcome (impl #1 termination message / impl #2 warm
  in-band per-attempt; the success-recovery read `Job` TTL would race).
- ADR 0054 — shared-cluster coexistence (PR-10 informer GC; `ownerReferences`
  deferred to PR-N1).
- ADR 0055 — secret scoping + token liveness (the security gate that orders PR-N1).
- ADR 0031 — scheduler reconciliation loop; `hasHeadroom`/`max_active_runs`;
  `DispatchAttempts` / Amendment A (infra ≠ task failure).
- `internal/scheduler/plan.go:22-60, 98-108` — `PlanRun`, the `queued` transition,
  and the "leave it parked" gates admission joins.
- `internal/scheduler/scheduler.go:666-679` — `hasHeadroom` (`max_active_runs`,
  the one existing concurrency gate).
- `internal/api/api_stubs.go:34` — the empty `/api/v2/pools` stub (`#31`).
- `internal/ui/assets/i18n/locales/*/common.json` — the `maxActiveTasks` label the
  UI renders and the backend never reads.
- `scripts/rbac-covers-executor.sh` — the CI guard that catches the
  `configmaps:[create,delete]` grant the warm-pool GC-anchor adds.
