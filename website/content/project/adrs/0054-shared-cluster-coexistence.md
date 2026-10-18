---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /adr/0054-shared-cluster-coexistence.html
# --- end AUTO redirect aliases ---
title: "ADR 0054: Coexistence in a shared, multi-team Kubernetes cluster"
linkTitle: "0054 · Coexistence in a shared, multi-team Kubernetes cluster"
weight: 540
description: "ADR 0054: Coexistence in a shared, multi-team Kubernetes cluster"
---

**Status:** Accepted
**Date:** 2026-08-17
**Relates:** ADR 0049 (split API/scheduler roles), ADR 0035 (keyless-first cloud auth; Leoflow is not a key manager), ADR 0015 (Kubernetes as the sole execution path), ADR 0002 (pod-per-task, ephemeral, zero idle cost), ADR 0053 (admission + placement — task-level concurrency, the Leoflow-owned half of the ownership line), ADR 0055 (secret scoping + token liveness — closes the B1 the namespace split only shrinks)

## Context

Enterprise adopters do not hand Leoflow its own cluster. They drop it into a
shared, multi-team Kubernetes cluster next to online services, other batch
systems, and other teams' namespaces — all drawing on the *same* finite pools:
node capacity, the kube-apiserver's request budget (API Priority & Fairness),
the CNI, and whatever ResourceQuota the platform team has already carved out.
In that setting Leoflow is a **guest**, and the platform team's first question
is not "what can it do?" but "what will it do to my neighbors?"

Leoflow's cluster footprint today is deliberately minimal, and that is a
load-bearing strength we must not regress: a single namespaced `Role`
(`helm/leoflow/templates/rbac.yaml`), no ClusterRole, no CRD, no admission
webhook, no DaemonSet, no node agent. It creates task pods; the cluster does the
rest. But "minimal footprint" is not the same as "good tenant." A good tenant
also has to **bound** what it consumes and **stay in its lane** under contention,
and there the current shape has real gaps (spelled out in
`spec/shared-cluster-multitenant-spec.md` §2 and §3):

- The dominant apiserver cost Leoflow imposes on neighbors is **unbounded** in
  the number of concurrently-running tasks (§2.2).
- Two of the isolation controls a platform team would reach for are **blocked**
  by contract bugs / missing passthrough in `BuildPod`
  (`internal/executor/kubernetes.go`).
- There is no ADR that says *which* of these coexistence controls Leoflow owns
  and which the platform owns — so the temptation is to grow the chart to manage
  all of them, which is exactly how a minimal guest turns into a cluster-wide
  operator.

This ADR draws that ownership line and ratifies the isolation model from the
spec's §3. It is a *coexistence* decision, not an execution-topology one
(warm-pool / N:1 lives in ADR 0053).

## Decision

### 1. The ownership principle

> **Where Kubernetes already solves it, it is a platform (GitOps) object, NOT
> chart-managed by Leoflow. Where Kubernetes cannot — task-level concurrency,
> secret scope — Leoflow owns it.**

Concretely, the Leoflow chart **must not** manage the controls that are
properties of the *cluster and its other tenants*, not of Leoflow:

- **ResourceQuota** — it bounds a namespace's total consumption, a budget the
  platform team allocates across all tenants. Leoflow does not get to set its own
  allowance.
- **LimitRange** — the same: a namespace default-and-cap policy the platform owns.
- **PriorityClass** — a *cluster-scoped* object whose whole point is to rank
  Leoflow's pods **relative to the neighbors' pods**. A tenant cannot self-assign
  its priority relative to production without the ranking being meaningless;
  worse, creating cluster-scoped objects would require cluster-scoped RBAC, which
  is precisely the footprint ADR 0015's minimalism buys us out of.
- **NetworkPolicy governing the neighbors** — a policy that constrains what *other*
  namespaces may do, or that encodes the cluster's trust zones, belongs to the
  platform. (Leoflow's chart *does* ship a NetworkPolicy for its **own** task and
  control-plane pods — its own lane — which is a different thing; see the ladder.)

