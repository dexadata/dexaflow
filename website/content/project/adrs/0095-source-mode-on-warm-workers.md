---
title: "ADR 0095: a source-mode version runs on warm workers, which receive its dag.py with each assignment"
linkTitle: "0095 · Source mode on warm workers"
weight: 950
description: "ADR 0095: with execution.source_mode.warm_pools on, a source-mode version gets a warm pool. The control plane sends the version's dag.py and its SHA-256 with each warm assignment, only to a worker that confirmed source delivery when it registered; the worker checks the digest, writes dag.py into its per-attempt scratch, runs the attempt from there and removes it afterwards. Off by default. Amends ADR 0067 section 3 and ADR 0058."
---

**Status:** Accepted
**Date:** 2026-10-26
**Amends:** ADR 0067 section 3 (Pro source mode, warm workers), ADR 0058 (warm worker pools, the assignment protocol)
**Relates:** ADR 0068 (new settings default to off), ADR 0089 (warm pools keyed by environment)
**Issues:** #2159

## Context

In source mode (ADR 0067 section 3) a version on the operator's runtime image
carries its `dag.py` as its source, and the Kubernetes executor ships it into
a dedicated pod through a pod annotation projected as `dag.py`. A warm worker
(ADR 0058) starts before any attempt is assigned to it, so its pod cannot
carry that annotation. ADR 0067 therefore kept every source-mode attempt on a
cold pod: `WarmPolicy.VersionReason` returns `source mode`, and the register's
execution report says `no warm pool for this version: source mode`.

Source mode is the no-build path for small DAGs, where pod startup is most of
the run time, so it is the path that gains most from warm workers and the one
that could not use them.

A warm pool already belongs to one DAG version, the one the worker names in
`WorkerRegister`, and the control plane already holds the version's source
and talks to the worker over the authenticated `AwaitAssignment` stream.

## Decision

1. **Setting.** `execution.source_mode.warm_pools` (Helm
   `execution.sourceMode.warmPools`, environment
   `DEXAFLOW_EXECUTION_SOURCE_MODE_WARM_POOLS`), default `false` as ADR 0068
   asks of a new setting. Off, nothing changes. On, with source mode and
   warm pools on, `VersionReason` no longer returns `source mode` and the warm
   pool reconciler keeps targets for source-mode versions; every other reason
   a task takes a dedicated pod still applies.
2. **Delivery per assignment.** `WorkAssignment` gains `dag_source` and
   `dag_source_sha256`. Dispatch sets both on a source-mode attempt it offers
   to a warm worker, from the source it already resolved for a dedicated pod.
   Delivering once at registration was considered: per assignment keeps the
   worker stateless across attempts, and the text is at most
   `MaxSourceModeBytes` (128 KiB), the same cap register and dispatch apply,
   so both paths accept the same versions.
3. **Capability, not version.** `WorkerRegister` gains `source_delivery`. An
   agent from 0.5.7 sets it when its command runner can set the task's working
   directory, which the production runner always can. The registry hands an
   assignment that carries a source only to a free worker that set it. A
   worker on an older agent, which would ignore the unknown field and run
   without `dag.py`, is passed over: the attempt takes a dedicated pod, and
   the registry logs a warning and counts
   `dexaflow_scheduler_decisions_total{decision_type="warm_worker_no_source_delivery"}`
   once per worker. Warm placement keeps degrading to a dedicated pod and
   never strands an attempt.
4. **On the worker.** After the scratch reset and before the ack, the worker
   checks `dag_source` against `dag_source_sha256`, writes it as `dag.py` into
   `<scratch>/source` and runs the attempt with that directory as its working
   directory, as a dedicated source-mode pod runs from its projected file.
   A digest that does not match is refused: the worker acks
   `started=false`, so the control plane reclaims and places the attempt
   again, then signals its slot free and keeps serving. Any other failure to
   stage the file (a full scratch) ends the worker, as a failed scratch reset
   does, so the pod is replaced and the attempt reclaimed rather than refused
   and handed back to the same worker. The file is removed
   after the attempt, with the rest of the scratch when the worker sweeps it
   (`LEOFLOW_WARM_ATTEMPT_HOME`), on its own otherwise, and the next attempt
   starts from an empty scratch in any case.

## Consequences

- Source-mode versions can use warm pools once the runtime image carries an
  agent from 0.5.7 or later. The operator rebuilds it, updates
  `execution.source_mode.image` to the new digest and turns the setting on.
- On a warm worker the source travels over the authenticated assignment
  stream and is never put in a pod annotation, so it does not reach pod
  readers, the apiserver audit log or a log shipper that copies annotations,
  and nothing with `patch pods` can change it under a running attempt. The
  SHA-256 only detects a copy damaged on the way: the control plane computes
  it from the stored source at dispatch, the same text a dedicated pod gets,
  so it does not protect the stored source itself. Pinning a digest at
  register time and checking it at dispatch would, for both paths, and is
  left to a later change. `dag_source` is `bytes`, so no text can fail to
  encode on the stream.
- The working directory of a warm source-mode attempt is a writable
  directory under the worker's scratch, unlike the read-only
  `/opt/leoflow/source` of a dedicated pod, and is wiped after the attempt.
- The agent comes with the runtime image, so all warm workers of a version
  run the same agent, and a recycled worker comes back the same. A pool of a
  version on an image older than 0.5.7 therefore never serves its version
  while it keeps its idle workers, which count against the tenant's warm pod
  cap. The warning and the counter name it, and the operate docs say to
  redeploy such versions on the new image before turning the setting on.
  Keeping the reconciler from creating such pools would need it to learn
  from the registry which versions' workers lack source delivery; it is left
  out until that cost shows.
