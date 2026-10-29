---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /helm-chart.html
# --- end AUTO redirect aliases ---
title: Helm chart
linkTitle: Helm chart
weight: 30
description: Install and configure the Dexaflow Cluster control plane on Kubernetes with the official Helm chart.
---

The **Cluster** control plane installs on Kubernetes via the official Helm chart. The
chart is chart-test gated, publishes multi-arch images per release, and signs them
with cosign. It runs on any cluster with an external Postgres + Redis.

The chart runs the server as the cluster edition. It still sets
`LEOFLOW_UI_EDITION=pro`: `pro` is the earlier name of `ui.edition: cluster`,
the server accepts both with the same features, and the chart switches to
`cluster` once every supported server accepts it. See
[Editions](/concepts/editions/).

To try the chart on your machine first, with throwaway datastores and a local
registry, see [Try the cluster edition on a local cluster](/operate/local-cluster/) (for testing
only, not for production).

## Install from the published OCI chart

Every release tag publishes the chart as a **cosign-signed OCI artifact** to
`oci://ghcr.io/dexadata/charts/dexaflow` ([ADR 0028](/project/adrs/0028-release-versioning-two-editions/)),
co-versioned with the release tag — so you can install a pinned version without
cloning the repo. Its signature and its build provenance are checked as shown
in [Build provenance and SBOMs](/get-started/installation/#build-provenance-and-sboms):

```bash
helm install dexaflow oci://ghcr.io/dexadata/charts/dexaflow --version <x.y.z> \
  -n dexaflow --create-namespace \
  -f values.yaml
```

Pass the release tag **without** the leading `v` (tag `vX.Y.Z` → `--version X.Y.Z`):
the chart `version`/`appVersion` move in lockstep with the tag, so this also pins
the control-plane image. Installing from a source checkout
(`helm install ./helm/dexaflow`) is still supported for unreleased branches — see
the [chart README](https://github.com/dexadata/dexaflow/blob/main/helm/dexaflow/README.md#quick-start)
for both paths and the full values surface.

{{% alert title="Reference lives with the chart" color="info" %}}
This operator-journey page is the entry point; the exhaustive values reference is
maintained **alongside the chart source** so it never drifts from `values.yaml`:

**[→ Helm chart README](https://github.com/dexadata/dexaflow/blob/main/helm/dexaflow/README.md)**
(including the [datastore compatibility matrix](https://github.com/dexadata/dexaflow/blob/main/helm/dexaflow/README.md#datastore-compatibility)).

A first-class values reference on this site is a TODO for a later migration phase.
{{% /alert %}}

## What the chart gives you

- The `/api/v2/` Airflow-compatible API + UI, and the scheduler — as one process
  (`role=all`) or split into `role=api` + `role=scheduler`
  ([ADR 0049](/project/adrs/0049-split-api-and-scheduler-roles/)).
- A guarded HA posture: `replicaCount > 1` refuses to render onto a single-writer
  log volume, and the PodDisruptionBudget turns itself on exactly when a second
  replica makes it safe — see
  [Control-plane HA and disruption posture](/operate/control-plane-ha/) and the
  one-switch `examples/values-ha.yaml` profile.
- Opt-in hardening templates: HPA, NetworkPolicy, and a Prometheus
  ServiceMonitor.

  Two things about that combination are worth knowing before you turn them on.

  **The metrics port has its own ingress rule, and its default allows any
  namespace.** `networkPolicy.metricsFrom` is where you narrow it, and you
  should: the metrics listener is unauthenticated and its series carry
  `dag_id` and `task_id`, so "reachable at all" is not the same bar as the
  mostly JWT-gated API on 8080. Point it at your Prometheus namespace. Setting
  it to an explicitly empty list closes the port, which is a supported choice —
  it was just a very poor *default*, because an Ingress-typed policy denies
  what it does not match, so `networkPolicy` plus
  `metrics.serviceMonitor.enabled` used to give you a target that was created
  and never answered.

  **The HPA needs both of its bounds set together** — see
  [Autoscaling](/operate/control-plane-ha/#autoscaling-set-both-bounds-or-neither).
- Cluster deploy defaults (`deploy.defaults`), unset by default: the registry
  prefix, platforms and base image `dexaflow deploy` uses for whatever a
  repository leaves unset, so no project needs its own `registry:` block, and
  with `sourceMode: true` the source mode runtime image (see
  [Deploying in source mode](/operate/source-mode/#deploying-in-source-mode)),
  the remote builder (`remoteBuilder.cloudBuild`), and, with
  `taskServiceAccount: true` and `warmPools: true`, the task ServiceAccount and
  warm pool settings a deploy reports. See
  [Cluster deploy defaults](/operate/workspaces-and-targets/#cluster-deploy-defaults).
- TLS termination via cert-manager: see [Cluster TLS](/operate/cluster-tls/).
- OIDC/SSO login (`auth.oidc.*`), off by default: turning it on renders the
  ConfigMap the two map settings need and makes `auth.provider: oidc` reachable
  from values, rather than requiring a hand-mounted config file. See
  [SSO with Google Workspace](/operate/sso-google-workspace/) or
  [SSO with another OIDC provider](/operate/sso-other-providers/).
- An in-cluster image builder (`builder.*`), off by default: rootless BuildKit
  in a namespace of its own, with no port, a ServiceAccount with no token, a
  Role that lets `builder.subjects` exec into it and a NetworkPolicy that keeps
  build steps away from the cloud metadata endpoint. Unless
  `builder.advertise` is false, the server advertises it as the cluster's
  remote builder, so `dexaflow deploy` builds there what no daemonless path
  covers. See
  [The in-cluster builder](/operate/builders/#the-in-cluster-builder).

## Migrations and the installation id

The chart migrates the database in a pre-install/pre-upgrade hook Job
(`migrations.enabled`, on by default) that runs the `dexaflow-migrate` image.
The Job also records the release's installation id
([ADR 0090](/project/adrs/0090-database-replaced-under-a-running-release/)):
it writes it into the database and into a ConfigMap,
`<release>-installation`, that it creates and updates itself in the release
namespace, and the control plane reads it from that ConfigMap
(`DEXAFLOW_DATABASE_INSTALLATION_ID`). A database that carries another id,
none, or no schema is then reported as replaced instead of migrated as if it
were a fresh install; see
[a replaced database](/operate/troubleshooting/#a-replaced-database).

- The chart renders no ConfigMap, so `helm template`, Argo CD and Flux installs
  behave like `helm install`, and `helm uninstall` leaves it in place.
- The Job runs as its own ServiceAccount, `<release>-migrate`, with its token
  mounted. With `rbac.create` the chart binds it a Role that may `create`
  ConfigMaps in the release namespace and `get` and `update`
  `<release>-installation`, nothing else. All three are hooks that run before
  the Job and are deleted with it. With `rbac.create: false`, grant the same
  to that account yourself.
- `installation.id` is empty: a first install generates the id, and an upgrade
  from a release before 0.5.6 records one the first time. Set it only to
  accept a replaced database; changing it rolls the control plane pods.

`migrations.onBoot` (default `false`) also lets the control plane apply the
embedded migrations itself at boot, when the schema is absent or behind, and
never when it is ahead or dirty or the database is reported as replaced. Only
the `all` role, and in split mode the scheduler, carries it: the api role
never migrates. The server's `database.url` must then be allowed to run DDL.
golang-migrate's advisory lock serializes it with the Job and with other
replicas. The Job stays the default and keeps the ordering an upgrade needs;
`onBoot` covers what it cannot see, such as `migrations.enabled: false`
installs and a `database.url` repointed between upgrades. With
`migrations.enabled: false` no installation id is recorded, so `onBoot`
migrates an empty database like a fresh one.

## Tuning the probes

### The startup gate

`probes.startup` runs before the other two, and while it is failing Kubernetes
runs **neither** the liveness nor the readiness probe. That is what keeps a slow
boot from being read as an unhealthy process. The pod is still not Ready during
that window, so it stays out of the Service's endpoints: the gate buys boot time,
it does not send traffic anywhere early.

It matters because liveness and readiness both target the API listener, and that
listener binds at the *end* of boot. Without a startup gate the kubelet answers
a boot that is slow or stuck by restarting the container, roughly every minute,
and each cycle is recorded as `Completed exit=0` because the process handles
`SIGTERM` cleanly. An operator triaging that sees a Deployment whose containers
keep finishing successfully, a readiness probe refusing connections, and no cause
anywhere.

```yaml
probes:
  startup:
    enabled: true
    periodSeconds: 5
    failureThreshold: 36
```

The budget is `periodSeconds * failureThreshold`, 180 seconds by default. That
number is sized from the server's own boot bounds rather than picked: the
Postgres connect retry is 30 seconds and runs twice (the request pool and the
dedicated probe pool), the pod-informer warm-up is bounded at 10 seconds, and
OIDC discovery is bounded at 15 seconds. That is 85 seconds of code-defined
budget before anything cloud-shaped, plus headroom for what still carries no
bound of its own (credential detection for an object-store log sink, and
process start), and a gate tighter than the boot would kill a pod that was
about to come up.

It is deliberately generous, because the two directions are not symmetric. A boot
that *fails* returns an error and the process exits 1, so `CrashLoopBackOff`
reports it promptly whatever this budget says. A database schema that is absent
or behind is not such a failure: the process waits for it with `/healthz`
answering 200, so the gate passes and this budget does not bound that wait
([a control plane that is up but not ready](/operate/troubleshooting/#a-control-plane-that-is-up-but-not-ready)).
Once the schema is current the rest of the boot runs under the liveness probe. The gate only bounds a boot that
*hangs*, and restarting a hang is a lottery ticket rather than a fix. The cost of
being too loose is a slower restart of something that was not going to recover
anyway; the cost of being too tight is the restart loop back.

The chart refuses to render a startup budget at or below the liveness budget it
replaces (`initialDelaySeconds + periodSeconds * failureThreshold`, 70 seconds by
default), because such a gate only moves the kill from one probe to the other.
Raise `failureThreshold` if your control plane legitimately takes longer to come
up, for example against a distant database or a node whose cloud metadata server
is not yet serving credentials. Set `enabled: false` to hand the boot back to
liveness deliberately.

The gate probes `/healthz`, not `/readyz`, and that is not an oversight. A
failing startup probe **restarts** the container, exactly like a failing liveness
probe, so gating it on dependency health would turn a database outage or an
in-flight migration into a crash loop. `/healthz` is a static 200 for the same
reason: restarting a pod creates no schema and reaches no database. Nothing is
lost by the narrower gate, because readiness still gates endpoint membership on
`/readyz` the moment the gate opens, so a pod never joins the Service on the
strength of a bound listener alone.

If a pod never passes the gate, or passes it and never turns ready, read
`/readyz` rather than guessing: it names the dependency that is not ready, and
for a schema that is absent, behind or dirty, the reason and what to run. The common causes are a database the pod cannot
reach and a ServiceAccount that cannot `list`/`watch` pods in the task namespace.

### Restarting a stalled scheduler

`/healthz` is a static 200 by default, so a scheduler that holds the leader
lease but whose loop stopped iterating stays live, and no other replica takes
over. To have the kubelet restart such a pod, set
`DEXAFLOW_SERVER_LIVENESS_SCHEDULER_STALL` through `extraEnv`, for example to
`2m`: `/healthz` then answers 503 while this pod leads and its loop has not
started an iteration for that long. The startup gate and the liveness probe
both read it, and a pod that does not lead, or has not ticked since it took
the lease, always passes. A database outage does not trip it, because the
loop keeps iterating and failing fast; `/readyz` reports the database. The
value must be at least twice the scheduler step timeout (`60s` at the default
loop interval), or the server refuses to boot. See
[`server.liveness_scheduler_stall`](/reference/configuration/).

### Timeouts under load

`probes.liveness` and `probes.readiness` are exposed so you can loosen them
under load: a busy scheduler can miss a 1s `/healthz` during a task-pod burst
and be kubelet-killed mid-run, which cascades in-flight tasks to `agent_lost`.
The defaults are deliberately forgiving — 5s liveness and 3s readiness timeouts,
three failures — rather than Kubernetes' 1s/3.

One of those values has a floor the chart enforces:

```console
$ helm upgrade --install dexaflow oci://ghcr.io/dexadata/charts/dexaflow \
    --set probes.readiness.timeoutSeconds=1
Error: probes.readiness.timeoutSeconds=1 is below the 3s floor. [...]
```

`/readyz` checks the database connection *and* asserts the schema is the one
this binary requires, and it bounds that whole check at one second under
`probes.readiness.timeoutSeconds`, so it always answers before the kubelet stops
listening. The chart passes that budget to the server as
`LEOFLOW_SERVER_PROBE_BUDGET` (`server.probe_budget`): 2s with the default
timeout of 3. On a slow metadatabase, such as a cross-region managed Postgres,
raise the timeout and the server gets the same extra room:

```yaml
probes:
  readiness:
    timeoutSeconds: 10  # /readyz may now spend 9s on its checks
```

A timeout under the floor would leave the server less than the 2s it has always
had, or nothing at all: the kubelet cancels a probe that was about to reply, so
instead of a `503` whose log line names the failing dependency, you get a bare
timeout that names nothing, precisely when you most need to know which
dependency is slow. The chart refuses the value rather than installing a cluster
that will go quiet under stress.

If you need Kubernetes to react to an unready pod *faster* than 3s, lower
`probes.readiness.periodSeconds` or `failureThreshold` instead. Both shorten
time-to-unready without cutting off the answer:

```yaml
probes:
  readiness:
    timeoutSeconds: 3   # leave at or above the floor
    periodSeconds: 5    # check twice as often
    failureThreshold: 2 # out of rotation after two misses
```

The readiness check runs on its own dedicated database connection, separate from
the pool serving API traffic, so a saturated control plane does not make every
replica report itself unready at the same moment.

## UI refresh interval

The bundled UI polls DAG, run and task-instance state on a fixed interval.
`ui.autoRefreshIntervalSeconds` sets it; left empty, the chart renders nothing
and the server uses 30 seconds. Every open browser tab polls on its own, so the
load grows with the number of tabs, not of users.

What one refresh costs was measured in
[Experiment 4](https://github.com/dexadata/dexaflow/blob/main/test/load/BASELINE.md#ui-refresh-cycle):
a DAG or run page with a run in progress sends 9 requests and runs about 27
database transactions per cycle, and one api process with Postgres on the same
4 vCPU host sustained about 280 such cycles per second. Read as capacity:

| Interval | Tabs of such a page one host serves | Transactions per second for 50 tabs |
|---|---:|---:|
| 30 s (default) | about 8,400 | about 45 |
| 10 s | about 2,800 | about 135 |
| 5 s | about 1,400 | about 270 |
| 1 s | about 280 | about 1,350 |

Those are ceilings for a host doing nothing else; size against your own DAG
count and leave room for the scheduler and the API traffic that is not the UI.
A team that watches runs progress usually wants 5 or 10 seconds, and a busy
shared database is the reason to stay at 30:

```yaml
ui:
  autoRefreshIntervalSeconds: 10
```

A zero or negative value is not a faster refresh: the server falls back to 30
seconds, so a misconfigured value cannot drop the interval to 0. The default is
due to be revisited in v0.6.0 with an upgrade note
([#1196](https://github.com/dexadata/dexaflow/issues/1196)); a patch release
does not change it.

## Memory limit for the Go runtime

The Go runtime does not read the container memory limit on its own. Without a
soft limit the garbage collector paces only on heap growth, so a burst can push
the control plane past `resources.limits.memory` and get it OOM-killed before a
collection runs. Set `goMemLimit.enabled` and the chart renders `GOMEMLIMIT` as
a share of that limit, so the collector works harder near the ceiling instead:

```yaml
resources:
  limits:
    memory: 512Mi
goMemLimit:
  enabled: true
  percent: 80   # GOMEMLIMIT=409MiB
```

It is off by default, so the rendered environment is unchanged until you turn it
on, and it applies to both Deployments in split mode. The value is computed in
whole MiB from any Kubernetes byte quantity (`512Mi`, `1.5Gi`, `1e9`). The render
fails on quantities that are not bytes, such as the `m` suffix; for those leave
the switch off and set `GOMEMLIMIT` through `extraEnv`. The remaining share is
headroom for memory the container is charged for but the Go runtime does not
track, such as cgo allocations and page cache.

## Related

- [Editions & operating modes](/concepts/editions/): what Cluster gives you and its
  validation status.
- [Deploy your first Cluster DAG](/operate/first-cluster-dag/): the promotion walkthrough.
- [Upgrades](/operate/upgrades/) — upgrading a chart release safely.
- [In-transit encryption](/operate/in-transit-encryption/): a service mesh with
  mTLS, or `httpTLS` for HTTPS on the API listener, and `networkPolicy.grpcFrom`.
- [SSO with Google Workspace](/operate/sso-google-workspace/) or
  [with another OIDC provider](/operate/sso-other-providers/): turning on
  `auth.oidc.*`.
- Cross-listed from the [Reference](/reference/) section for the values surface.
