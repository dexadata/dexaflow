---
title: "ADR 0089: a warm pool is keyed by the DAG's environment, so versions that share a dependency image and runtime settings share workers"
linkTitle: "0089 · Warm pools keyed by environment"
weight: 890
description: "ADR 0089: a warm pool is keyed by tenant, DAG, the dependency image digest and a hash of the warm pod's runtime settings instead of by dag_version_id. A version built on the code-layer path delivers its code to the worker per attempt as a bundle verified by digest before import, so a deploy that changes only code keeps the pool warm. Replaces the pool key of ADR 0058 D1 for those versions; every other version keeps its per-version pool."
---

**Status:** Accepted
**Date:** 2026-10-17
**Relates:** ADR 0058 (warm worker pools; this ADR replaces the pool key of D1 for versions that carry a code bundle, and keeps D2 to D12), ADR 0082 (a warm pool follows the current version; its targets, prewarm, drain and per-tenant cap now apply to pools instead of versions), ADR 0077 (dependency image and code layer; the environment is its dependency image and the bundle is its code layer), ADR 0053 (same-DAG reuse only; kept), ADR 0055 (per-attempt identity; kept), ADR 0048 (no user-influenced network request in the control plane; decides where the code comes from), ADR 0067 (source mode; still dedicated), ADR 0003 (a version is a `dag.json` and an image)
**Issues:** #1893, #1707

## Context

ADR 0058 D1 keys a warm pool by `dag_version_id`, because a warm pod caches
an image and its dependency closure, and a pod on an old image must never run
new code. ADR 0082 made the pool follow the current version: a deploy starts
the new version's pool and drains the previous one. Both are correct, and
together they make a deploy cost a whole pool:

- **Every deploy is a new version, and a new version is a new pool.** The
  version's image is in the pod spec (`BuildWarmPod`, `internal/executor/warmpod.go`),
  the pod carries the version as its `leoflow.io/dag-version-id` label, its
  bootstrap credential is minted for that version, and registration refuses
  a worker whose `WorkerRegister.dag_version_id` differs from the version in
  its token (`registerFromStream`, `internal/agentrpc/await_assignment.go`).
  Dispatch offers an attempt only to a free worker of the attempt's own
  version (`WarmPlacer.Assign(dagVersionID, ...)`,
  `internal/dispatch/dispatch.go`). The GC anchor is per version too
  (`warmAnchorName`).
- **Most deploys change only code.** ADR 0077 splits a pushed build of the
  generated Dockerfile into a dependency image (base, system packages, Python
  dependencies, environment, user), reused by its key, and one deterministic
  code layer. A deploy that changed only `dag.py` reuses the dependency image
  and appends a new code layer: the new version's image differs from the
  previous one by that layer only, yet its pool starts from nothing and pulls
  the whole image again on every node it lands on.
- **Superseded runs lose warmth.** Under ADR 0082 D5, attempts of a run whose
  version is no longer current find no idle worker of their version and take
  a dedicated pod, even when the current version runs the very same
  dependencies.
- **`min_idle_workers` is hashed into the content label** (ADR 0082,
  consequences), so even changing the pool's own floor drains the pool.
- The operator docs already call "CI/CD-heavy teams that push often" a poor
  fit for warm pools (`website/content/operate/warm-pools.md`), and ADR 0058
  D9 notes that per-version fragmentation starves the amortization.

What a warm worker holds for a version is two things: an environment (the
image the pod runs, plus the pod settings it was created with) and the
version's code, which today is part of the image. The task spec, the
per-attempt token, the task's environment variables and its secrets already
arrive per attempt (ADR 0058 D2 and D4: `GetTaskSpec` on the attempt token,
`buildEnv` from scratch for each child). Only the code is bound to the pod.

## Decision

### 1. The pool key

A warm pool is identified by its **environment key**, the SHA-256 of a format
version and these fields, in this order:

