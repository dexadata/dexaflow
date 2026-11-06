---
title: "ADR 0063: An operator executor policy forces pod fields on every task pod"
linkTitle: "0063 · Operator executor policy"
weight: 630
description: "ADR 0063: an optional executor.policy, set by the operator, forces the runtime class, service account, node selector and tolerations of every task pod. It runs as the last admission step of ADR 0100, so neither a DAG nor a hook can undo it. Refusing what a DAG may set stays with the execution allowlist."
---

**Status:** Accepted
**Date:** 2026-10-04 (proposed in #1365), rewritten and accepted 2026-11-06
**Relates:** ADR 0023 (L0 platform defaults), ADR 0054 (coexistence in a shared cluster), ADR 0058 (warm worker pools), ADR 0066 (resource unit), ADR 0100 (dispatch admission hook, §7)
**Issues:** #1372, #1365

## Context

A DAG decides most of its task pod: from `execution` in the spec it sets the
service account, the runtime class, the node selector, tolerations, affinity,
the priority class, labels and annotations. On a cluster shared by teams that
do not trust each other each of those fields reaches beyond the DAG.

The first draft of this record (#1365, October 2026) proposed one policy with
two kinds of rules: restrict rules that refuse a task (allowed service
accounts, priority classes, images, label prefixes, a resource ceiling, no DAG
placement at all) and force rules that set a field whatever the DAG says. While
it waited, most of the restrict half shipped another way:

- the execution allowlist (#629, #537): which `execution` fields a DAG may set
  at all, which priority classes, which service accounts and which images,
  checked at registration and again at dispatch;
- the resource unit and the default limits (ADR 0066, #537), which bound and
  fill a task's resources;
- the dispatch admission hook (ADR 0100), where an operator's own code can
  refuse or change an attempt for any reason.

What no configuration can do yet is the force half: run every task under a
sandboxed runtime class, as one service account, or on one node pool, without
writing and running an admission webhook for it. ADR 0100 §7 already decided
where that belongs: the executor policy is the last step of the admission
chain, so its forced fields apply to the result of every hook.

## Decision

1. **Force rules only.** `executor.policy.force` sets:
   - `runtime_class_name`, replacing the task's;
   - `service_account`, replacing the task's or the default;
   - `node_selector`, merged over the task's, the policy's keys winning;
   - `tolerations`, appended to the task's.

   Restricting what a DAG may set stays with the execution allowlist. The
   restrict rules of the first draft are not added: each one now has a home
   (the allowlist, the resource unit, a hook), and a second place that refuses
   the same field would give an operator two lists to keep in step. Label and
   annotation prefixes, the one restrict rule with no home yet, are left to a
   hook until someone needs them in configuration.

2. **Where it runs.** After the admission hooks (ADR 0100 §7) and before the
   agent token is minted. It never refuses, so it adds no failure mode at
   dispatch. What it forces is not the author's choice: the executor's
   allowlist re-check reads the task's own execution settings, as it does for
   a hook's changes, so `fields: none` still lets the policy set the runtime
   class.

3. **Configuration.** The policy is read verbatim from the server's YAML or
   JSON config file (`DEXAFLOW_CONFIG`), the route the OIDC maps take: node
   label keys carry dots that viper would split, and tolerations cannot travel
   as environment variables. Boot fails on an unknown key (so a restrict rule
   of the first draft is refused, not ignored), on a value Kubernetes would
   reject, on a reserved `leoflow.io/` or `dexaflow.io/` node selector key, on a
   policy in a config file of another format, and on a forced service account
   outside `execution_allowlist.service_accounts` when that list is set (the
   default task ServiceAccount is always allowed). A chart value follows in its
   own change.

4. **Warm pools.** Warm pods are built by the warm pool reconciler from the
   version's image, not at dispatch, so they would not carry what the policy
   forces. Boot refuses a policy together with `execution.warm_pools_enabled`.
   A forced field also makes `WarmPolicy.DedicatedReason` send the attempt to a
   dedicated pod, so no attempt reaches a warm worker even if the two ever meet.
   Building warm pods with the forced fields, which would lift the refusal, is
   future work.

5. **Lite** runs no pod. It ignores the policy and logs a warning at boot.

## Consequences

- An operator gets a sandboxed runtime, one identity or one node pool for every
  task from configuration alone. Without a policy, dispatch is unchanged.
- The cost is a copy of the task's execution settings per attempt when a policy
  is set, and nothing when it is not.
- The policy and warm pools exclude each other until warm pods carry the
  forced fields.

## Alternatives considered

- **The first draft as proposed** (#1365, #1372): a policy with its own
  disposition path and restrict rules. ADR 0100 replaced the path, and the
  allowlist the rules.
- **A webhook only.** It works today, but it is a service to write, run and
  keep available on the dispatch path for what is a fixed setting.
- **Kubernetes admission** (a mutating policy). It runs after the token is
  minted and the pod is built, it does nothing on the Dexaflow side of a
  refusal, and it cannot tell the scheduler that an attempt will land on a
  node pool it chose.
