---
title: Production profile
linkTitle: Production profile
weight: 31
description: "The one Cluster configuration the project recommends for production until 1.0, the configuration every reliability test, alert and upgrade check runs on. Anything else is best effort."
---

The operate pages describe many valid configurations. Only one of them is the
configuration the project recommends for production, and it is the one every
reliability test, alert rule and upgrade check of a release runs on: a test
matrix cannot cover an open set of combinations, it can cover one profile.

Until 1.0, the **production profile** is the configuration below. A Cluster
install that departs from it is supported on a best-effort basis: bugs are
fixed, but nothing in the release checklist proves that combination before a
release ships.

## The profile

| Area | Production profile | Why, and where the detail is |
|---|---|---|
| Edition and topology | Cluster edition, non-split, `replicaCount: 2`, from [`values-ha.yaml`](https://github.com/dexadata/dexaflow/blob/main/helm/dexaflow/examples/values-ha.yaml) | A leader-elected scheduler with a warm standby and an active-active API. Split mode needs `split.scheduler.replicaCount: 2` for the same failover ([control-plane HA](/operate/control-plane-ha/#split-mode-with-a-scheduler-standby)). |
| Placement | Replicas spread across nodes, and across zones on a multi-zone cluster | Two replicas on one node or in one zone are one replica ([control-plane HA](/operate/control-plane-ha/#two-replicas-on-one-node-are-one-replica)). |
| Postgres | Managed Postgres 16 with high availability and point-in-time recovery | The only durable state of the control plane. Direct connections, or PgBouncer in **session** mode, for every process that runs the scheduler: the leader election holds a session advisory lock ([connection budget](/operate/control-plane-ha/#postgres-connection-budget)). |
| Redis | Managed Redis with persistence, `maxmemory-policy noeviction`, reached through the provider's failover endpoint | Holds XCom values of running DAGs and carries the live log tail ([Redis requirements](/operate/redis/)). |
| Task logs | Object storage sink (`logs.sink.provider: s3` or `gcs`), no log PVC | A `ReadWriteOnce` log volume cannot follow a second replica ([control-plane HA](/operate/control-plane-ha/#the-storage-precondition)). |
| Task execution | Dedicated task pods | Warm pools (`execution.warmPoolsEnabled`) are **experimental** and outside the profile ([warm pools](/operate/warm-pools/)). |
| Retention | On, with the [starting configuration](/operate/data-retention/#a-starting-configuration) | Without it `dag_runs`, `task_instances` and `audit_log` grow without bound. |
| Monitoring | `metrics.serviceMonitor.enabled` and `metrics.prometheusRule.enabled` | The rules the chart ships page on the states that stop work silently, such as a schema that is not ready ([helm chart](/operate/helm-chart/)). The project ships no dashboard yet. |
| Versions | A pinned chart version, whose `appVersion` pins the images, upgraded along the [supported path](/operate/upgrades/) | A minor is upgraded from the latest patch of the previous minor ([versioning](/project/versioning/#upgrade-path)). |
| Backups | The RPO and RTO targets of [Cluster backup](/operate/backup-restore/#cluster-helm) | Postgres, Redis, the log bucket and the secrets are separate state, each with its own loss. |

## Install it

Start from the HA overlay, then set the datastores, secrets and bucket it marks
`CHANGEME`:

```bash
helm upgrade --install dexaflow oci://ghcr.io/dexadata/charts/dexaflow \
  --version <x.y.z> -n dexaflow -f values-ha.yaml -f values-prod.yaml
```

with a `values-prod.yaml` that turns on what the overlay leaves to you:

```yaml
metrics:
  serviceMonitor:
    enabled: true
  prometheusRule:
    enabled: true
```

and the [retention settings](/operate/data-retention/#turn-it-on) through
`config` or `extraEnv`. Leave `image.tag` and `migrations.image.tag` empty:
they then follow the chart's `appVersion`, so the pinned chart version also
pins the images, and the release signature covers them
([verify a release](/operate/helm-chart/#install-from-the-published-oci-chart)).

## What best effort means

Outside the profile, for example a single replica, split mode, a log PVC,
warm pools, Redis Sentinel or Redis Cluster, or a transaction-mode pooler in
front of the scheduler:

- the configuration is documented and expected to work, and a bug in it is a
  bug;
- no reliability row of the
  [release checklist](https://github.com/dexadata/dexaflow/blob/main/test/release/perf-reliability-checklist.md)
  and no row of the
  [RC cluster validation](https://github.com/dexadata/dexaflow/blob/main/test/release/rc-cluster-validation.md)
  runs on it, so a regression there can reach a release;
- some combinations are refused at render time instead, with the reason, such
  as two replicas on a `ReadWriteOnce` log volume.

The profile changes only with a release, and the changelog names the change.

## Related

- [Editions](/concepts/editions/#dexaflow-cluster-chart-installable)
- [Helm chart](/operate/helm-chart/)
- [Control-plane HA](/operate/control-plane-ha/)
- [Redis requirements](/operate/redis/)
- [Backup and restore](/operate/backup-restore/)