1. **`tenant_id`.** A pool never serves two tenants.
2. **`dag_id`** (the DAG's UUID). A pool never serves two DAGs (ADR 0053,
   ADR 0058 non-goals). Sharing across the DAGs of one tenant is not decided
   here; see the alternatives.
3. **The dependency image, by digest.** The `deps-<key>` image of ADR 0077,
   as the `repository@sha256:<digest>` the registry returned when it was
   pushed (ADR 0075). For a multi-platform build it is the digest of the
   index, which is what the pod spec names.
4. **The runtime settings hash**: the SHA-256 of the warm pod template the
   reconciler would build for that image with the pod name, the anchor and the
   token left out. It covers what `WarmPodSpec` carries from the operator's
   configuration: the ServiceAccount, the resources (one unit or the default
   limits), the pod security settings, `warm_read_only_root_filesystem`, the
   pull policy, the operator's pod labels and annotations, the agent CA, the
   token transport and its audience and expiry, and the D9 lifecycle caps.

The digest, never the tag, because a tag names whatever was pushed last
under it: two versions registered with the same `deps-*` tag could hold
different content, which is exactly the silent stale-code execution D1
forbids. A digest names the content, and it is what the kubelet pulls. The
runtime settings are in the key so that a configuration change cannot leave a
pool of pods built under the old settings serving new attempts; today such
pods keep their old spec until they recycle.

What is **not** in the key, because it never reaches the pod at creation:
the task spec (`dag.json`), the code, the run's `conf`, the per-attempt
token, the task's environment variables, its declared secrets, Variables and
Connections, and `min_idle_workers`. All of these are delivered or resolved
per attempt, on the attempt's own token, into a child forked from a pristine
template (ADR 0058 D2, D4). Environment variables that the project declares
for the image are in the dependency image's Dockerfile (ADR 0077 decision 1),
so they are in the digest.

### 2. Which versions are keyed by environment

A version is keyed by environment when all of these hold:

- it was registered with an **environment record**: the dependency image
  pinned by digest and a **code bundle** (decision 3). The CLI sends it for a
  build that took the code-layer path of ADR 0077, and only when the
  dependency image's runtime declares that it can load a bundle;
- the server runs with **`execution.warm_pool_key: environment`**
  (decision 7);
