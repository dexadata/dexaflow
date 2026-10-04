---
title: "ADR 0063: Operator executor policy for task pods"
linkTitle: "0063 · Operator executor policy for task pods"
weight: 630
description: "ADR 0063: an optional, operator-owned policy that forces or restricts the pod fields a DAG may set today (runtime class, service account, placement, metadata, resources, image), checked at registration and enforced at dispatch and on warm pods."
---

**Status:** Proposed
**Date:** 2026-10-04
**Relates:** ADR 0023 (L0 platform defaults), ADR 0054 (coexistence in a shared cluster), ADR 0058 (warm worker pools). This ADR adds the operator side that ADR 0054 §3 anticipates for the placement passthrough ("an operator allowlist mirroring `taskPodSecurity`") and extends it to every pod field a DAG controls.

## Context

A DAG decides most of its task pod. From `execution` in the spec
(`internal/domain/dag.go`, `Execution`) and the DAG's `image` and `resources`,
`dispatch.Dispatcher` builds an `executor.Request` and `executor.BuildPod`
applies it verbatim:

| DAG field | Pod field | What an author can do with it |
|---|---|---|
| `execution.service_account` | `serviceAccountName` | run as any ServiceAccount in the task namespace, including the keyless cloud identity bound to another team's SA |
| `execution.runtime_class_name` | `runtimeClassName` | pick the default runtime when the operator meant every task to run sandboxed |
| `execution.node_selector`, `tolerations`, `affinity`, `topology_spread_constraints` | placement | land on any node pool, including one tainted for another workload |
| `execution.priority_class_name` | `priorityClassName` | outrank, and preempt, the cluster's online services |
| `execution.labels`, `execution.annotations` | pod metadata | set annotations that other controllers act on (sidecar injection, autoscaler eviction, cloud identity) |
| `resources` | requests and limits | ask for any amount, bounded only by a namespace `LimitRange` if the platform installed one |
| `image` | container image | run any image from any registry the nodes can pull |

The operator, by contrast, controls only pod security (`PlatformDefaults.PodSecurity`,
deliberately kept out of the DAG spec so an author cannot elevate their own task)
and the default ServiceAccount used when a DAG names none.

That split was acceptable while one team wrote every DAG. It is not on an engine
shared by tenants that do not trust each other, which is what 0.5.x supports: a
DAG author is a tenant, and every row above is a way for one tenant to reach
another tenant's identity, nodes or budget, or to step outside the isolation the
operator built.

Warm pods (ADR 0058) already take service account and placement from the
operator, not the DAG, but they run the DAG version's image, so they need the
same image rule and any forced runtime class.

## Decision

### 1. One optional policy, off by default

The engine gains an `executor.policy` section. When it is absent, nothing
changes: a DAG's pod is byte-identical to today. The policy is engine-wide in
this ADR; a per-tenant overlay is an open item (§6).

Because the policy carries maps and lists of structured values (tolerations,
node selectors), it cannot travel as env vars. It is decoded from the server
config file named by `LEOFLOW_CONFIG`, the route `auth.oidc.role_mappings`
already takes. The chart's partial config file (`oidc-config.yaml`, today
rendered only for OIDC) carries `executor.policy` too and is rendered when
either is set; env still ranks above it, so it cannot override anything else.
An invalid policy fails startup.

### 2. Two kinds of rule: force and restrict

A **force** rule sets a pod field to the operator's value regardless of the DAG.
A **restrict** rule leaves the DAG's value alone if it is allowed and refuses the
task if it is not. Restrict rules never clamp: a memory limit silently halved
becomes an OOM kill the author cannot explain, and an annotation silently dropped
becomes a sidecar that does not appear. Refusing names the rule.

```yaml
executor:
  policy:
    runtime_class_name: gvisor            # force; also on warm pods
    service_account:
      force: ""                           # force this SA, or
      allowed: [etl-reader, etl-writer]   # restrict to these
    placement:
      node_selector: {pool: tasks}        # force; policy keys win a collision
      tolerations:                        # force; appended to the DAG's
        - {key: pool, operator: Equal, value: tasks, effect: NoSchedule}
      allow_dag_placement: false          # refuse DAG node_selector, tolerations,
                                          # affinity, topology spread
      allowed_priority_classes: [batch-low]
    metadata:
      allowed_label_prefixes: [team.example.com/]
      allowed_annotation_prefixes: []     # empty list: no DAG annotations
    resources:
      max: {cpu: "4", memory: 16Gi, ephemeral_storage: 20Gi}
    images:
      allowed: [registry.example.com/dags/]   # prefix match on the image reference
```

