---
title: Control-plane HA and disruption posture
linkTitle: Control-plane HA
weight: 75
description: "Why a single control-plane replica is evicted as routine housekeeping, what each restart costs, and the one-switch HA profile that turns it into seconds of failover."
---

A Pro control plane with one replica is not "stable until something breaks". It
is evicted as **routine cluster housekeeping** — and every eviction is a full
restart. This page explains the restart window and what is at risk inside it,
why running two replicas is the recommended production posture, the storage
precondition that makes it safe, and how the chart guards the combinations that
are not.

## The restart window

When the single control-plane pod is evicted, the replacement has to:

1. **Pull the image** on whatever node it lands on (nothing cached — the old node
   is usually the one being drained).
2. **Boot**: connect to Postgres and Redis, start the API, gRPC and metrics
   listeners, pass readiness.
3. **Take leadership**: acquire the scheduler's advisory lock
   ([ADR 0009](/project/adrs/0009-leader-election/)) and start the loop. Until
   the new leader has **settled** — the settling grace (180 s by default) has
   elapsed, the pod informer has synced, and the pod reconciler has completed a
   sweep under this leadership — **no reaper** fires, so a task that merely lost
   its control plane for the duration of the restart is not failed before the
   reconciler has recovered its durable outcome. One gate covers all five
   reapers; a liveness valve opens it after 2 × grace if the sweep never
   completes ([scheduler resilience](/operate/scheduler-resilience/)).

Measured on a production drill (EKS Auto Mode), that window is **48–80 seconds**,
dominated by the image pull. During it:

- **Nothing dispatches.** Queued task instances wait; scheduled runs slip.
- **In-flight tasks lose their control plane.** Agents keep running the task and
  retry their heartbeats and outcome reports until the server returns. The
  leader-settling gate on the reapers is what keeps that from becoming a false
  `agent_lost` / `pod_lost` failure; the server also validates at boot that the
  timing ladder this depends on holds (heartbeat < agent-lost threshold <
  settling grace < token TTL, and two maintenance cycles inside the grace),
  refusing to start if a knob was moved out of order. These bound the damage;
  they do not remove the window.
- **The API and UI are down.** Every replica is the same binary, so one replica
  means one API.

## Why eviction is routine, not exceptional

On a managed autoscaling platform the control-plane pod is just another pod to
bin-pack:

- **Karpenter (EKS Auto Mode, or self-managed)** consolidates under-utilized
  nodes as a matter of course: it evicts the pods, deletes the node, and the pods
  reschedule elsewhere. A single-replica control plane is consolidated like any
  stateless workload — the drill above observed it as ordinary bin-packing, not
  a failure.
- **GKE Autopilot** does the equivalent — the platform owns the nodes and
  compacts them.
- **Node auto-upgrades** (EKS, GKE, AKS) drain every node in turn on a schedule
  you may not control.
- **Involuntary disruptions** — node failure, spot/preemptible reclamation, a
  kernel panic, a kubelet crash — hit every platform and give no warning that a
  budget could honor.

The first three are *voluntary* disruptions: Kubernetes asks before evicting and
honors a PodDisruptionBudget. The last is not. The posture below handles both.

## HA is the recommended production posture

