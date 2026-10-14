---
title: "ADR 0082: a warm pool follows the DAG's current version, started when it becomes current and drained when it stops being current"
linkTitle: "0082 · Warm pool follows the current version"
weight: 820
description: "ADR 0082: with warm pools on, the reconciler keeps a pool for the current version of every unpaused DAG from the moment it becomes current, and for an active run's version only while it is still current. A superseded version keeps no idle buffer: its idle workers are deleted and busy ones finish their attempt first. Replaces the version bump paragraph of ADR 0058 D1."
---

**Status:** Proposed
**Date:** 2026-10-14
**Relates:** ADR 0058 (warm worker pools; this ADR replaces the "a version bump drains the old pool by idle-TTL" sentence of D1 and keeps the rest of D1 and D2), ADR 0055 (per-attempt identity), ADR 0002 (pod per task, still the default)
**Issues:** #1707

## Context

ADR 0058 D1 keys a warm pool by `dag_version_id`. The reconciler's targets
were the versions of the active runs (`ActiveWarmTargets` in
`internal/storage/scheduler_store.go`), so:

- a new version's pool started with its first run, whose first attempts paid
  the cold start the pool is there to avoid;
- the previous version kept its idle buffer while any of its runs was active,
  and its idle workers were recycled only by the worker idle TTL;
- the deploy report (#1849) had to say "start with this version's first run",
  and could not truthfully say that workers were warming or that the
  previous pool was draining.

A deploy should behave the same with warm pools on and off (#1707): the
version that is current is the one new runs use, so it is the one to keep
warm.

## Decision

1. **The target set is computed from the current version.** A
   `dag_version` is a warm target when it is the current version of an
   active, unpaused DAG (prewarm), or when an active run that needs warm
   workers uses it and it is still its DAG's current version. A run of a DAG
   with no current version keeps its version's target, as before. The
   computation is `warmCandidates` in `internal/storage`, read by the
   reconciler through `executor.WarmTargetSource`; no new state is stored.
2. **A version becomes current through any path the same way.** Register
   (deploy, promote) and `PUT /api/v2/dags/{dag_id}/currentVersion`
   (rollback) all move `dags.current_version_id`; the next reconcile sees it.
   There is no push from the API to the reconciler: the reconciler runs on
   the leader only, is idempotent, and converges within one tick (30 s by
   default), so any replica can serve the register.
3. **A prewarm target needs an idle target.** The current version gets a
   target only when its `EffectiveMinIdle` (the DAG's `min_idle_workers`, else
   the operator floor `execution.min_idle_workers`, clamped to
   `max_pool_size`) is above 0, so a server with the default floor of 0 keeps
   no pool for a DAG that declares none. A paused DAG is not prewarmed; its
   running runs keep the target as before.
4. **A superseded version drains at once, never by killing an attempt.** It
   is no longer a target, so the reconciler's existing drain applies: idle
   workers are deleted (each delete re-reads the busy set first, #1601), a
   worker bound to a queued or running attempt is kept until that attempt
   ends, and the GC anchor goes once no pod is left. No new assignment
   targets the drained pool, since no idle worker of it is left to take one.
5. **In-flight runs keep their version.** A run's attempts always execute
   its own version with their own identity (ADR 0058 D1 and D2, ADR 0055).
   Attempts of a superseded run that start after the switch find no idle
   worker of their version and take a dedicated pod, the warm-miss path
   dispatch already has.
6. **Observability.** The reconciler logs and counts each prewarm start and
   each drain start once per leader
   (`dexaflow_scheduler_decisions_total`, `warm_pool_prewarm_started`,
   `warm_pool_drain_started`), and logs when a drained pool's anchor is
   deleted.

## Alternatives considered

- **Keep the old pool while its runs are active.** Long runs of a superseded
  version would keep an idle buffer of an image no new run uses, multiplied by
  every deploy during the run. Their later attempts lose warmth instead; they
  still run, on dedicated pods.
- **Trigger the pool from the register handler.** The handler runs on any
  replica, the reconciler only on the leader; a push would need a channel
  between replicas and would miss rollbacks and direct database changes. The
  reconciler reading the current version covers every path.
- **A setting to opt into prewarm.** A pool exists only when the operator
  turned warm pools on and asked for idle workers; keeping them for the
  version that will run is what that request means. The per-tenant cap
  (`max_warm_pods_per_tenant`) still bounds the total.

## Consequences

- With `execution.min_idle_workers` above 0, every unpaused DAG keeps that
  many idle warm workers even with no run: the standing cost is per unpaused
  DAG, not per active version. An idle worker is still recycled after
  `worker_idle_ttl` and replaced at the next reconcile.
- Each reconcile tick reads one more query (`ListWarmCurrentVersions`, over
  the unpaused DAGs and the DAGs of active runs) and the current versions'
  specs through the spec cache, where current versions are pinned.
- A deploy's image pull moves from the first run to the deploy. Pools keyed
  by environment, which would survive a code-only deploy, are a separate
  decision (#1707).
- The deploy report says `warming now` (or that the workers start at the
  unpause) and `previous pool draining`, which are now true.
