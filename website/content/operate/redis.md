---
title: Redis requirements
linkTitle: Redis requirements
weight: 33
description: "What a Cluster install keeps in Redis, the settings and topologies the production profile requires, how to size it, and what a flush or a lost instance costs."
---

A Cluster control plane needs an external Redis (`redis.url`, or a Sentinel
or Redis Cluster, see [Supported topologies](#supported-topologies)). Lite does not:
it keeps XCom in Postgres and tails logs in process
([ADR 0026](/project/adrs/0026-lite-datastore-no-redis/)).

## What Redis holds

| Data | Written by | Lifetime | Lost on a flush |
|---|---|---|---|
| **XCom values** | the agent, through the control plane, when a task returns a value or pushes one | 7 days from the push (a TTL on every key, [ADR 0006](/project/adrs/0006-xcom-redis/)); the Postgres index row of each value is kept with its run | Yes. A downstream task of a run in flight that pulls a lost value fails. Finished runs keep their state, but their XCom values are gone from the UI and the API. |
| **Live log tail** | the control plane, one publish per log line | none: Redis pub/sub keeps nothing | No data: a follower watching a log reconnects and the full log stays in the log sink. |

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

- **Redis Sentinel** (since 0.6.0, [#2358](https://github.com/dexadata/dexaflow/issues/2358)):
  set `redis.sentinel.master` and `redis.sentinel.addrs` (Helm the same
  names). The client asks the sentinels for the master and follows a
  failover to the new one. `redis.url`, when set, only gives the master's
  password, the database and TLS; `redis.sentinel.existingSecret` (key
  `sentinelPassword`) holds the sentinels' own password when they have one.
- **Redis Cluster** (since 0.6.0): list seed nodes in `redis.cluster.addrs`;
  one configuration endpoint (ElastiCache cluster mode) is enough. The client
  discovers the other nodes and follows slot moves. A Cluster has database 0
  only. XCom reads several keys at once with one pipelined batch per node
  instead of an `MGET`, which a Cluster refuses across hash slots, so the keys
  keep their format and values pushed before a move to a Cluster stay
  readable.

A failover is ridden through: the errors of its window (a refused
connection, `READONLY` from a demoted master, `MASTERDOWN`) are the transient
kind the agent retries, so a task pushing or pulling XCom during it waits
instead of failing. The integration test of #2358 fails a Sentinel pair over
while tasks push and fetch.

### Client timeouts and pool

| Setting | Default | When to change it |
|---|---|---|
| `redis.dial_timeout` (Helm `redis.dialTimeout`) | 5 s | Lower it (for example `2s`) so a reconnect after a failover gives up on a dead node sooner. |
| `redis.read_timeout` (Helm `redis.readTimeout`) | 3 s | Lower it so a read on a dead master fails, and is retried on the new one, sooner. Keep it above the slowest XCom read, a 256 KB value over your network. |
| `redis.write_timeout` (Helm `redis.writeTimeout`) | the read timeout | Rarely; a write is bounded like a read. |
| `redis.pool_size` (Helm `redis.poolSize`) | 10 per CPU, per node | Raise it when `dexaflow_redis_pool_total_conns` sits at the pool size under load. |

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
