---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /adr/0058-warm-worker-pools.html
# --- end AUTO redirect aliases ---
title: "ADR 0058: Warm worker pools — pod-reuse semantics (N:1)"
linkTitle: "0058 · Warm worker pools — pod-reuse semantics (N:1)"
weight: 580
description: "ADR 0058: Warm worker pools — pod-reuse semantics (N:1)"
---

**Status:** Accepted
**Date:** 2026-08-20
**Amended by:** ADR 0095 (a warm assignment can carry a source-mode version's `dag.py`)
**Relates:** ADR 0053 (admission + placement — this ADR resolves the *reuse semantics* 0053 deferred to its Phase 4/5), ADR 0051 (separate orchestration/execution state machines — the seam a warm worker reports through), ADR 0052 (durable task outcome — a warm worker uses in-band impl #2, not the termination message), ADR 0055 (secret scoping + token liveness — the security gate; satisfied), ADR 0002 (pod-per-task — warm pools are an *amortization* of cold start, not a return to persistent Celery workers), ADR 0022 (per-run staging), ADR 0003 (per-DAG image/dependency isolation)

## Context

ADR 0053 decided **placement** (the O(1) free-slot index, same-DAG-only reuse, the
per-DAG ConfigMap GC-anchor, dedicated-pod-as-degenerate-case, Lite containment,
feature-flag-off). It deliberately left the reuse *semantics* — what it means to
reuse one live pod, holding a live credential, across many attempts — as
"specified but unbuilt Phase 4/5". This ADR decides those semantics.

**Gate status (verified 2026-08-20).** ADR 0055 (Lane E) is the hard prereq that
orders warm-pool. It is merged on `origin/main` (`edccb77`): scope-by-policy
(`7fe922e`), per-attempt token TTL renewed on heartbeat (`9eeafa8`), and the
projected-SA token-exchange transport, flag-gated off (`edccb77`). All three ship
in **safe mode** (permissive / observe / envvar). The gate is *satisfied* (the code
exists and is flippable); warm-pool's safety additionally requires those knobs
*flipped* (see Decision 2 / Decision 10).

**Two predecessors are specified but unbuilt, both on the critical path:**

- **P1 — ADR 0051 Phase 3:** re-home the three reapers + reconciler + `PodManager`
  from `internal/scheduler` to the execution side. A warm *worker* has a lifecycle
  (spawn / idle / recycle / lose) distinct from an *attempt* lifecycle; that
  machine belongs behind the execution seam. No behavior change — its own PR.
- **P2 — ADR 0053 Phase 4:** carry a typed `ExecutionOutcome` up the seam
  (`Executor.Execute` returns a bare `error` today, `internal/executor/executor.go`)
  and add the placement interface with `max[d]=1` as the only case (no warm reuse,
  no security exposure). Its own PR, before reuse. P2 also lands the **cold-start
  measurement harness** (below).

## Decision

Warm worker pools reuse one pod across many attempts of a single **DAG version**,
behind the `Executor` seam, Pro-gated, default OFF (dedicated pod-per-task remains
the default and the OFF path is byte-for-byte today's behavior).

### D1 — Pool key: `dag_version_id`

The pool is keyed by `dag_version_id`, not `dag_id`. A warm pod caches an image +
dependency closure (ADR 0003); keying by `dag_id` would let a pod on the old image
serve new-code attempts (silent stale-code execution). A version bump drains the
old pool by idle-TTL and starts a fresh one; in-flight attempts on the old version
finish on the old pod.

**Consequence to communicate to operators:** warm-pool benefit is a function of
**attempts-per-version**, not attempts-per-DAG. CI/CD-heavy teams that push often
keep thin version-pools and — compounded by D4's per-attempt import cost — can
approach pod-per-task economics.

### D2 — Per-attempt identity via the ADR 0055 exchange split

The pool holds a projected-SA **bootstrap** token (audience
`leoflow-control-plane`; authenticates the pod, fetches no secrets). Each attempt
receives a fresh task-scoped Leoflow JWT **in-band** on the work channel, dying
with the attempt via `IsTaskInstanceLive` and the short per-attempt TTL. The
`SecretKeyRef` fallback transport is a static per-pod credential that cannot carry
a per-attempt identity and is therefore incompatible with reuse.

**`transport=exchange` is necessary but not sufficient.** A per-attempt token only
"dies with the attempt" when `secret_liveness_mode=enforce`; under the shipped
`observe` default, `checkLiveness` logs "would-have-denied" and still delivers the
vault — so a superseded attempt's token on a reused pod would still resolve
secrets. Therefore **`warm_pools_enabled` requires, validated fail-closed at boot,
`agent_token_transport=exchange` AND `secret_liveness_mode=enforce`.**
`secret_scoping=permissive` is tolerable only during the documented warn window and
must reach `enforce` before warm pools are GA.

### D3 — In-band per-attempt durable outcome (ADR 0052 impl #2)

A warm worker does not exit per task, so it reports each attempt's terminal outcome
(`Succeeded / Failed / Unexecutable / Rescheduled`) **in-band, per attempt,
durably**, through the ADR 0051 seam (requires P2). The worker writes the durable
per-attempt record **before** acking the attempt on the control channel
(write-then-ack): a crash between write and ack replays at-least-once, and the
`(run, task, try_number) AND state IN ('queued','running')`-guarded settle makes
the replay idempotent. Cost is ~one gRPC RTT + one settle UPDATE (~5–15 ms/attempt)
— the same round-trip count as today's terminal report, negligible against
attempts that run seconds-to-minutes. **The warm reconciler path never reads pod
phase** (there is none per attempt) — this is a tested invariant, not prose
(guards against the #543-class phase-as-truth conflation).

### D4 — Isolation between attempts: fork from a pristine template

Each attempt runs in a **fresh child process, hard-scrubbed, forked from a pristine
template — never from a sibling attempt.** The worker (a) rebuilds the env from
scratch (only that attempt's `AIRFLOW_VAR_*`/`AIRFLOW_CONN_*` + `LEOFLOW_*`, no
prior-attempt residue; `stripAgentOnly` re-runs per attempt); (b) resets `/tmp` and
per-attempt scratch; (c) drops the prior attempt's task JWT before minting the
next. A long-lived **shared interpreter** (state persisting across attempts) is
**rejected** for v1.

**Isolation invariant (tested, not best-effort):** *each attempt's child forks from
a pristine template, never from a sibling attempt; no attempt observes another
attempt's secrets, env, or writable filesystem state.* Phrasing it as
fork-from-pristine keeps a future **forkserver optimization** open (a parent that
pre-imports the heavy libraries; children CoW-fork from it with rebuilt/scrubbed
env → near-zero import cost with no bleed, because library code carries no tenant
data and secrets re-enter per child) without reopening the invariant.

**Cost, stated honestly.** The warm pod amortizes *image pull + pod schedule +
kubelet admit + agent Register/TokenReview* (~2–15 s). A fresh child re-pays the
*import graph* every attempt (pandas+numpy ~1 s, dbt ~1–4 s, torch/tf ~3–10 s), so
for import-heavy DAGs we retain only ~40–65% of the amortization — not "all". For
import-light DAGs the win over pod-per-task can be small. This is accepted for v1
(isolation over speed); the forkserver optimization is the escape hatch when the
numbers justify it.

**P2 harness requirement:** the cold-start measurement harness MUST report *pod
cost* and *import cost* separately, before the D9 caps and any forkserver decision
are locked.

> **Correction note (2026-08-25, re: #728 / #741).** Two claims above overstate
> what the worker resets, and are corrected here rather than by rewriting the
> accepted text:
>
> - The D4 reset described as "resets `/tmp`" is more precisely **resets `TMPDIR`
>   (redirected into the per-attempt scratch), not `/tmp` itself**. The #728 fix
>   points each child's `TMPDIR` at a subdir of the agent-owned scratch that
>   `resetScratch` wipes between attempts; the container's `/tmp` is never reset by
>   the worker. (With a read-only rootfs, `/tmp` is a shared writable `emptyDir` —
>   see #741 — and is likewise not per-attempt reset.)
> - The isolation invariant ("no attempt observes another attempt's secrets, env,
>   or writable filesystem state") holds only for **writable filesystem state
>   under the agent's control** — the per-attempt scratch and the `TMPDIR`
>   redirected into it. State an attempt writes outside that control (a raw path
>   under `/tmp`, or elsewhere on the image) is **not** wiped between attempts and
>   can be observed by a later attempt on the same worker. The qualifier belongs
>   on the invariant as stated.

### D5 — Staging: exclude staging-dependent DAGs from warm pools in v1

Dynamic per-attempt staging remount is **infeasible** — a pod's volumes/mounts are
immutable after creation, and `subPath`/CSI-ephemeral are fixed at pod-spec time,
so a warm pod (per-DAG-version, across runs) cannot hot-add a future run's per-run
PVC (`StagingClaimName(dagID,runID)`). Therefore **a DAG that uses `/staging` falls
back to dedicated pod-per-task** (statically detectable, documented); warm pools
are offered only to DAGs that don't. Making staging per-DAG (spanning runs) would
change ADR 0022's per-run contract and, if ever wanted, needs its own ADR
amendment — not a warm-pool footnote.

### D6 — Scale-to-zero + owner (inherited from ADR 0053)

`min-idle=0` default (scale to zero — preserves ADR 0002's zero-idle floor),
`idle-TTL` recycle, `max-size = max[d]`. Owner is the existing single-leader
scheduler behind the execution seam — **no new controller**. The free-slot index is
an in-memory cache of Postgres truth; a new leader rebuilds it from live pod/TI
state (see D7).

### D7 — Reaper fan-out + per-pod liveness

Losing a **worker** is one infra event that fans out to **all** in-flight TIs on
that pod, each re-placed on the `infra_attempts` budget without charging the user's
`try_number` (the `InfraFailed` machinery generalizes to a set). The pod-lost
liveness check moves from per-TI to **per-pod/per-worker**, collapsing the
apiserver term from Θ(running TIs) to Θ(pools). The free-slot index is the source
of truth for which TIs a worker holds.

Idempotent-settle + heartbeat-grace bound the failure modes (a stale index either
strands attempts — bounded by heartbeat grace — or double-runs them — bounded to
wasted compute by the `try_number`-guarded idempotent settle), but that is not
enough on its own at leader failover. **On leader failover the reaper reconciles
the index against a live pod LIST** (one-time Θ(pools), not per-tick) before
trusting it to drive reaps. **Precondition, named explicitly:** "double-run is
safe" holds only for **idempotent tasks** (Airflow-compat assumes this; it is a
precondition, not a guarantee warm-pool provides).

### D8 — Feature flag default-off + edition gate

`execution.warm_pools_enabled` (bool, default false, Pro-gated via
`UI.Edition=="pro"`, ignored by Lite). OFF ⇒ dedicated pod-per-task ⇒ today's
behavior, byte-for-byte.

### D9 — Worker max-lifetime / max-attempts-per-worker

`max_attempts_per_worker = 50`, `max_worker_lifetime = 1h`, both operator-tunable
(operator-set, not DAG-author-set — consistent with the secret-scoping stance). On
either cap, drain and recycle. Low enough to bound the leak/stale-image exposure
ADR 0002 rejected Celery for; high enough to keep real amortization.
**Boot-validated ordering:** `max_worker_lifetime ≥ max_attempt_credential_lifetime`
(ADR 0055 Fix #4 — `RenewAgentToken` returns ok=false past the ceiling), or a
worker could be force-recycled mid-attempt by token lapse. With D1's
`dag_version_id` keying, a too-low attempt-cap compounds per-version fragmentation
and starves amortization — the defaults account for this.

### D10 — Security posture of reuse

- **Revocation:** an attempt token is per-attempt and short-TTL, so a
  role/permission revocation lands within TTL. The bootstrap token authenticates
  the pod only (fetches no secrets); it also carries the short-TTL +
  heartbeat-renewal treatment, so a compromised idle worker loses its channel
  within one TTL of the operator cutting it. (This blast-radius argument holds only
  under `secret_liveness_mode=enforce` — hence the D2 boot coupling.)
- **Signal split:** `should_terminate` means **graceful drain** for a warm worker
  ("finish the current attempt, ack, then exit and let the pool respawn") — it
  **must not hard-kill mid-attempt** (tested invariant; #543-class regression
  risk). A separate `terminate_now` is the hard-kill for a compromise signal.
- **Recycle-on-suspicion:** any attempt that trips a security signal (auth failure,
  liveness-deny under enforce) recycles the whole worker rather than serving the
  next attempt on it — cheap, bounded insurance.

### D11 — GC anchor & RBAC (inherited from ADR 0053)

Per-DAG(-version) ConfigMap `leoflow-pool-<key>` as the warm pods' `ownerReference`;
adds `configmaps:[create,delete]` to the executor `Role`, guarded by
`scripts/rbac-covers-executor.sh` in CI. No CRD; a pool-CR stays deferred to ADR
0051 alternative 2. Never a per-task CR.

### D12 — Placement fairness (inherited from ADR 0053)

Ordering under contention is owned by ADR 0053 (lines 304-308); inherited unchanged.
PR-N1 does not expand fairness scope (track separately if it bites).

## Algorithmic evaluation

- **Placement:** O(1) amortized per attempt (index pop/push); bookkeeping Θ(D) =
  active DAG-versions, not Θ(tasks).
- **Per-attempt secret resolution:** because D4 mandates a fresh scrubbed child and
  D2 a fresh per-attempt token, there is **no per-pod secret cache** to leak across
  attempts — one scoped read per attempt (`ListVariablesScoped` /
  `ListConnectionSecretsScoped`), same as pod-per-task. Reuse does not amortize
  secret reads, by design (correctness over caching).
- **Pod-loss liveness:** Θ(pools) per tick, down from Θ(running TIs) — the
  shared-cluster apiserver-load win.
- **Outcome flush (D3):** one synchronous gRPC per attempt — same count as today's
  terminal report; no added round-trips.

## Lite containment

All of warm-pool is inside `KubernetesExecutor` behind the `Executor` seam. Lite's
`SubprocessExecutor` path, `PlanRun`, and dispatch are byte-for-byte unchanged. Flag
OFF ⇒ dedicated pod-per-task.

## Non-goals

PR-13 (namespace-per-tenant) is independent — neither blocks the other. Cross-DAG
and cross-tenant reuse are forbidden forever (ADR 0053). A pool-CR stays deferred.
Lite is unchanged. A shared long-lived interpreter is out of scope for v1.

## PR sequence

1. **P1** (ADR 0051 Phase 3): re-home reapers/reconciler/PodManager behind the
   execution seam. No behavior change.
2. **P2** (ADR 0053 Phase 4): `ExecutionOutcome` up the seam + placement interface
   with `max[d]=1` degenerate case + cold-start harness (pod-cost vs import-cost).
   No warm reuse, no security exposure.
3. **PR-N1** (this ADR): warm reuse — D1–D5, D7, D9, D10. Flag default-off.

## Consequences

- Warm pools cut cold start for repeated attempts of a stable DAG version, at the
  price of the honest limits above (import re-pay under D4, staging exclusion under
  D5, per-version fragmentation under D1).
- The security posture of reuse is *stricter* than pod-per-task's defaults: turning
  warm pools on forces the ADR 0055 enforce flips (D2), so a cluster cannot run
  warm pools in the fail-open secret mode.
- Three tested invariants gate PR-N1: warm reconciler never reads pod phase (D3),
  `should_terminate` drains rather than hard-kills (D10), and each attempt forks
  from a pristine template (D4).

## Measurement & enable readiness (P2c)

Turning warm pools on is a **reliability decision, stated in rates, not median
latency**. Unit/integration tests use fakes — they prove the logic, not the
timing, topology, and Kubernetes behavior. Before flipping
`execution.warm_pools_enabled` on a shared production cluster, drive a warm
workload on a REAL cluster (k3d/CI or staging) and gate on the signals below.

### Reliability signals (mostly already emitted)

The warm reaper/reclaim/placement decisions already surface on the metrics
endpoint through the `SchedulerDecisions` counter (label = decision), scrapeable
today — no new instrumentation needed for the safety bar:

- reclaim by reason: `dispatch_lost_warm_deferred` (H3 double-run defer fired),
  `warm_worker_lost` / `warm_worker_lost_noop` (failover fan-out), the reclaim
  reasons via the N1d-c wiring;
- reconciler health: `warm_pool_pod_created`, `warm_pool_*_error`,
  `warm_pool_tenant_cap_below_min_idle_sum` (M4 misconfig), `warm_pool_anchor_*`.

**Cold-start decomposition** (pod-schedule vs image-pull vs container-start→Register
vs import/SDK-warmup) and **warm-hit vs dedicated end-to-end latency** are read
from **pod events / timestamps on the cluster** during the run (not from an agent
metric); add per-phase histograms only if the pod-event read proves too coarse.

### Go / no-go bar (rates, not means)

- **double-run count == 0** under induced worker death (hard requirement).
- cold-start-failure and worker-register-failure rates below an agreed threshold.
- `dispatch_lost_warm_deferred` non-zero but bounded (proves the H3 defer works
  without over-deferring); lease-expiry reclaim rate low (else `warmLeaseSeconds`
  is mistuned).
- warm-hit rate high in steady state (the pool is actually being used, not
  churning to dedicated).

### Real-cluster scenarios that ONLY a cluster proves (enable gate)

Prove each before enable — none is provable in fakes:

1. HA topology (Hole B): workers reach the leader; a follower refuses + the agent
   reconnects to the leader.
2. Worker dies mid-attempt (SIGKILL / OOM / node loss): fan-out → infra budget
   (not `try_number`), attempt re-runs, **double-run == 0**.
3. Leader failover with in-flight warm attempts: busy workers survive, the new
   leader's empty registry recovers via the durable binding + reaper.
4. H3 defer under real slow-start timing (queued past the 3-min threshold).
5. Node drain / eviction: busy workers finish; reconciler replaces.
6. Image-pull storm: `min_idle=N` new version, all pull at once; no thundering
   herd; dispatch-lost defers on slow pulls.
7. Scale-to-zero and back.
8. M4 under contention: a tenant at cap doesn't starve neighbors or its own floor.
9. Reclaim re-placement (WorkerGone/Refused) vs reaper recovery — no double
   dispatch.

Enable is gated on these passing AND the D2 enforce flips
(`agent_token_transport=exchange`, `secret_liveness_mode=enforce`), plus the
bootstrap worker-scoped exchange (a pre-enable security item, tracked separately).
