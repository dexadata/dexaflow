---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /helm-chart.html
# --- end AUTO redirect aliases ---
title: Helm chart
linkTitle: Helm chart
weight: 30
description: Install and configure the Dexaflow Pro control plane on Kubernetes with the official Helm chart.
---

The **Pro** control plane installs on Kubernetes via the official Helm chart. The
chart is chart-test gated, publishes multi-arch images per release, and signs them
with cosign. It runs on any cluster with an external Postgres + Redis.

## Install from the published OCI chart

Every release tag publishes the chart as a **cosign-signed OCI artifact** to
`oci://ghcr.io/dexadata/charts/dexaflow` ([ADR 0028](/project/adrs/0028-release-versioning-two-editions/)),
co-versioned with the release tag — so you can install a pinned version without
cloning the repo:

```bash
helm install dexaflow oci://ghcr.io/dexadata/charts/dexaflow --version <x.y.z> \
  -n leoflow --create-namespace \
  -f values.yaml
```

Pass the release tag **without** the leading `v` (tag `v0.4.0` → `--version 0.4.0`):
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
- TLS termination via cert-manager — see [Pro TLS](/operate/pro-tls/).
- OIDC/SSO login (`auth.oidc.*`), off by default: turning it on renders the
  ConfigMap the two map settings need and makes `auth.provider: oidc` reachable
  from values, rather than requiring a hand-mounted config file. See
  [SSO with Google Workspace](/operate/sso-google-workspace/) or
  [SSO with another OIDC provider](/operate/sso-other-providers/).

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
reports it promptly whatever this budget says. The gate only bounds a boot that
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

If a pod never passes the gate, read `/readyz` rather than guessing: it names the
dependency that is not ready. The common causes are a database the pod cannot
reach and a ServiceAccount that cannot `list`/`watch` pods in the task namespace.

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
this binary requires, and it bounds that whole check at 2s so it always answers
before the kubelet stops listening. Setting the kubelet's timeout below that
inverts the relationship: the kubelet cancels a probe that was about to reply,
so instead of a `503` whose log line names the failing dependency, you get a
bare timeout that names nothing — precisely when you most need to know which
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

- [Editions & operating modes](/concepts/editions/) — what Pro gives you and its
  validation status.
- [Deploy your first Pro DAG](/operate/first-pro-dag/) — the promotion walkthrough.
- [Upgrades](/operate/upgrades/) — upgrading a chart release safely.
- [SSO with Google Workspace](/operate/sso-google-workspace/) or
  [with another OIDC provider](/operate/sso-other-providers/): turning on
  `auth.oidc.*`.
- Cross-listed from the [Reference](/reference/) section for the values surface.
