---
title: "ADR 0082: a warm pool follows the DAG's current version, started when it becomes current and drained when it stops being current"
linkTitle: "0082 · Warm pool follows the current version"
weight: 820
description: "ADR 0082: with warm pools on, the reconciler keeps a pool for the current version of every unpaused DAG that declares min_idle_workers from the moment it becomes current, and for an active run's version only while it is still current. A superseded version keeps no idle buffer: its idle workers are deleted and busy ones finish their attempt first. Replaces the version bump paragraph of ADR 0058 D1."
---

**Status:** Accepted
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
   active, unpaused DAG that declares `min_idle_workers` (prewarm, D3), or
   when an active run that needs warm workers uses it and it is still its
   DAG's current version. A run of a DAG
   with no current version keeps its version's target, as before. The
   computation is `warmCandidates` in `internal/storage`, read by the
   reconciler through `executor.WarmTargetSource`; no new state is stored.
2. **A version becomes current through any path the same way.** Register
   (deploy, promote) and `PUT /api/v2/dags/{dag_id}/currentVersion`
   (rollback) all move `dags.current_version_id`; the next reconcile sees it.
   There is no push from the API to the reconciler: the reconciler runs on
   the leader only, is idempotent, and converges within one tick (30 s by
   default), so any replica can serve the register.
3. **Only a DAG that declares `min_idle_workers` is prewarmed.** The
   current version of a DAG with no active run gets a target only when the
   DAG itself declares `min_idle_workers` above 0 (clamped to
   `max_pool_size`). The operator floor `execution.min_idle_workers` prewarms
   nothing: it still applies to a version an active run holds, as before, so
   a DAG that declares none gets its workers with its first run. The
   declared value is read from the spec in SQL (`ListWarmCurrentVersions`),
   so no spec of a DAG that is not prewarmed is loaded. A paused DAG is not
   prewarmed; its running runs keep the target as before. The drain of D4
   applies to every superseded version, declared or not. The author declares
   it as `min_idle_workers` in `dexaflow.yaml`; the compile copies it into
   `dag.json` (the parser does not emit it), and refuses it on a DAG with
   `staging.enabled`, which is never warm (ADR 0058 D5).
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
   `warm_pool_drain_started`, and `warm_pool_prewarm_rationed` and
   `warm_pool_run_floor_over_retiring_prewarm` for the cap of D7), and logs
   when a drained pool's anchor is deleted. A new leader starts without that memory, so it logs and counts
   each prewarmed or draining pool it finds once more.

7. **Prewarm is bounded by the per-tenant cap.** A prewarm-only target (the
   current version, no active run) never raises a tenant's budget the way
   the idle floor of a version with an active run does (ADR 0058, the
   per-tenant cap). The prewarm floors of a tenant share what
   `max_warm_pods_per_tenant` leaves after the floors of its versions with an
   active run, served by DAG id (then version id), each taking the smaller of
   its floor and what is left. The order is stable, so the same DAGs keep
   their pools on every tick. A rationed target creates toward its share
   only; when its share drops below the idle workers it has (a DAG that
   sorts first appears, or a run of another version needs its floor), its
   idle workers are not deleted: they retire with `worker_idle_ttl` and are
   not replaced, so a share that moves between ticks does not churn pods.
   They are not charged against the floor of a version with an active run,
   which is created at once, so the cap can be exceeded by those idle
   workers for at most one idle TTL after each share drop: a worker's idle
   TTL runs from when it connected or finished its last attempt, so it has
   retired one idle TTL after the drop at the latest. The bound holds per
   drop, not overall. A run version with no prewarm of its own drains when
   its run ends, the prewarmed pool gets its share back and refills, and
   the next run of that version exceeds the cap again, each time for at
   most the shorter of the run and one idle TTL. Each tick that grants such
   creates logs them and counts `warm_pool_run_floor_over_retiring_prewarm`,
   so the pods above `max_warm_pods_per_tenant` are explained. A prewarm
   target that took the share waits for those workers to retire, so a
   prewarm target never creates past the cap. A busy worker is never deleted, as before. Once a run of a rationed
   version is active, it is a run target again and its floor is honored.
   A tenant's rationed set is logged (at most 10 versions and a count of the
   rest) and counted as `warm_pool_prewarm_rationed` when it changes, not on
   every tick; `warm_pool_tenant_cap_below_min_idle_sum` stays the signal
   that the floors of versions with an active run exceed the cap. A target
   rationed to nothing has not started, so its `warm_pool_prewarm_started`
   (D6) is counted when it later gets a share.

