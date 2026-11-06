---
title: DexaflowDatabasePoolCanceledAcquires
weight: 70
description: What DexaflowDatabasePoolCanceledAcquires means and what to do when it fires.
---

**Symptom.** `dexaflow_db_pool_canceled_acquires_total{pool}` kept rising for
10 minutes: callers gave up waiting for a connection of that pool.

**Impact.** Requests fail (pool `api`) or scheduler work stalls (pool
`scheduler`).

**First commands.**

```promql
dexaflow_db_pool_acquired_conns / dexaflow_db_pool_max_conns
rate(dexaflow_db_pool_empty_acquire_wait_seconds_total[5m])
```

```sql
SELECT state, wait_event_type, count(*) FROM pg_stat_activity GROUP BY 1, 2 ORDER BY 3 DESC;
SELECT pid, now() - query_start AS age, left(query, 120) FROM pg_stat_activity
WHERE state <> 'idle' ORDER BY age DESC LIMIT 10;
```

**Decide.**

- The pool is full of long queries: find them above and the request that sends
  them; slow queries usually mean missing `ANALYZE` after a large backfill or a
  table that needs retention.
- The pool is full of short ones: raise `database.max_open_conns` (or
  `database.scheduler_max_conns`) within what Postgres allows.
