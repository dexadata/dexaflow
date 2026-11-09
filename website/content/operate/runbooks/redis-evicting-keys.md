---
title: DexaflowRedisEvictingKeys
weight: 85
description: What DexaflowRedisEvictingKeys means and what to do when it fires.
---

**Symptom.** `dexaflow_redis_evicted_keys`, the server's `evicted_keys` read
from `INFO stats`, grew in the last 10 minutes.

**Impact.** Redis deleted keys to stay under `maxmemory`. XCom values carry a
TTL, so under a `volatile-*` policy they are the first to go: a task that
pulls a value its upstream pushed fails with "not found", which reads as a
user error. Stored logs, the scheduler and the run history in Postgres are
unaffected.

**First commands.**

```promql
dexaflow_redis_eviction_policy_safe
delta(dexaflow_redis_evicted_keys[10m])
dexaflow_redis_used_memory_bytes
```

```bash
redis-cli -u "$REDIS_URL" INFO memory | grep -E 'used_memory_human|maxmemory_human|maxmemory_policy'
redis-cli -u "$REDIS_URL" INFO stats | grep evicted_keys
```

**Decide.**

- The policy is not `noeviction`: set `maxmemory-policy noeviction` (on a
  managed service, in its parameter group or configuration), then grow the
  instance if memory stays near `maxmemory`. See
  [DexaflowRedisEvictionPolicyUnsafe](../redis-eviction-policy-unsafe/).
- Clear the failed tasks of the affected runs once memory has headroom, so
  their upstream tasks push the values again.

Sizing and the required settings are in [Redis requirements](/operate/redis/).