## Alternatives considered

- **Keep the old pool while its runs are active.** Long runs of a superseded
  version would keep an idle buffer of an image no new run uses, multiplied by
  every deploy during the run. Their later attempts lose warmth instead; they
  still run, on dedicated pods.
- **Trigger the pool from the register handler.** The handler runs on any
  replica, the reconciler only on the leader; a push would need a channel
  between replicas and would miss rollbacks and direct database changes. The
  reconciler reading the current version covers every path.
- **Prewarm under the operator floor too.** With a floor of N, every
  unpaused DAG would hold N idle pods with no run. The per-tenant cap would
  ration them (D7), but which DAGs get warmth would then depend on DAG ids,
  not on any author asking for it. The
  DAG's own declaration is the opt in: the author asks for warmth for this
  DAG, the operator caps how much.
- **Let prewarm floors raise the tenant budget, as run floors do.** A tenant
  could then pin `max_pool_size` idle pods per unpaused DAG with no run, and
  `max_warm_pods_per_tenant` would bound nothing. A run floor raises the
  budget because a run needs it now; a prewarm floor is an anticipation, so
  it takes only what the cap leaves.
- **A separate setting to opt into prewarm.** The declaration already is one,
  per DAG, and the operator keeps `warm_pools_enabled` and `max_pool_size`.

## Consequences

- **Cost.** Each unpaused DAG that declares `min_idle_workers: N` keeps N
  idle warm pods (at most `max_pool_size`) even with no run, from the moment
  its version is current, within `max_warm_pods_per_tenant` (D7): a tenant's
  prewarmed pods never exceed what the cap leaves after the idle floors of
  its versions with an active run (save idle workers retiring for at most
  one `worker_idle_ttl` after a share drops), and the DAGs last by id get
  fewer idle workers when the floors do not fit. An idle worker is still recycled after
  `worker_idle_ttl` and replaced at the next reconcile. The operator floor
  alone adds no standing cost.
- **A new version per change.** `min_idle_workers` is part of
  `dexaflow.yaml`, which the content label hashes, so changing it gives the
  DAG a new version: a new image tag, and the previous version's pool
  drains (D4).
- **Older servers.** A server older than 0.5.4 accepts a `dag.json` carrying
  `min_idle_workers` but applies it only while a run of the version is
  active, with no prewarm.
- **Reads per tick.** One more query, `ListWarmCurrentVersions`, which scans
  `dags` for the unpaused ones and the DAGs of active runs and probes each
  one's current version by primary key (about 60 ms with 100k DAGs, 3,000 of
  them unpaused, on a laptop Postgres 16; the cost follows the unpaused DAGs,
  not the versions). The specs of the targets are read through the spec
  cache in one batch query on a cold cache (`GetDagVersionsByIDs`).
- **Leader memory.** The leader pins in its spec cache the decoded current
  spec of each prewarmed DAG, as every reader of a current version does
  (`spec_cache.go`: current versions are pinned outside the LRU bound), so
  its heap grows with the number of DAGs it prewarms; the soak's leak check
  watches it.
- A deploy's image pull moves from the first run to the deploy. Pools keyed
  by environment, which would survive a code-only deploy, are a separate
  decision (#1707).
- The deploy report says `warming now` (or that the workers start at the
  unpause) and `previous pool draining`, which are now true.