- `execution.warm_read_only_root_filesystem` and
  `execution.warm_require_attempt_home_sweep` are on. With a writable root,
  an attempt can leave a module on the import path that a later attempt of
  another version imports, which is the stale-code execution D1 was written
  to prevent. With a read-only root, everything an attempt can write is
  emptied before the next one (the `/tmp`, `/dev/shm`, `HOME` and scratch
  sweep, #1678), and the code root of decision 4 is rewritten for each
  attempt. The server refuses `warm_pool_key: environment` at boot without
  both settings.

Every other version keeps the key it has today, `dag_version_id`: a full
build, a project Dockerfile, an unpinned image, a version registered by an
older CLI, and every version while the setting is `version`. Source mode and
staging stay dedicated (ADR 0067 section 3, ADR 0058 D5), and so does every
task that `WarmPolicy.DedicatedReason` sends to a dedicated pod today.

### 3. The code bundle

The bundle is the code layer of ADR 0077 decision 6, **uncompressed**: the
same deterministic tar (sorted entries, root-owned, dated 1970-01-01,
normalized modes, filtered by `.dockerignore`, no whiteouts). Its digest is
the SHA-256 of those bytes, which is the layer's `diff_id` in the config of
the version's DAG image. So the two paths run the same bytes: a dedicated pod
runs the DAG image, whose last layer has that `diff_id`, and a warm worker
runs the dependency image plus the bundle with that digest. Anyone can check
the relation from the image config without running anything.

The CLI uploads the bundle with the register. The server:

- checks its digest and its size against
  `execution.warm_code_bundle_max_bytes` (a version over the cap is
  registered and keyed by version, and the register response says why);
- checks its shape: every entry is a regular file or a directory under the
  code roots the version declares, with no absolute path, no `..`, no link,
  no device and no whiteout;
- stores it once per `(tenant_id, digest)` in Postgres, referenced by the
  versions that carry it. A bundle no version references is deleted with the
  last one. Bundles are never shared across tenants, so the store is not an
  oracle for what another tenant deployed.

The server never fetches the bundle or anything else from the registry. A
reference to an image is author input, and the control plane makes no
network request on author input (ADR 0048). The bytes the server stores are
data it serves back; it never unpacks or imports them.

### 4. How a worker runs an attempt of a version

- **Registration.** A worker of an environment pool registers with the pool
  key in place of a version, and its bootstrap credential is minted for that
  key, its tenant and its DAG. The check in `registerFromStream` is kept: a
  worker cannot register for a pool its token was not minted for. A worker
  that does not report bundle support is refused and counted (decision 6).
- **Placement.** Dispatch computes the attempt's pool key from its version
  and offers the attempt to a free worker of that key. The registry checks
  that the worker's tenant and DAG are the attempt's before it hands the
  assignment over; a mismatch is refused, not reclaimed, and logged as a
  bug. The `WorkAssignment` names the attempt's version and its bundle digest.
- **Code before ack.** Before it acks the assignment, the worker fetches the
  bundle on the attempt's token (a new streaming RPC on the work client,
  answering only the bundle of the version the token was minted for), hashes
  it while it reads, and compares the result with the digest in the
  assignment. It keeps the last few verified bundles in an agent-owned
  directory, bounded in bytes, and hashes a cached bundle again before every
  use, since an attempt runs as the same user and could have changed it.
- **Code root.** An environment pod mounts an `emptyDir` at each code root of
  the dependency image (the directories the `COPY` lines of the generated
  Dockerfile write into). For each attempt, after the sweep, the agent empties
  the code roots and extracts the verified bundle there, so the child sees the
  same files at the same paths as in the DAG image. Nothing of the previous
  attempt's code, or of anything an attempt wrote there, survives into the
  next attempt.
- **Everything else** is ADR 0058 as built: the ack, the per-attempt token,
  `GetTaskSpec`, the fresh child, the durable outcome, the `warm_worker_id`
  binding, the sweeps, `SlotFree`.

The worker never imports code it has not verified against the digest in the
assignment, and it never runs an attempt whose bundle it could not fetch.

### 5. Targets, floors and drain

ADR 0082 is applied to pools. A pool is a target when at least one version
that maps to it is a target under ADR 0082 D1, plus one rule this ADR adds:
an active run of a superseded version whose key equals the current version's
key uses that pool, so its attempts stay warm. The pool's idle floor is the
largest floor of its target versions (the current version's prewarm floor,
the floors of versions with an active run), its ceiling is `max_pool_size`
per pool, and the per-tenant cap of ADR 0082 D7 counts pools, ordered by DAG
id and then by pool key.

So a deploy that keeps the environment moves `dags.current_version_id` and
nothing else happens to the pool: no prewarm, no drain. A deploy that
changes the environment (a dependency, the base image, a system package)
starts a new pool and drains the old one, exactly as ADR 0082 D4 does
today. A change to the runtime settings changes the key of every environment
pool, so every one of them drains and restarts, within the per-tenant cap.

The GC anchor, the pod label that groups the pool and the reconciler's
grouping move from the version to the pool key: `leoflow.io/warm-pool`, the
first 32 hex digits of the key, next to the warm labels the reconciler reads
today. A pool keyed by version keeps its version as its key, so one code
path serves both kinds.

### 6. Failure modes

- **Bundle fetch fails** (the control plane is unreachable, the stream
  breaks, the bundle is gone because its version was deleted). The worker
  acks the assignment with `started=false` instead of starting it. The
  registry's reclaim path re-places the attempt (ADR 0058 N1d-c,
  `RequeueForRedispatch`) without charging `try_number` or `infra_attempts`, and
  the next placement may take a dedicated pod, which runs the full DAG image.
  A worker that fails three fetches in a row exits and is replaced.
