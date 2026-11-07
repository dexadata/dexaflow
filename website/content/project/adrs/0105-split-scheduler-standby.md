---
title: "ADR 0105: The split scheduler runs a leader and a standby"
linkTitle: "0105 · Split scheduler standby"
weight: 1050
description: "ADR 0105: in split mode the scheduler Deployment may run two replicas, a leader and a standby on the same advisory lock, with a PodDisruptionBudget of its own, so the restricted API identity of ADR 0049 no longer costs dispatch failover."
---

**Status:** Proposed
**Date:** 2026-11-06
**Amends:** ADR 0049 (split api and scheduler roles), which pinned the scheduler Deployment to one replica
**Relates:** ADR 0009 (leader election), ADR 0031 (the database as the source of truth)
**Issues:** #2316

## Context

ADR 0049 split the control plane into an `api` Deployment with a restricted
identity and a `scheduler` Deployment that keeps the identity which creates task
pods. The chart pinned the scheduler to one replica, whatever
`split.scheduler.replicaCount` said. Every eviction of that pod (a node drain, a
cluster upgrade, consolidation) was the full restart window: image pull, boot,
schema check, leadership. Non-split `replicaCount: 2` already fails dispatch
over in seconds, so an operator had to choose between the restricted API
identity and scheduler failover. A production profile for regulated users needs
both.

Nothing in the scheduler role requires a single process. The scheduler loop,
the reapers and the dispatch path run only while a process holds the leader
advisory lock (ADR 0009), whatever its role. A non-split follower already serves
the agent gRPC endpoint behind the same Service as the leader:

- the unary calls (state reports, heartbeats, log lines, XCom) write to the
  database, so they are correct on any replica (ADR 0031);
- `AwaitAssignment`, the warm worker stream, is refused by a follower, so the
  worker reconnects until it reaches the leader, whose placer reads the registry
  that stream registers into.

The pin was a chart decision taken before that follower behavior was proven,
not a property of the role.

## Decision

1. **`split.scheduler.replicaCount` is honored, with `1` or `2`.** `1` stays
   the default, so an upgrade changes nothing. `2` runs a leader and a standby
   waiting on the same lock, exactly as non-split `replicaCount: 2` does. Any
   other value fails the render: only one replica leads, so a third adds a pod
   and no failover, and charts before 0.5.11 ignored the value, so a leftover
   number must not start that many schedulers on an upgrade.
2. **The agent gRPC endpoint stays on the scheduler Service**, which routes to
   both replicas. No change to the agents or the API role: the follower
   behavior above is what makes this safe.
3. **The scheduler gets its own PodDisruptionBudget when it runs two
   replicas**, with the same `podDisruptionBudget` settings as the api budget,
   so a drain evicts the leader only while the standby is up. It never renders
   over one replica (a budget over one pod blocks every drain), even with
   `podDisruptionBudget.enabled: true`; an explicit `false` turns both budgets
   off.
4. **Spread and drain follow the HA profile.** `topologySpreadConstraints`
   already render per Deployment with that Deployment's selector, and
   `deployment.preStopSleepSeconds` already renders on both split Deployments;
   with a standby the sleep now buys the agent streams a second endpoint.
5. **Both scheduler replicas mount the logs volume**, so the single-writer
   refusal counts them: `2` needs object storage or a ReadWriteMany logs PVC,
   as every multi-replica topology does.
6. **The leader kill scenario runs in CI.** `test/e2e/split-two-process.sh`
   starts a standby scheduler process, checks that it does not tick while the
   leader holds the lock, kills the leader with `SIGKILL`, and requires the
   standby to take the lock and run a new run to success.

## Consequences

- Split mode with `split.scheduler.replicaCount: 2` gives the restricted API
  identity and dispatch failover together. The failover costs what it costs in
  non-split mode: the standby takes the lock within seconds, and the new leader
  holds the reapers for one settling grace (180 s by default) before it may
  declare a task lost.
- A rolling upgrade of a two-replica scheduler moves the lock twice, as in
  non-split mode, and opens two settling windows back to back.
- With `scheduler.eager_promotion` on, a terminal report that lands on the
  standby wakes the standby, which runs an empty tick; the leader promotes the
  downstream task on its next regular tick. This is the same behavior as a
  non-split follower and costs at most one tick interval.
- The standby costs one more pod with the scheduler's requests, and it holds
  the privileged identity of the scheduler role, so it falls under the same
  RBAC and NetworkPolicy as the leader.

## Alternatives considered

- **Serve the agent gRPC endpoint from the api role.** It would remove the
  follower routing question but give the restricted identity a stream that
  carries task secrets, which is what ADR 0049 moved away from.
- **Route the gRPC Service to the leader only** (a readiness gate on
  leadership). A follower would then be unready, so a drain could not tell a
  healthy standby from a broken one, and the PodDisruptionBudget would count
  the standby as unavailable. The follower behavior already makes routing to
  both correct.
- **Allow any replica count.** Rejected for the reasons in decision 1.
