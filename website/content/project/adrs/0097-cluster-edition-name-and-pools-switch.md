---
title: "ADR 0097: the Helm edition is called cluster, and named pools have their own switch"
linkTitle: "0097 · Cluster edition and the pools switch"
weight: 970
description: "ADR 0097: the edition ADR 0027 calls Pro is renamed cluster, with pro kept as an alias the server still reads. Named pools stop being gated on ui.edition: scheduler.pools.enabled turns them on or off, and unset it follows the edition, so an install that does not set it behaves as before. OIDC and the TLS boot guards still key on the cluster edition. Follows ADR 0027."
---

**Status:** Accepted
**Date:** 2026-10-28
**Follows:** ADR 0027 (product editions, executors and delivery), ADR 0028 (one codebase, one license)
**Relates:** ADR 0053 (admission and placement, named pools), ADR 0068 (patch safety bar)
**Issues:** #2173

## Context

ADR 0027 defines two editions by how the server is delivered: the Helm chart on
Kubernetes, which it calls Pro, and the installable binary, Lite. ADR 0028
makes both the same commit and the same license. The name Pro reads as a paid
tier to most people, which it is not: both editions are the open source engine.

`ui.edition` also does more than its name says. Besides the badge in the UI
shell it turns on named pools (the scheduler's cross-DAG slot gate and the
pools API), allows `auth.provider: oidc`, and selects the TLS boot guards of
the agent channel. An operator who wants pools on a non-Helm install, or a
Helm install without them, has to claim an edition to get it, and a
scheduling feature hangs off a `ui.*` key.

## Decision

1. **The Helm edition is called cluster.** `ui.edition: cluster` is its
   name in the configuration, the docs, the CLI and the UI badge. `pro` stays
   accepted as an alias and means exactly the same (`config.IsClusterEdition`),
   so a values file or a chart that sets it keeps working. No production code
   compares against `"pro"` outside that alias.

2. **The chart keeps writing `pro` for one more patch.** A rollback of the
   chart to an earlier image must not meet a value that image refuses, so the
   chart switches to `cluster`, and the server starts warning about `pro`, in
   a later release, once every supported image reads `cluster`.

3. **Pools have their own switch.** `scheduler.pools.enabled` turns named
   pools on or off: both the scheduler's gate and the pools API read it
   (`ServerConfig.PoolsEnabled`). The key has no built-in value; unset, it
   follows the edition, on for `cluster` and off otherwise. That keeps
   ADR 0068's bar: an install that does not set the key behaves byte for byte
   as before. A value that is not a boolean fails boot.

4. **OIDC and TLS stay on the edition.** `auth.provider: oidc` still requires
   the cluster edition, because the TLS boot guards that keep its tokens and
   cookies off plaintext channels key on it too. Giving OIDC its own
   validation is a change of what a Lite install may do, and is left to its
   own decision.

## Consequences

- The configuration reference documents `scheduler.pools.enabled` with its
  edition-dependent default, and `ui.edition` points to it.
- `server.pools_read_only` and `scheduler.pool_starvation_threshold` apply
  while pools are on, whatever the edition; with pools off the server logs
  that `server.pools_read_only` has no effect, as it did on Lite.
- The page slugs of the renamed docs keep aliases, so links to the earlier
  names still resolve.
- The badge stays a UI concern: it reads the edition but no longer owns a
  scheduling feature.