- **Digest mismatch.** The worker acks with `started=false`, the attempt is
  re-placed, and the worker exits rather than serve another attempt
  (recycle on suspicion, ADR 0058 D10). This is counted on its own, because
  it means the store, the transport or the cache was altered.
- **A bundle that does not fit the lease.** The fetch happens before the
  ack, inside the 30 second lease (`warmLeaseSeconds`). A bundle too large to
  fetch in time is a reclaim, which the size cap is there to prevent; the
  implementation sets the default cap from the fetch time it measures.
- **An environment image without bundle support.** The CLI does not send a
  bundle for it (decision 2). If a worker of an environment pool still
  registers without reporting support, it is refused and counted, and the
  versions of that pool are keyed by version until the leader restarts, so
  the reconciler does not create pods that can never register.
- **The dependency image cannot be pulled.** Every version of the pool loses
  its warm workers at once, where today one version would. The existing
  create and pull errors of the reconciler report it, and dispatch falls
  back to dedicated pods, which pull the DAG image.
- **Leader failover.** The key is computed from the database and the
  configuration, and the pods carry it as a label, so a new leader rebuilds
  the same pools. No in-memory state is needed beyond the fallback of the
  previous item, which a new leader learns again.
- **A rollback of the server** to a release that keys by version. The old
  server refuses an environment worker's registration, which carries no
  version, so the worker exits; the old reconciler does not keep a pod with
  no version among its targets. The implementation PR proves both against
  the previous release before the setting can be turned on.

### 7. Migration from per-version pools

- `execution.warm_pool_key` takes `version` (the default in the release that
  ships this) or `environment`. With `version`, nothing changes: no bundle is
  read, no new label is written, and the reconciler's pools are the ones of
  ADR 0082.
- Register stores the environment record and the bundle whatever the
  setting, as long as warm pools are on, so that turning the setting on later
  finds the versions already registered keyed by environment. A version
  registered before the release keeps its version key until the DAG is
  deployed again.
- Turning the setting on changes the targets at the next reconcile: the
  version pools of eligible versions are no longer targets and drain under
  ADR 0082 D4 (idle workers deleted, busy workers finish their attempt), and
  the environment pools start. Turning it off does the reverse. Neither
  direction kills an attempt.
- The default may become `environment` in a later release, after the
  measurements of the consequences below exist; that change is a release
  note, not a new ADR.
