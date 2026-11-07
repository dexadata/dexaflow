---
title: DexaflowRedisEvictionPolicyUnsafe
weight: 175
description: What DexaflowRedisEvictionPolicyUnsafe means and what to do when it fires.
---

**Symptom.** `dexaflow_redis_eviction_policy_safe` has been 0 for 15 minutes:
the control plane read a `maxmemory_policy` other than `noeviction` from
`INFO memory`. The boot log carries the same warning with the policy it found.

**Impact.** Nothing fails yet. When Redis memory fills, the server deletes
keys instead of refusing writes, and XCom values of running DAGs, which carry a
TTL, go first under a `volatile-*` policy (the default of several managed
services). Their downstream tasks then fail with "not found".

**First commands.**

```bash
redis-cli -u "$REDIS_URL" INFO memory | grep -E 'maxmemory_human|maxmemory_policy'
```

**Decide.**

- Set `maxmemory-policy noeviction`: `CONFIG SET maxmemory-policy noeviction`
  on a self-hosted Redis (and in `redis.conf`), or the provider's parameter
  group or configuration on a managed one. The gauge returns to 1 within 30
  seconds.
- To make the chart refuse to start on an unsafe policy, set
  `redis.requireNoEviction: true` once the policy is fixed.

With `noeviction`, a full Redis refuses the write and the pushing task fails
with the reason. Size `maxmemory` as described in
[Redis requirements](/operate/redis/).
