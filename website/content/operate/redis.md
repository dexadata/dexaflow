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
| `maxmemory-policy` | `noeviction` | Every XCom key carries a TTL, so the `volatile-*` policies, the default of several managed services, evict exactly the XCom values of running DAGs first when memory fills, and the downstream task fails with "not found" (#2354). With `noeviction` a full Redis refuses the write and the pushing task fails with the reason instead. Dexaflow reads the policy from `INFO memory` at boot and every 30 seconds: anything else logs a warning, and `redis.require_noeviction` (Helm `redis.requireNoEviction`) refuses to boot instead. |
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

The [Redis failure injection run](https://github.com/dexadata/dexaflow/blob/main/test/e2e/redis-faults.sh)
(`make redis-faults`, weekly in CI as "E2E Redis faults") measures each case on
a running DAG: `produce` pushes an XCom value, `hold` runs while the fault
lands, and `consume` pulls the value after it. No task retries, so a lost value
shows as a failure. First run, 2026-11-25:

| Case | Setup in the run | What the run did | Control plane |
|---|---|---|---|
| **Restart with persistence on** | AOF, `appendfsync everysec`, restarted after one fsync period | The run succeeded: `consume` read the value written before the restart. Redis was back in about 1 s. | 0 failed Redis commands |
| **Failover to a replica** | Replica caught up, primary stopped, replica promoted behind the same endpoint | The run succeeded. The endpoint was down about 1 s. | 2 failed Redis commands, no restart needed |
| **Flush, or an instance replaced without its data** | `FLUSHALL` while `hold` runs | `produce` stays success; `consume` failed ("task exited non-zero") and the run failed, settled 52 s after the flush. The API shows no XCom value for `produce`. The next run succeeded, 57 s after the flush. | No restart needed |

What follows from it:

- With persistence on, a restart costs nothing a DAG can see; without it, a
  restart is the flush row.
- A failover keeps what the replica had received. The run waited until the
  replica's replication offset matched the primary's before the switch; with
  asynchronous replication, writes of the last moments before an unplanned
  failover can still be lost, and then the failover behaves like a flush for
  those values.
- After a flush, the runs whose next task pulls a lost value fail, and only
  those. Recovery is to clear the run from its producing task (the producer
  pushes the value again) or to trigger a new run; nothing in the control
  plane needs a restart. Finished runs show no XCom.

## Monitoring

Every role exports `dexaflow_redis_command_failures_total`,
`dexaflow_redis_dial_failures_total`, `dexaflow_redis_dial_duration_seconds`
and the pool gauges (`dexaflow_redis_pool_*`), listed in the
[metrics reference](/reference/metrics/). The control plane also reads `INFO`
every 30 seconds into `dexaflow_redis_eviction_policy_safe` (1 for
`noeviction`), `dexaflow_redis_used_memory_bytes` and
`dexaflow_redis_evicted_keys`. The chart's alerts page on evictions
([DexaflowRedisEvictingKeys](/operate/runbooks/redis-evicting-keys/)) and open
a ticket for an unsafe policy
([DexaflowRedisEvictionPolicyUnsafe](/operate/runbooks/redis-eviction-policy-unsafe/)).
Watch the provider's failover metrics as well.

## Related

- [Production profile](/operate/production-profile/)
- [Helm chart](/operate/helm-chart/)
- [Control-plane HA](/operate/control-plane-ha/)
- [Backup and restore](/operate/backup-restore/#cluster-helm)
