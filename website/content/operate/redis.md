---
title: Redis requirements
linkTitle: Redis requirements
weight: 33
description: "What a Cluster install keeps in Redis, the settings and topologies the production profile requires, how to size it, and what a flush or a lost instance costs."
---

A Cluster control plane needs an external Redis (`redis.url`). Lite does not:
it keeps XCom in Postgres and tails logs in process
([ADR 0026](/project/adrs/0026-lite-datastore-no-redis/)).

## What Redis holds

| Data | Written by | Lifetime | Lost on a flush |
|---|---|---|---|
| **XCom values** | the agent, through the control plane, when a task returns a value or pushes one | 7 days from the push (a TTL on every key, [ADR 0006](/project/adrs/0006-xcom-redis/)); the Postgres index row of each value is kept with its run | Yes. A downstream task of a run in flight that pulls a lost value fails. Finished runs keep their state, but their XCom values are gone from the UI and the API. |
| **Live log tail** | the control plane, one publish per log line | none: Redis pub/sub keeps nothing | No data: a follower watching a log reconnects and the full log stays in the log sink. |
| **Sign-in rate limits** | every API replica, one increment per counted attempt | the current one-minute window | The counts restart at zero: an address gets a fresh minute of attempts. If Redis is unreachable, each replica counts on its own until it is back. |

The rate limits of the sign-in routes (failed password logins, token
renewal, SSO, CLI device sign-in, CI token exchange) count in Redis so that
the limit holds across API replicas and the HPA; without Redis each replica
would allow the full limit on its own.

Nothing takes a lock in Redis. Scheduling, leader election and dispatch state
live in Postgres only, so a Redis outage never runs a task twice or loses a
run: it fails the XCom calls made during the outage.

## Required settings

| Setting | Value | Why |
|---|---|---|
| Version | Redis 7 (tested in CI); 6.0 or later | The [datastore compatibility matrix](https://github.com/dexadata/dexaflow/blob/main/helm/dexaflow/README.md#datastore-compatibility) of the chart. |
| `maxmemory-policy` | `noeviction` | Every XCom key carries a TTL, so the `volatile-*` policies, the default of several managed services, evict exactly the XCom values of running DAGs first when memory fills, and the downstream task fails with "not found" (#2354). With `noeviction` a full Redis refuses the write and the pushing task fails with the reason instead. |
| Persistence | AOF (`appendfsync everysec`) or the provider's replication with persistence | A restart without persistence is a flush (see the table above). AOF loses at most about a second of writes; RDB snapshots lose everything since the last snapshot, which can be minutes of XCom. |
| TLS | `rediss://` URL; `redis.caConfigMap` when the provider signs with its own CA | Memorystore, ElastiCache in-transit encryption and Azure Cache sign with a provider CA ([verified TLS](https://github.com/dexadata/dexaflow/blob/main/helm/dexaflow/README.md#verified-tls-to-managed-redis-312)). |
| Credentials | in `redis.existingSecret` (key `redisUrl`) | The URL carries the password. |

## Supported topologies

- **A single primary** (self-hosted, with persistence).
- **A managed primary endpoint with automatic failover**: ElastiCache
  (replication group primary endpoint), Memorystore (Standard tier), Azure
  Cache for Redis (Standard or Premium). The client reconnects to the same
  name after a failover.

**Not supported:** Redis Sentinel and Redis Cluster (sharded) endpoints. The
client speaks to one endpoint and does not follow a Sentinel or a cluster
redirect; support is tracked in #2358.

## Sizing

XCom is the only data that occupies memory. Size for the values alive inside
the TTL:

```text
memory ≈ XCom pushes per day × average value size × 1.33 × 7
```

The `1.33` covers the key, the envelope and Redis overhead, and `7` is the TTL
in days. A single value is capped at 256 KB. For example, 200,000 pushes a day
of 2 KB each need about 3.7 GB; set `maxmemory` with headroom above that, since
`noeviction` turns a full instance into failed pushes.

## What a loss costs

- **A restart with persistence on** keeps the XCom values written before the
  last fsync.
- **A failover** to a replica keeps what the replica had received; the writes
  of the last moments before it may be lost, as with any asynchronous
  replication.
- **A flush, or an instance replaced without its data**, loses every XCom
  value: runs in flight whose next task pulls one fail, and finished runs show
  no XCom.

The measured outcome of each case on a running DAG, the reason the UI shows
and the recovery step are recorded by the Redis failure injection runs
(#2307), and this section will carry them once they ran.

## Monitoring

Every role exports `dexaflow_redis_command_failures_total`,
`dexaflow_redis_dial_failures_total`, `dexaflow_redis_dial_duration_seconds`
and the pool gauges (`dexaflow_redis_pool_*`), listed in the
[metrics reference](/reference/metrics/). Alert on a rising failure count, and
on the provider's memory, eviction and failover metrics as well:
`evicted_keys` above zero means the policy is not `noeviction`.

## Related

- [Production profile](/operate/production-profile/)
- [Helm chart](/operate/helm-chart/)
- [Control-plane HA](/operate/control-plane-ha/)
- [Backup and restore](/operate/backup-restore/#cluster-helm)