Keys are snake_case like the rest of the server config. Every key is optional;
an unset rule does nothing, and an empty list allows nothing. Resource ceilings
are checked after the L0 defaults (ADR 0023) are applied, so a task that
declares nothing is held to the ceiling through the default; a limit the policy
fills in comes with an explicit zero request, because Kubernetes would
otherwise default the request to the whole ceiling. The metadata rule covers
the DAG's own keys only; the executor's own labels and annotations are not
checked against it.

### 3. Where it is checked

1. **Registration (authoring feedback).** Registering a DAG version
   (`POST /api/v2/dags/{dag_id}/versions`, `internal/api/versions.go`) runs the restrict
   rules against every task and answers `422` naming the task, the field and the
   rule. The author learns at deploy time, not at 3 a.m.
2. **Dispatch (the guarantee).** `dispatch.Dispatcher` applies the policy to the
   `executor.Request` after L0 defaults and before `Execute`: force rules
   mutate it, restrict rules refuse it. A refusal is a permanent outcome, so it
   fails the task at once with a note naming the rule, instead of going through
   the bounded dispatch retries meant for transient errors (ADR 0031
   Amendment A). This check exists because the policy can change after a version
   was registered, and versions registered before the policy existed are still
   dispatched.
3. **Warm pods.** `executor.BuildWarmPod` applies the force rules (runtime
   class, placement). A warm pool is not created for a version whose image the
   policy refuses. A task whose own execution fields would be refused never
   reaches a warm worker, because it is refused at dispatch first.

Lite runs no pods and ignores the policy; the server logs that at startup if a
policy is configured on a Lite edition.

### 4. Relationship to Kubernetes admission

ADR 0054 §1 says that where Kubernetes already solves a problem, it is a
platform object, not an engine feature. Admission control (Pod Security
Admission, `ValidatingAdmissionPolicy`, or a policy engine) can refuse most of
the pods above, and the documentation will keep recommending it as the
platform-owned backstop. The engine policy is still needed, for three reasons
that admission cannot cover:

- **Forcing is mutation.** Setting the runtime class or the node pool on every
  task pod needs a mutating webhook or a mutating admission policy, which many
  clusters do not run.
- **Failure shape.** A pod refused by admission surfaces as an apiserver error
  at dispatch, retried and then reported as `dispatch_failed`. The engine can
  refuse at registration with the field and the rule.
- **Tenant awareness.** Only the engine knows which tenant a task belongs to,
  which the per-tenant overlay in §6 needs.

### 5. What does not change

`PlatformDefaults.PodSecurity` stays where it is; the policy does not duplicate
it. The default task ServiceAccount stays the default; `serviceAccount.force`
overrides it, `serviceAccount.allowed` is checked against the resolved value
(the DAG's or the default). The warm path's existing SA rule (a task pinning a
different SA takes the dedicated path) is unchanged.

### 6. Open items

- **Per-tenant overlay.** A `tenants: {<name>: {...}}` map merged over the
  engine-wide policy, so each tenant can have its own service accounts and node
  pool. Deferred until the engine-wide policy has shipped.
- **Image digests.** Requiring `@sha256:` references is a natural extra
  restrict rule; left out until someone needs it.
- **Fields not covered yet.** Dynamic Resource Allocation claims
  (`execution.resource_claims`), which bypass the resource ceiling, and the
  termination grace period.
- **Executor-owned metadata.** A DAG can today set labels and annotations under
  the executor's own `leoflow.io/` prefix on its pod. That is not a policy
  question: the executor should refuse or drop those keys for every DAG, and
  is fixed separately.

## Consequences

- An operator can run untrusted tenants' DAGs without granting them the choice
  of identity, runtime, node pool, priority or registry.
- A DAG that worked yesterday can be refused today when the operator turns on a
  rule. Registration-time `422`s and the dispatch note name the rule, and the
  policy is off until the operator writes one.
- The chart's partial config file is rendered when `executor.policy` is set,
  not only for OIDC.
- Dispatch gains a permanent-refusal outcome distinct from `Rejected` (which
  retries), which the scheduler maps to a failed task with a note.

## Alternatives considered

- **Admission only.** Rejected as the sole mechanism for the reasons in §4;
  kept as the recommended backstop.
- **One flag per field in the flat config** (`executor.force_runtime_class`,
  ...). Rejected: placement and tolerations do not fit flat env-bound keys,
  and a dozen unrelated flags hide that they form one policy.
- **Clamping instead of refusing.** Rejected in §2.

## Implementation plan

1. Policy type, file loading, validation and dispatch enforcement, with the
   permanent-refusal outcome. Until step 3 lands, the server refuses to start
   with both a policy and warm pools, because warm pods would run outside it.
2. Registration-time check (`422`).
3. Warm pod force rules and the image check before pool creation.
4. Chart values and config file, configuration reference and an operator guide
   that pairs the policy with an admission backstop.