The control plane already supports more than one replica: the scheduler
**leader-elects** — one active scheduler, the others standing by on the same
Postgres advisory lock — and the API serves **active-active** from every replica
([ADR 0009](/project/adrs/0009-leader-election/)). With `replicaCount: 2`
(non-split — see [split mode](#split-mode-is-not-dispatch-ha) below):

- an eviction of the leader becomes a **failover measured in seconds** — the
  standby is already pulled, booted and connected; it only has to win the lock
  (followers poll every few seconds);
- the API and UI stay up on the surviving replica;
- a rolling upgrade is a real rolling upgrade instead of a stop-the-world
  `Recreate`.

### One switch: the HA profile

The chart ships a complete overlay,
[`helm/dexaflow/examples/values-ha.yaml`](https://github.com/dexadata/dexaflow/blob/main/helm/dexaflow/examples/values-ha.yaml):

```bash
helm upgrade --install dexaflow oci://ghcr.io/dexadata/charts/dexaflow --version <x.y.z> \
  -n leoflow -f values-ha.yaml
```

It sets, and documents why:

| Value | HA profile | Purpose |
|---|---|---|
| `replicaCount` | `2` | leader-elected scheduler standby + active-active API |
| `topologySpreadConstraints` | hostname spread, `ScheduleAnyway` | two replicas bin-packed onto one node are one replica (see below) |
| `logs.persistence.enabled` / `logs.sink.provider` | `false` / `s3` (or `gcs`) | task logs in object storage — the recommended HA log path (see below) |
| `resources` | `1Gi` request / `2Gi` limit | the object sink keeps each running attempt's log in memory until the attempt ends (it is flushed incrementally, but re-uploaded whole); size for your fan-out |
| `podDisruptionBudget.enabled` | *unset (auto)* | the PDB renders itself because the replica floor is above one; `unhealthyPodEvictionPolicy: AlwaysAllow` |
| `terminationGracePeriodSeconds` | `60` | headroom for the HTTP shutdown, the dispatch-pool drain and the bounded gRPC stop (see below) |
| `podAnnotations` → `karpenter.sh/do-not-disrupt` | *commented out* | EKS/Karpenter-only opt-in, see below |

Edit the `CHANGEME` datastore URLs, secrets and bucket, and bind the
control-plane ServiceAccount to the cloud identity that may write the bucket:
IRSA on EKS (`serviceAccount.annotations`), **EKS Pod Identity** — AWS's current
recommendation, which needs *no* annotation and is configured as a Pod Identity
association against the ServiceAccount instead — or Workload Identity on GKE. In
[split mode](#split-mode-is-not-dispatch-ha) `serviceAccount.annotations` is
rendered onto **both** ServiceAccounts and both need the identity: the scheduler
*writes* task logs to the bucket and any api replica *reads* them back to serve
the UI, so annotating one role leaves half the control plane without credentials.

{{% alert title="Why the chart does not default to two replicas" color="info" %}}
Because it would break every existing install on upgrade. The default install
keeps task logs on a `ReadWriteOnce` PVC; a second replica scheduled to another
node hangs in `ContainerCreating` on a **Multi-Attach** error while `helm
upgrade` reports success. "First-class HA" therefore means an explicit,
documented, one-file profile plus render-time guards — not a flipped default.
{{% /alert %}}

### Two replicas on one node are one replica

Nothing in Kubernetes keeps two replicas of a Deployment apart by default, and
autoscaler consolidation actively pushes them together — bin-packing both onto
one node is exactly what it is for. Then a node failure takes the whole control
plane, and the auto PDB makes it worse: `minAvailable: 1` with both pods on the
node being drained **blocks the drain**, which is the single-replica trap one
level up. The profile therefore sets a `topologySpreadConstraints` entry on
`kubernetes.io/hostname` with `maxSkew: 1`:

```yaml
topologySpreadConstraints:
  - maxSkew: 1
    topologyKey: kubernetes.io/hostname
    whenUnsatisfiable: ScheduleAnyway
```

`whenUnsatisfiable: ScheduleAnyway`, deliberately not `DoNotSchedule`: on a
single-node cluster `DoNotSchedule` leaves the second replica `Pending` forever,
and the PDB then blocks every drain of the one node. A constraint that omits
`labelSelector` gets the Deployment's own selector labels from the chart, so the
values file does not need to know the release name. On multi-zone clusters add
a second constraint on `topology.kubernetes.io/zone` (also `ScheduleAnyway`).

### Split mode is not dispatch-HA

The api/scheduler role split
([ADR 0049](/project/adrs/0049-split-api-and-scheduler-roles/)) is a
**security** topology, not an availability one. Its `api` Deployment is HA — it
runs `split.api.replicaCount` active-active replicas and gets the auto PDB — but
its `scheduler` Deployment is **pinned to one replica** by the chart, with no
standby and, correctly, no PDB (a budget over one pod would block every drain).
Every eviction of the split scheduler is therefore still the full restart window
above: image pull, boot, leadership. Non-split `replicaCount: 2` is the only
topology that gives dispatch failover today; choose the split when you need the
restricted API identity more than scheduler failover, and know which one you
picked.

### Warm pools are rebuilt on the far side of a failover

[Warm worker pools](/operate/warm-pools/) survive a failover as *pods*, but not
as a *pool*. The assignment stream every warm worker holds open
(`AwaitAssignment`) is gated to the scheduler **leader**, and the registry of
which workers are warm, which pool they serve and which are free is
**in-memory and leader-local** — none of it is persisted or shared between
replicas. So a new leader starts with an empty registry and the pool is rebuilt
from the workers' side:

- **Idle warm workers exit and are replaced.** A control plane on its way out
  ends **every** assignment stream at `SIGTERM` with `Unavailable`, and a worker
  sitting idle treats that as a clean recycle: it exits `0`, with no `ERROR`
  line and no failed pod. A hard-killed leader is the same *code* but not the
  same *timing*. A killed process has its sockets torn down by the kernel, so
  the worker's blocked receive fails promptly with `Unavailable` too; a lost
  **node** closes nothing, and nothing pings — the agent's dial configures no
  gRPC keepalive, and the control plane deliberately enables no server-initiated
  keepalive either (it only *permits* a client's pings on an otherwise idle
  stream, so gRPC does not `GOAWAY` them as abusive). "Nothing pings" is true of
  gRPC only: Go's dialer still enables TCP keepalive at a 15s period, so the
  kernel gives up after roughly 2.5 minutes on Linux defaults rather than never,
  and the assignment stream's own idle TTL (5 minutes by default) is a second
  backstop. So that worker sits blocked on a stream to a control plane that is
  already gone for minutes, not seconds — bounded, but far longer than the
  prompt failure a killed process produces. Tracked as
  [#946](https://github.com/dexadata/dexaflow/issues/946). The new leader's warm-pool
  reconciler is leader-gated as well, and it counts live warm pods from the
  **apiserver** and busy ones from the durable `warm_worker_id` binding, never
  from the registry, so it rebuilds each active DAG version's
  `minIdleWorkers` buffer on its own cycle without double-creating.
- **A worker handed a follower reconnects toward the leader.** Every replica
  serves the agent gRPC port, so the Service can route a worker to a follower,
  which refuses the stream with `FailedPrecondition` ("not the scheduler
  leader"). The worker re-dials with jittered exponential backoff until it
  reaches the leader — bounded at **10** consecutive rejections, a fixed
  constant today (the bound is a field only tests set), after which it exits
  non-zero and lets the reconciler replace the pod rather than spinning forever
  against a misconfigured deployment.
- **A busy worker finishes its attempt, then exits non-zero — by design.** The
  server puts **no busy predicate** on the shutdown: the raw `SIGTERM` context
  *is* the shutdown signal the assignment handler selects on, so every open
  stream ends there, busy or idle. What is idle-specific is the **worker's**
  handling, and deliberately so — it accepts `Unavailable` as a clean end only
  in the branch that is waiting for work, because an attempt dying mid-flight
  and a real outage must both still surface as failures, and a test locks that.
  So a busy worker runs its attempt to completion and reports its terminal
  state; then its slot-free send fails on the dead stream, the error is
  loop-fatal, it propagates out of the worker loop, and the process logs one
  `ERROR` and exits `1`. Warm pods are `RestartPolicy: Never`, so **expect one
  `Failed` warm pod and one `ERROR` line per busy worker per control-plane
  restart** — and that `Failed` pod is exactly the signal the warm-worker-lost
  reaper keys on. A terminal warm pod is not live, so any attempt still bound to
  it — via the durable `warm_worker_id` written when the worker acked the
  assignment — is re-placed on the infrastructure-retry budget without charging
  the user's `try_number`
  ([scheduler resilience](/operate/scheduler-resilience/)).

The cost is a **cold window just after a failover**, on top of those exits. Warm
placement is assign-if-free-else-dedicated, so until workers have re-registered
against the new leader, attempts fall through to the dedicated pod-per-task path
and pay the start-up they would otherwise have amortized. Attempts themselves
are not stranded — the binding and the reaper cover the ones a dead worker held
— but a failover is not free of failures either: budget one `Failed` warm pod
per busy worker, and read a post-failover `ERROR` from a warm worker as the
expected shape rather than an incident.

## The storage precondition

Task pods stream their logs over gRPC to the control plane, which persists them
under `config.logsDir`. Every replica must be able to **write** (the leader
does) and **read** (any API replica may serve the log) the same store. That
rules out the default volume:

| Log store | HA-safe? | Notes |
|---|---|---|
| **Object storage** — `logs.persistence.enabled: false` + `logs.sink.provider: s3` or `gcs` ([ADR 0056](/project/adrs/0056-task-log-object-sink/)) | **Yes — recommended** | no PVC at all; every replica reads the same bucket; retention is the bucket's lifecycle policy; keyless auth via the control-plane ServiceAccount; the Deployment can roll. Read the durability note below |
| **`ReadWriteMany` PVC** — `accessMode: ReadWriteMany` on an RWX class (EFS, GCP Filestore, Azure Files, NFS, CephFS, Longhorn-rwx) | Yes | the alternative when no object store is reachable from the control plane; the disk sink flushes through a 1 MiB buffer, so a mid-attempt control-plane death loses at most that last chunk; needs an RWX-capable StorageClass |
| **`ReadWriteOnce` / `ReadWriteOncePod` PVC** (the default) | **No** | attaches to one node / one pod; a second replica Multi-Attach-deadlocks |
| **emptyDir** — `logs.persistence.enabled: false` with `logs.sink.provider: disk` | No | each pod keeps its own logs: the leader's logs are invisible to every other pod's API, and everything is lost on restart. Dev only. The chart's NOTES warn whenever more than one pod can mount it (`replicaCount`, the HPA ceiling), and **refuse to render** it in split mode, where the scheduler is the only writer and no api pod could ever read a log |

**What the object sink makes durable, and when.** The live tail the UI shows
while a task runs is Redis pub/sub and works from any replica. The durable
object is **rewritten incrementally while the attempt runs**: object stores
have no append, so the control plane accumulates the attempt in memory and
re-uploads the whole object (an overwrite `Put` of the same key) whenever the
unflushed tail reaches 1 MiB, or every 5 s while anything is unflushed, with a
final write on close. Once the stored object is large the cadence is damped —
a flush waits until the unflushed tail is at least one eighth of what is
already stored — so a big log costs about nine times its final size in upload
rather than a quadratic bill; small logs (the common case) trail the live tail
by at most a few seconds. What a kill loses is therefore **the unflushed tail
of each running attempt** — for a typical task, the lines of the last ~5 s —
not the attempt's whole log; and an orderly shutdown loses nothing on the
control-plane side, because open log streams are closed and flushed at
`SIGTERM` (see [drain grace](#drain-grace)). Two honest caveats. First, the
agent does not currently re-open a log stream the control plane closed, so the
lines a task prints *after* its control plane went away are not shipped by that
task; the task itself is unaffected, the reconciler still recovers its outcome,
and a replacement control plane serves the stored partial log. Second, a bucket
with **object versioning** keeps every rewrite as a version — set a lifecycle
rule on noncurrent versions or expect the versioned size to be several times
the log size. On memory: the whole attempt is still held in RAM until it ends
(capped at 128 MiB per attempt), so size `resources.limits.memory` for
*concurrent attempts × their log volume*, not for the chart's 512 Mi default (a
wide fan-out of chatty tasks can OOM a small control plane, and OOM is a
restart — the flushed prefix survives it). If even a few seconds of tail is
unacceptable for you, prefer the RWX PVC, which flushes at 1 MiB without
re-uploading.

The chart refuses to render the unsafe combination. `replicaCount > 1` (or an
HPA with `maxReplicas > 1`, or the api/scheduler split — any shape where more than
one pod mounts the PVC) with a single-writer access mode fails `helm install` /
`helm upgrade` / `helm template` with:

```text
more than one control-plane pod would mount the logs PVC (replicaCount > 1,
autoscaling.maxReplicas > 1, or split.enabled), but
logs.persistence.accessMode=ReadWriteOnce attaches the volume to a single
node/pod: the extra pods hang in ContainerCreating with a Multi-Attach error
while the install reports success. HA needs a log store every replica can write.
Recommended: set logs.persistence.enabled=false and ship task logs to object
storage (logs.sink.provider=s3|gcs with logs.sink.bucket). Alternative:
logs.persistence.accessMode=ReadWriteMany on an RWX StorageClass (EFS, Filestore,
Azure Files, NFS, CephFS, Longhorn-rwx). Or keep a single replica. See
helm/dexaflow/examples/values-ha.yaml.
```

Safe-by-default means exactly this: HA can never silently deploy onto a volume
only one pod can hold.

### Moving an existing single-replica install to HA

1. **Pick the log store first.** Switch to the object sink (recommended) or
   re-create the logs PVC on an RWX class. The chart cannot convert an existing
   `ReadWriteOnce` claim in place — access modes are immutable on a bound PVC.
2. **Know what happens to old logs.** Logs written before the switch stay where
   they were. With the object sink the control plane serves logs from the bucket
   only, so keep the old PVC around (or copy it into the bucket under the same
   `{prefix}/{tenant}/{dag}/{run}/{task}/{try}.e{epoch}.log` layout, one object per execution of the try; logs written before 0.5.1 are at `{try}.log`) if you need the
   history in the UI.
3. **Apply the profile.** `helm upgrade -f values-ha.yaml`. With the PVC gone the
   update strategy auto-selects `RollingUpdate`, the second replica comes up
   alongside the first on another node (the spread constraint), and the PDB
   appears — the install NOTES say so.
4. **Confirm leadership moves.** Delete the leader pod once and watch the
   standby take the lock within seconds — no image pull, and the dispatch gap is
   the re-election poll, not a restart.

### Autoscaling: set both bounds, or neither

`autoscaling.minReplicas` and `autoscaling.maxReplicas` are independent values
with independent defaults — 2 and 6 — so overriding one and not the other
inverts them. The obvious shrink, `--set autoscaling.maxReplicas=1`, leaves the
minimum at 2 and produces a HorizontalPodAutoscaler with a maximum below its
minimum, which the apiserver rejects.

The chart now refuses that pair at render time rather than letting the install
fail, and it refuses a minimum below 1 as well: Kubernetes rejects one unless
the `HPAScaleToZero` feature gate is enabled, which this chart does not assume.

```yaml
autoscaling:
  enabled: true
  minReplicas: 1   # set both together
  maxReplicas: 3
```

With `maxReplicas` above 1 you also need the storage precondition above
satisfied — more than one control-plane pod cannot share a ReadWriteOnce log
volume, and the chart refuses that combination outright, so the snippet above
only installs as written at `maxReplicas: 1`. Above that, pair it with
`logs.persistence.enabled: false` and a `logs.sink`, or an RWX access mode.

Both bounds must be whole numbers. A float in a values file used to coerce past
the check and render a fractional replica count the apiserver rejects, so
non-integers are refused too.

### Postgres connection budget

Every control-plane pod opens three connection pools against the same
database:

| Pool | Size | Opened by |
|---|---|---|
| Requests and scheduling | `database.maxOpenConns` (chart default `20`) | every pod |
| Health checks (`/readyz`, the UI database widget) | `2` | every pod |
| Scheduler leader lock | `1` | every pod that runs the scheduler (`all` and `scheduler` roles) |

So one pod can hold up to `maxOpenConns + 3` connections, and the cluster-wide
ceiling is that number times the most pods that can be up at once: the HPA's
`maxReplicas` (or `replicaCount`, or `split.api.replicaCount + 1` in split
mode), plus the surge pods a rolling update starts before it stops old ones.
The chart sets no `maxSurge`, so Kubernetes' default of 25% of the replicas,
rounded up, applies to each Deployment. The migration Job adds one
short-lived connection per upgrade.

With the shipped defaults and the HPA on at `maxReplicas` 6, a rollout at full
scale runs 6 pods plus `ceil(25% of 6) = 2` surge pods, so the worst case is
`8 * (20 + 3) = 184` connections. Stock Postgres allows
`max_connections = 100`, of which 3 are reserved for superusers, and managed
offerings often size it from instance memory, so a small instance can allow
fewer. When the pools reach the limit, new
connections fail with `FATAL: sorry, too many clients already`: requests
return 5xx and `/readyz` can fail across every replica at once.

Keep `pods * (maxOpenConns + 3)`, counting the surge pods, below the server's
`max_connections` minus whatever else connects to it (backups, migrations,
dashboards). In order of preference:

1. **Lower `database.maxOpenConns`.** A replica rarely needs 20 connections in
   flight; `8` at 6 replicas plus 2 surge pods is `8 * (8 + 3) = 88`.
2. **Cap `autoscaling.maxReplicas`** at what the database can serve.
3. **Raise `max_connections`** on the server, if the instance has the memory
   for it (each connection is a backend process).

**PgBouncer.** A pooler in front of Postgres multiplexes many client
connections over few server connections, but only in `transaction` pool mode,
and two things in the control plane need a real session:

- The scheduler's leadership is a session-scoped `pg_try_advisory_lock`
  (ADR 0009). In `transaction` mode the lock is taken on whichever server
  connection served that statement and is not tied to the pod, so two pods
  can both believe they lead. Processes that run the scheduler must connect
  to Postgres directly, or through PgBouncer in `session` mode.
- The migration Job takes an advisory lock the same way.

The api role in split mode holds no session state and can go through
`transaction` mode. pgx then needs `default_query_exec_mode=simple_protocol`
in the DSN (for example
`postgres://user:pass@pgbouncer:6432/dexaflow?default_query_exec_mode=simple_protocol`),
because named prepared statements do not survive a change of server
connection. The chart takes one `database.url` for every role, so it cannot
send the api Deployment through PgBouncer and the scheduler around it; until
it can, size the pools as above.

## The PodDisruptionBudget — and the single-replica trap

A PodDisruptionBudget tells the eviction API how many pods must stay up during a
*voluntary* disruption. With two replicas and `minAvailable: 1`, a drain or a
consolidation evicts one replica, waits for the other to be ready, then the next
— the control plane never disappears. That is why the chart renders the PDB
**automatically when the guaranteed replica floor is above one** (`replicaCount`,
or the HPA `minReplicas`, or `split.api.replicaCount` in split mode).

The same budget over a **single** replica is a trap. `minAvailable: 1` over one
pod means *no* voluntary eviction of that pod is ever allowed:

- `kubectl drain` hangs on it indefinitely.
- Cluster and node auto-upgrades stall on that node — until the platform's
  patience runs out and it overrides the budget anyway.
- Autoscaler consolidation of that node is blocked (Karpenter) — which sounds
  like protection but leaves an under-utilized node pinned by one pod.
- And none of it helps against an involuntary disruption: the pod still dies
  with the node.

It protects the pod at the cost of cluster operations. So the chart never turns
it on for a single replica by default; `podDisruptionBudget.enabled: true` still
forces it (an informed choice — the install NOTES say what it costs), and
`false` forces it off even in HA.

Two more details the chart gets right for you:

- **`unhealthyPodEvictionPolicy: AlwaysAllow`** (the chart default; `""` omits
  the field for apiservers older than 1.27). Kubernetes' own default,
  `IfHealthyBudget`, refuses to evict even *unhealthy* pods once the budget is
  unmet — so with both replicas unready (bad database credentials after a
  rotation, an image-pull failure, a wedged rollout) a node drain hangs on a
  control plane that is already down. `AlwaysAllow` lets the broken pods go.
- **Auto mode counts every shape.** The floor is `replicaCount`, or the HPA
  `minReplicas` when autoscaling is on, or `split.api.replicaCount` in split
  mode. That means an existing `split.enabled` install (api default 2) or
  `autoscaling.enabled` install (`minReplicas` default 2) **gains a PDB on
  upgrade** to this chart version; the upgrade NOTES call it out, and
  `podDisruptionBudget.enabled: false` opts out if your maintenance tooling
  assumed none.

### How platforms honor a PDB

| Platform | Voluntary disruption source | PDB behavior |
|---|---|---|
| **EKS with Karpenter** (incl. Auto Mode) | consolidation, drift, node expiration | Honored: a node whose pod is blocked by a PDB is not disrupted. A NodePool `terminationGracePeriod` can force the drain past a blocking PDB after that period — check yours. The pod annotation below exempts the node from voluntary disruption entirely |
| **GKE Standard** | cluster autoscaler scale-down, node auto-upgrade, maintenance | Honored on scale-down (the node is not removed). Upgrades and maintenance honor the budget for a **bounded** window (on the order of an hour per node), then proceed |
| **GKE Autopilot** | platform compaction, auto-upgrade | Same as Standard, with the platform owning the nodes: honored, bounded, then overridden |
| **AKS** | cluster / node-image upgrade, node pool scale-down | Honored during drain; when the drain cannot complete within the timeout the **upgrade operation fails** rather than overriding the budget, so a blocking PDB fails your maintenance instead of protecting you through it |
| Any — `kubectl drain`, cluster-autoscaler | manual drain, scale-down | Honored; a blocked single replica hangs the drain until someone adds `--disable-eviction` or deletes the pod |

Two replicas plus the auto PDB behave well on **every** row of that table. A
single replica with a forced PDB behaves badly on every row.

## `karpenter.sh/do-not-disrupt` — EKS/Karpenter-only opt-in

Karpenter honors a pod annotation, `karpenter.sh/do-not-disrupt: "true"`, that
exempts the pod's **node** from Karpenter's voluntary disruption: no
consolidation, no drift replacement, no expiration while the pod runs. The chart
wires `podAnnotations` into the control-plane pod template, so it is one
uncommented line in the HA profile:

```yaml
podAnnotations:
  karpenter.sh/do-not-disrupt: "true"
```

It is deliberately **not** set by default:

- it is meaningless outside Karpenter (GKE, AKS and a plain cluster-autoscaler
  ignore it);
- it trades **maintenance friction for eviction protection** — that node is never
  consolidated or rolled by Karpenter while the control plane sits on it, so
  drift (a new AMI) and consolidation savings stop at that node;
- it does nothing against involuntary disruptions.

Reach for it only when you have measured that consolidation on your cluster
still evicts the control plane faster than failover recovers it — and prefer
the second replica first.

## Drain grace

A voluntary eviction sends `SIGTERM` and waits `terminationGracePeriodSeconds`
(Kubernetes default 30s) before `SIGKILL`. Be precise about what the grace buys,
because it is not leadership:

- **Leadership handoff does not depend on it.** The scheduler releases its
  advisory lock within about one tick of `SIGTERM`, and the lock frees anyway
  the moment the Postgres connection drops — `SIGKILL` included. The standby
  wins the lock on its next poll either way.
- **What it does buy is drain headroom.** On `SIGTERM` the server gives
  in-flight HTTP requests up to 10 s to finish, then drains the dispatch pool so
  dispatches already in progress settle instead of leaving task instances stuck
  `queued`. Reapers are gated off during the step-down so nothing destructive
  fires from a dying leader.
- **A second replica can stop taking new streams before the first one stops.**
  Endpoint removal is asynchronous, so for a propagation window after termination
  begins, task pods still open **new** agent log streams against the replica that
  is on its way out. `deployment.preStopSleepSeconds` spends that window in a
  `preStop` sleep before the process is signalled, so those streams open against
  the surviving replica instead. It is **off by default** and set to `5` in
  `examples/values-ha.yaml`, because moving a stream needs somewhere to move it
  to: a single-replica install (the chart default, and the split topology's
  scheduler, which is what serves agent gRPC) has no second endpoint, so the
  sleep buys nothing **for agent log streams** there and adds its own seconds to
  every upgrade. That is the scheduler-side singleton case, not the whole story:
  the hook renders on **both** split Deployments deliberately, and on the api
  side with `split.api.replicaCount` > 1 the same sleep spends the same
  propagation window for HTTP and UI requests, which have a second endpoint to
  land on. What it never buys, at any replica count, is an
  **already-established** stream: those
  connections are pinned by conntrack and get `Unavailable` at `SIGTERM`
  regardless — only not-yet-opened streams move. The sleep runs *inside*
  `terminationGracePeriodSeconds`, ahead of everything below, so count it in the
  budget (Kubernetes rejects the pod spec outright if the sleep alone exceeds the
  grace, and the chart refuses to render that). 5 s is the low end; raise it when
  a service-mesh sidecar sits in the path. The hook needs Kubernetes **1.30**,
  where the native `sleep` action is beta and on by default; it is alpha and
  *off* in 1.29, and an apiserver without it rejects the empty `preStop: {}` it
  is left with instead of ignoring it. So the chart renders the hook only on
  1.30+ and omits it below — a 1.27–1.29 install keeps working, with the
  pre-hook behavior, and the install notes warn that the value you set had no
  effect rather than leaving the omit silent.
- **With tasks running, the stop is bounded.** Open agent log streams are
  closed and flushed the moment `SIGTERM` arrives (the agent keeps running its
  task; log shipping is best-effort), idle warm-worker assignment streams end
  the same way, and the gRPC graceful stop that follows is bounded at 5 s before
  falling back to a forced stop — which still lets the remaining handlers finish
  their deferred flushes, waited for up to another 5 s.

  Two different clocks, and the grace has to cover both. **Process shutdown** is
  everything after `SIGTERM`: normally milliseconds, well under a second, warm
  pools on or off; bounded at HTTP (≤10 s) + dispatch drain (≤15 s, configurable)
  + gRPC stop (≤10 s) ≈ 35 s plus the telemetry flush. **Pod termination** is
  what an operator watches, and it also includes the `preStop` sleep, which runs
  *before* `SIGTERM` and is pure wall-clock: with the HA profile's `5` a
  completely healthy pod takes just over 5 s to go away, and that is expected,
  not a slow shutdown.

  So size the grace by the rule, not by a number:
  `terminationGracePeriodSeconds` ≥ `deployment.preStopSleepSeconds` + 10 s
  (HTTP) + the dispatch drain + 10 s (gRPC), with headroom. The default 30 s
  covers a normal shutdown but not that worst case, so an installation running
  the object log sink at scale should raise it — the HA profile's `60` holds the
  full bound plus its 5 s sleep. A `SIGKILL` (`exitCode: 137` on the terminated
  container) means a stop that overran the grace — look for the `agent grpc
  graceful stop exceeded its bound` warning first, then for a slow object store.
  That warning is meant to be rare: every long-lived agent stream ends itself at
  `SIGTERM`, so seeing it on every shutdown is a bug, not a tuning problem.
  Nothing in the leadership handoff needs any of this to finish.

The HA profile sets `60` for comfortable HTTP + dispatch drain headroom under
load. The chart deliberately ships **no default**: a default would add up to
30 s of downtime to every single-replica `Recreate` upgrade, for nothing.

Three values mean the same thing here — unset, `null`, and an explicit `0`: the
chart omits the field and **Kubernetes' own 30 s applies**, which is also the
grace the `preStopSleepSeconds` guard reasons about. A literal `0` is
deliberately *not* rendered, because in a pod spec it means `SIGKILL` with
nothing drained at all — in-flight HTTP requests cut, the dispatch pool never
settling (task instances left stuck `queued`), open agent log streams never
flushed, and any `preStop` sleep unsatisfiable. If you really want no grace, set
it on the pod spec yourself rather than through this value.

## Involuntary disruptions: why HA is the posture that matters

No PDB, annotation or grace period prevents:

- **node failure** — hardware, kernel panic, kubelet or container-runtime crash;
- **spot / preemptible reclamation** — a two-minute notice at best, no eviction
  API involved;
- **zone or network partition** that isolates the node;
- **platform-side forced maintenance** once a bounded PDB window expires.

Each of those is the 48–80 second restart window with no warning. The only
thing that shrinks it is a replica that is already running somewhere else — and
the scheduler's own resilience — the leader-settling gate that holds every reaper
until the reconciler has swept under the new leader, the destructive gate that
holds every reaper during a step-down, the reconciler that recovers a pod's
durable outcome (now run in the same cycle as, and before, the reapers), and the
boot-time check of the timing ladder they depend on, described in
[scheduler resilience](/operate/scheduler-resilience/) — is what keeps the tasks
that were in flight during the window from being falsely failed. HA shortens the
window; the resilience mechanisms make the remaining window survivable. Run both.

## Related

- [Helm chart](/operate/helm-chart/) — the entry point and the chart README
  with the full values table.
- [Scheduler resilience](/operate/scheduler-resilience/) — what happens to
  in-flight tasks around a restart.
- [Warm worker pools](/operate/warm-pools/) — the pool is leader-local and is
  rebuilt on failover; what that costs the attempts that follow one.
- [Upgrades](/operate/upgrades/) — rolling a control-plane release, edition by
  edition.
- [ADR 0009](/project/adrs/0009-leader-election/) — leader election;
  [ADR 0049](/project/adrs/0049-split-api-and-scheduler-roles/) — api/scheduler
  split; [ADR 0056](/project/adrs/0056-task-log-object-sink/) — the task-log
  object sink.