What Leoflow owns is what Kubernetes has no concept of: **task-level admission
and concurrency** (`max_active_tasks`, real pools — ADR 0053) and **secret scope**
(which subset of a tenant's vault a given task may resolve — ADR 0055). Those are
Leoflow-domain decisions with no cluster primitive to defer to.

The consequence of the principle is a chart that stays a *workload manifest*, not
a cluster policy bundle. When an operator needs a ResourceQuota, we document the
GitOps object they apply — we do not template it.

### 2. Leoflow provisions no nodes — it is a pure workload

Leoflow **creates Pods and nothing more**. It never provisions, cordons, drains,
labels, taints, or otherwise touches nodes. When a task pod is created, the
cluster's own `kube-scheduler` places it, and the cluster's Cluster
Autoscaler / Karpenter provisions a node if none fits. Leoflow is on neither the
placement nor the provisioning path.

This is verifiable in the RBAC, and the claim is a deliberate invariant:

- The executor `Role` (`helm/leoflow/templates/rbac.yaml`) grants verbs on
  `pods`, `pods/log`, and `persistentvolumeclaims` **only**. It has **no `nodes`
  verbs**, and it is a namespaced `Role`, **not a ClusterRole**.
- There is **no DaemonSet and no node agent** anywhere in the chart. The only
  cluster-side components are the control-plane Deployment(s) and the ephemeral
  task pods.
- `scripts/rbac-covers-executor.sh` checks the grant against the executor's
  actual apiserver calls both ways on every CI run, so a `nodes` call could not
  be added without either a failing build or a conspicuous new grant landing in
  this file.

A **dedicated node pool** for Leoflow is therefore an **operator GitOps choice**,
not a Leoflow feature: the operator taints a node pool
(e.g. `workload=leoflow:NoSchedule`) and Leoflow's task pods opt in via
`tolerations` + `nodeSelector`. This is the strongest form of noisy-neighbor
isolation and it costs Leoflow no new privilege — but it is **blocked today by a
contract bug**: `tolerations` is declared on the execution spec
(`internal/domain/dag.go:167`, carried into `req.Execution`) and then **silently
dropped** by `BuildPod`, which reads only `NodeSelector` and `ServiceAccount`
(`internal/executor/kubernetes.go` — the pod's `Spec.Tolerations` is never set).
So a pod tolerating the taint is never produced, and the taint keeps it off the
dedicated pool entirely. **`nodeSelector` IS applied today** (`BuildPod` sets
`Spec.NodeSelector = req.Execution.NodeSelector`), but a `nodeSelector` alone
cannot land a pod on a *tainted* pool. The taint-based dedicated pool is thus
**gated on PR-1** (apply `tolerations` in `BuildPod`). An untainted, merely
labeled pool works today via `nodeSelector`.

### 3. The isolation ladder (strongest → cheapest)

The following controls compose; an operator adopts as many rungs as their risk
posture requires. Each names its owner and, where it depends on Leoflow work, the
gating PR.

1. **Dedicated node pool** — taint + toleration. Physical isolation: Leoflow's
   noisy neighbors are only *other Leoflow tasks*. Owner: platform GitOps (the
   taint) + Leoflow **PR-1** (apply `tolerations`; the taint side is blocked
   without it). `nodeSelector` half works today.

2. **Namespace split + ResourceQuota + LimitRange** — run the control plane in
   `leoflow-system` and task pods in `leoflow-tasks` (the chart already separates
   `taskNamespace` from the release namespace — `values.yaml`), then let the
   platform apply a `ResourceQuota` and a `LimitRange` to `leoflow-tasks`. The
   **`LimitRange` is not optional dressing**: without default requests/limits a
   task pod is **BestEffort**, which is invisible to the Cluster Autoscaler's
   scale-up math and first in line for eviction under pressure; the `LimitRange`
   also **caps `ephemeral-storage`**, the resource a runaway task most easily
   exhausts on a shared node. Owner: platform GitOps (zero Leoflow PR — this is
   the principle in action).

3. **PriorityClass below online services** — so that under genuine contention the
   kube-scheduler preempts Leoflow's ETL, never production. The `PriorityClass`
   object is platform-owned (cluster-scoped, ranks neighbors — §1); Leoflow's
   part is only to **stamp `priorityClassName` onto its task pods**, which
   `BuildPod` does not do today. Gated on **PR-3** (`priorityClassName`
   passthrough, alongside affinity / topologySpread / ephemeral-storage /
   terminationGracePeriod / `resourceClaims` (DRA, GA 1.34), as an operator
   allowlist mirroring `taskPodSecurity`).

4. **Informer + APF FlowSchema** — bound Leoflow's share of the apiserver. The
   `FlowSchema`/`PriorityLevelConfiguration` is platform GitOps; the Leoflow side
   is **PR-10**: replace the per-task-instance polling LISTs with a shared pod
   **informer/watch**. This is a *correctness-of-scale* requirement in a shared
   cluster, not a nicety — see §4.

5. **NetworkPolicy (Leoflow's own lane)** — deny ingress by default, allow egress
   only to what tasks legitimately need (DNS + the control-plane gRPC), and
   **block the cloud metadata endpoint + RFC1918** so a DAG cannot pivot to
   internal services or steal the node's cloud identity (this is why ADR 0035's
   keyless posture pairs with it — no key to steal, and no metadata reach to abuse
   the node identity). The chart ships this (`taskNetworkPolicy`,
   `networkPolicy`) **off by default**; turning it on is an operator decision.
   Requires a CNI that enforces policy. Note this is Leoflow policing *its own*
   pods — distinct from the neighbor-governing NetworkPolicy the chart must not
   manage (§1). See the egress trajectory note in §4.1.

6. **Split API / scheduler roles (`split.enabled`, ADR 0049)** — the cheapest and
   most orthogonal rung: give the internet-facing API a restricted identity with
   **no apiserver RBAC**, so a compromised or SSRF'd API cannot reach the
   apiserver at all; only the scheduler SA is bound to the executor `Role`. Chart
   value, off by default.

### 4. The apiserver-cost driver — why PR-10 is a scale requirement, not a nicety

The dominant, steady-state apiserver cost Leoflow imposes on a shared cluster is
**not** the periodic reconcile LIST one would guess. It is the **pod-lost
reaper**: every ~1s tick it issues **one namespace-wide pod `LIST` per running
task-instance older than 60s — healthy ones included** (the liveness check runs
*after* the LIST, so a healthy pod does not spare the call). That is:

```
apiserver LIST/sec  ≈  O(running task-instances)   — per second, every second, unbounded by cluster size
```

~200 running tasks ⇒ ~200 namespace LISTs **per second**, most of them on pods
that are perfectly fine. In a shared cluster this dwarfs every other apiserver
term Leoflow contributes (reconcile's one LIST / 30s, staging-GC's one / 60s) and
lands directly on the APF budget the platform team is trying to protect.

Therefore this ADR records that **PR-10 (shared pod informer/watch) is a
correctness-of-scale requirement for a shared cluster, not a performance nicety.**
An informer turns per-task-instance liveness into an O(1) cache read and reaping
into event-driven O(Δ), collapsing the O(running-TIs)/sec LIST storm to a single
long-lived watch. Rung 4 of the ladder (APF FlowSchema) *bounds* Leoflow's share;
PR-10 *removes the reason it would ever blow that bound*. A Leoflow that stays on
the polling path cannot honestly promise good-tenant behavior at even moderate
task concurrency.

**PR-10 scope this release: informer/watch ONLY.** PR-10 ships the informer and
nothing more this release: liveness becomes a lister-cache read; GC becomes an
event-driven delete on terminal-phase + age; and the ADR 0052 termination-status
recovery read is served from the informer cache too (so success-recovery is also
a cache read, not a fresh GET). **`ownerReferences`-based cascade GC is explicitly
removed from PR-10's scope** and **deferred to PR-N1** (the warm-worker release),
because for today's ephemeral dedicated pods there is no per-DAG owner object to
point at — a bare `Pod` GC'd by the informer's terminal+age delete needs no owner
(ADR 0053, "Pod ownership and GC"). The per-DAG ConfigMap GC-anchor and its
`ownerReferences` arrive with the warm pool that actually needs cascade GC, not
before.

#### 4.1. Egress trajectory — per-pod NetworkPolicy is not the whole story

Rung 5's per-pod egress block (metadata endpoint + RFC1918) is real defense in
depth, but two facts bound it, and this ADR records the trajectory so the control
is not over-trusted:

- **Cluster-wide egress guardrails are migrating to `AdminNetworkPolicy`
  (KEP-2091, beta).** A per-namespace `NetworkPolicy` Leoflow ships for its own
  lane cannot express a cluster-operator's baseline egress deny that applies
  across tenants; that is what `AdminNetworkPolicy` / `BaselineAdminNetworkPolicy`
  are for, and they are platform-owned (§1). Leoflow keeps shipping its per-pod
  `NetworkPolicy` for its own pods, but notes it is one layer, not the whole
  story — the cluster's ANP baseline is where cross-tenant egress belongs.
- **The per-pod metadata `ipBlock.except` is CNI-dependent.** Whether a
  `NetworkPolicy` `ipBlock` actually blocks the `169.254.169.254` metadata IP
  depends on the CNI's enforcement of `ipBlock` egress; not every CNI honours it
  identically. So the metadata block is best-effort at the NetworkPolicy layer.

The durable defense is **keyless credentials (ADR 0035)**: if there is no
long-lived cloud key on the node to steal and no static credential the metadata
endpoint would hand out, the egress block's failure mode is far less severe. The
NetworkPolicy narrows reach; ADR 0035 removes the prize.

### 5. The namespace split also shrinks the B1 blast radius

Beyond bounding resource consumption, the namespace split (rung 2) has a security
payoff for the open whole-tenant-secret-exfiltration risk (B1, spec §5.1). Even
after ADR 0055 moves the token off the pod spec, isolating task pods in a
dedicated `leoflow-tasks` namespace means **fewer principals hold `get pods` on
the task-bearing pods** than if tasks ran in a busy shared namespace — a smaller
set of identities that can inspect task pods, i.e. a smaller B1 blast radius. This
does not *close* B1 (that needs the secret scoping + token liveness of ADR 0055,
a separate ADR), but it is a real, free reduction and a reason the split is on the
ladder rather than orthogonal to it.

## Open items

- **APF FlowSchema should split reaper LISTs from dispatch/leader calls into
  different flows.** Today Leoflow's control-plane calls the apiserver under a
  single ServiceAccount, so a platform `FlowSchema` matching that SA puts *all* of
  Leoflow's apiserver traffic — the reaper's residual LISTs, the dispatch
  `CREATE`s, and the leader-election calls — into **one flow**. Under contention,
  a burst of reaper LIST load can then starve dispatch and leader-election of
  their APF share, i.e. Leoflow throttles *itself* into scheduling lag and, worse,
  risks losing leadership. Even after PR-10 collapses the LIST storm, the residual
  LIST load should be classified into a **separate, lower-priority flow** from the
  latency-sensitive dispatch/leader calls (distinct `FlowSchema` matchers, e.g. by
  verb/resource or a dedicated SA), so reaping can never starve dispatch. The
  `FlowSchema` objects are platform GitOps; the Leoflow side is making its calls
  *distinguishable* (SA or user-agent) so the platform can write the matchers.
  Flagged here, not yet a PR.

## Consequences

- **The chart stays a workload manifest.** We do not template ResourceQuota,
  LimitRange, PriorityClass, or neighbor-governing NetworkPolicy; we document the
  GitOps objects an operator applies. The minimal-footprint property (one
  namespaced Role, no cluster-scoped objects, no CRD/webhook/DaemonSet) that makes
  Leoflow a viable guest is preserved by construction.
- **Two ladder rungs are gated on Leoflow work.** The dedicated node pool needs
  **PR-1** (`tolerations` in `BuildPod`); PriorityClass placement needs **PR-3**
  (`priorityClassName` passthrough, now including `resourceClaims`). Until then, an
  operator can only reach rungs 2, 5, 6 fully, plus a labeled-not-tainted pool via
  `nodeSelector`.
- **PR-10 is promoted from "optimization" to "coexistence blocker" — and scoped to
  the informer alone this release.** It is the single highest-impact change for
  shared-cluster viability, because the current reaper's O(running-TIs)/sec LIST is
  an unbounded tax on the shared apiserver. `ownerReferences` cascade GC is *not*
  in PR-10; it is deferred to PR-N1 where a per-DAG owner object exists (ADR 0053).
- **Node provisioning stays the cluster's job, forever.** By recording "Leoflow
  provisions no nodes" as an invariant with a CI-checked RBAC guardrail, we keep
  the door shut on a node agent / DaemonSet / cluster-autoscaler integration
  creeping in — any such thing would demand cluster-scoped privilege and break the
  guest posture.
- **Egress defense is layered, and the layers are named.** The per-pod
  `NetworkPolicy` is one layer with CNI-dependent metadata blocking; cluster-wide
  egress belongs to platform `AdminNetworkPolicy` (KEP-2091); keyless creds
  (ADR 0035) is the durable defense. None is trusted as the whole answer.
- **Lite is unaffected.** All of this is the Kubernetes execution path (ADR 0015);
  Lite is single-host, subprocess/in-process, no cluster, no pods, no neighbors.
  The ladder, the node-provisioning invariant, and the reaper cost are Pro-only
  concerns.
- **The line is now citable.** When a contributor proposes "let's have the chart
  set up the quota," or an operator asks "can Leoflow autoscale its nodes," the
  answer and its reasoning are written down, not re-litigated.

## Alternatives considered

- **Chart-manage the whole isolation stack (quota, limitrange, priorityclass,
  network policy).** Superficially convenient — one `helm install` and you are
  "isolated." Rejected: it inverts the ownership of shared-cluster budget (the
  platform team, not the tenant, allocates it), forces cluster-scoped RBAC for the
  PriorityClass, and turns a minimal guest into a cluster operator. It also fights
  GitOps: platform teams already manage quotas/policies declaratively and do not
  want a Helm chart racing them.
- **A Leoflow node agent / DaemonSet for placement or capacity.** Rejected
  outright: it would require cluster-scoped `nodes` privilege, add a per-node
  component to a system whose whole value proposition is a minimal footprint, and
  duplicate what the kube-scheduler and Cluster Autoscaler already do well. Leoflow
  creates pods; the cluster places and provisions. That division is the decision.
- **Leave coexistence undocumented and handle it per-deployment.** Rejected: the
  ownership line and the "PR-10 is a scale blocker" finding are non-obvious (the
  reaper cost in particular is a headline correction to the intuitive guess), and
  without an ADR they get re-discovered painfully in each pilot. The controls
  exist across the chart, the spec, and several PRs; this ADR is what makes them a
  coherent model.
- **Rely on APF FlowSchema alone to contain apiserver cost.** Rejected as
  sufficient: APF *throttles* Leoflow when it exceeds its share, which under the
  current reaper means Leoflow throttles *itself* into scheduling lag at moderate
  concurrency. Bounding the symptom is not fixing the cause; PR-10 removes the
  LIST storm so the FlowSchema rarely has to bite. (And even then, the single-flow
  problem in Open items means the FlowSchema must be *split* so reaping cannot
  starve dispatch.)
- **Ship `ownerReferences` cascade GC in PR-10 now.** Rejected for this release:
  today's dedicated pods are ephemeral bare `Pod`s with no per-DAG owner object to
  reference, so `ownerReferences` would be near-worthless churn. Cascade GC is
  deferred to PR-N1, where the warm pool introduces a per-DAG ConfigMap anchor
  that actually needs it (ADR 0053).
