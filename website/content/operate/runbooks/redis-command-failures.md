---
title: DexaflowRedisCommandFailures
weight: 80
description: What DexaflowRedisCommandFailures means and what to do when it fires.
---

**Symptom.** `dexaflow_redis_command_failures_total{reason}` (cancellations
excluded) kept rising for 10 minutes.

**Impact.** XCom pushes and pulls fail, so tasks that exchange values fail; the
live log tail stops. Stored logs and the scheduler are unaffected.

**First commands.**

```promql
sum by (reason) (rate(dexaflow_redis_command_failures_total[5m]))
sum by (reason) (rate(dexaflow_redis_dial_failures_total[5m]))
dexaflow_redis_pool_total_conns
```

```bash
redis-cli -u "$REDIS_URL" INFO memory | grep -E 'used_memory_human|maxmemory_policy'
redis-cli -u "$REDIS_URL" INFO persistence | grep aof_enabled
```

**Decide.**

- `timeout` or `connection_refused`: Redis is down, overloaded or unreachable.
- `auth` or TLS dial failures: a rotated password or certificate.
- Memory at `maxmemory` with `noeviction`: writes are refused. Grow Redis.

Redis needs `maxmemory-policy noeviction` and AOF persistence for XCom values to survive.