- The register response and the deploy report say which key a version got
  and why, and whether its pool is `shared with the previous version` or new,
  next to the per-task mode they already report (#1849).

### 8. Observability

- `dexaflow_scheduler_decisions_total`, with new decision types:
  `warm_pool_version_joined` (a newly current version maps to a pool that
  already exists, so nothing is prewarmed or drained),
  `warm_code_bundle_fetch_failed`, `warm_code_bundle_digest_mismatch`,
  `warm_worker_refused_no_code_bundle` and `warm_pool_key_fallback_version`
  (an eligible version kept on a version key, with the reason logged).
- `dexaflow_warm_attempt_start_seconds`, a histogram of the time from the
  assignment to the attempt's `RUNNING` report, labeled by key kind
  (`environment`, `version`). It includes the fetch and the extraction, so the
  cost this ADR adds is visible next to the cold start it saves.
- `dexaflow_warm_code_bundle_served_bytes_total`, the bundle bytes the
  control plane served, and a gauge of live pools by key kind. No metric is
  labeled by DAG, version or pool key.
- The agent logs the fetch, the cache hit or miss and the extraction time on
  each attempt's log, so one slow attempt can be explained.

## Alternatives considered

- **Keep the version key and make deploys cheaper.** ADR 0082 already moved
  the image pull from the first run to the deploy, and ADR 0077 made the
  build cheap. The pool still starts from zero, the pull still happens on
  every node, and superseded runs still go dedicated.
- **Key by the tag of the dependency image.** A `deps-*` tag is predictable
  and can be pushed again where tags are mutable (ADR 0077, consequences).
  Two versions with the same tag and different content would share a pool.
- **Key by the full DAG image digest.** It changes with every code change,
  which is the version key again.
- **Let the control plane fetch the code layer from the registry.** The
  image reference is author input; fetching it from the control plane is the
  request ADR 0048 forbids, with the control plane's network position and
  credentials.
- **Let the worker pull the code layer from the registry.** The agent would
  need registry credentials in a pod that runs author code, and the pull
  secrets of a namespace are the operator's, possibly shared by tenants.
- **Deliver the code in a ConfigMap, an image volume or a shared volume.** A
  ConfigMap holds at most 1 MiB (ADR 0067 found 128 KiB usable through
  annotations). Volumes of a pod cannot change after it is created (the
  reason for ADR 0058 D5), so an image volume or a per-version mount needs a
  new pod. A shared filesystem of DAG code is what principle 7 rules out.
- **Put the bundle in an object store.** It would need a store in every
  installation; the task-log sink of ADR 0056 is optional. Postgres already
  holds every version's spec, and the size cap bounds what it holds. A
  bundle store behind an interface keeps an object store open for later.
- **Share pools across the DAGs of a tenant.** Two DAGs on the same
  dependency image would share workers. Their secret scopes differ (ADR
  0055), and the correction note of ADR 0058 D4 records that state written
  outside the agent's control can be seen by a later attempt. The read-only
  root closes most of that, but not all of it is proven, and ADR 0053 and
  0058 forbid cross-DAG reuse. A later ADR can revisit it with the
  measurements of this one.
- **Extract the bundle once per worker instead of once per attempt.** An
  attempt runs as the agent's user and could change the extracted files for
  the next one. Extracting per attempt from verified bytes costs time, which
  the start histogram measures; a read-only code mount owned by another user
  can replace it if the numbers ask for it.

## Consequences

- **A code-only deploy keeps the pool.** With the setting on, the first run
  of a new version starts on a warm worker without an image pull, which is
  the acceptance of #1707, and runs of the previous version stay warm while
  they finish.
- **Each warm attempt pays a fetch and an extraction.** The fetch is skipped
  when the bundle is cached; the extraction is not. The cost grows with the
  bundle (a dbt project with its `target/` is the large case) and is measured
  by `dexaflow_warm_attempt_start_seconds` before the default changes.
- **Fewer pods for the same work.** Versions that share an environment share
  `max_pool_size` and one idle floor, so a DAG that deploys often holds one
  pool instead of one per version with an active run.
- **The database holds code.** Bundles take space in Postgres, bounded by
  the size cap and by the versions that reference them; backups grow with
  them. Who can read a bundle is who can read the version's source today
  (`GET /api/v2/dagSources/{dag_id}`), plus the version's own attempts.
- **The environment record is trusted as the image reference is.** The
  deploy identity that registers a version decides its image today (ADR
  0076); it now also decides its bundle. The `diff_id` relation of decision
  3 lets an audit find a version whose bundle and image disagree; the server
  does not check it, since it does not read the registry.
- **A schema migration, a proto change and a new setting.** A table for
  bundles and a column for the environment record on `dag_versions`, both
  added without a rewrite; the pool key in `WorkerRegister` and the bundle
  digest in `WorkAssignment`, with the new fetch RPC; `warm_pool_key` and
  `warm_code_bundle_max_bytes`. The runtime image gains bundle support and a
  label saying so. Each lands with its own tests and docs, after this ADR is
  accepted.
- **Docs.** The warm pools page replaces "Pool key: DAG version" with both
  keys and the conditions for each, and stops calling frequent deploys a
  poor fit for versions keyed by environment.
