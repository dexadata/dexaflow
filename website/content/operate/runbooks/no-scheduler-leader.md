---
title: DexaflowNoSchedulerLeader
weight: 20
description: What DexaflowNoSchedulerLeader means and what to do when it fires.
---

**Symptom.** At least one replica serves the scheduler role
(`dexaflow_build_info{role=~"all|scheduler"}`), and no replica ran a leader
tick (`dexaflow_scheduler_loop_duration_seconds_count` did not move) for 5
minutes, held for 5 more.

**Impact.** No run is created, no task is promoted or dispatched, no reaper
runs. Running tasks finish, but nothing follows them.

**First commands.**

```bash
kubectl -n <namespace> get pods -l app.kubernetes.io/name=dexaflow -o wide
kubectl -n <namespace> logs deploy/<release> --tail=200 | grep -iE 'leader|advisory|step'
```

In Postgres, see who holds the scheduler's advisory lock:

```sql
SELECT pid, application_name, client_addr, state, backend_start
FROM pg_stat_activity
WHERE pid IN (SELECT pid FROM pg_locks WHERE locktype = 'advisory' AND granted);
```

**Decide.**

- A session holds the lock but no pod ticks: a replica is wedged or a stale
  connection kept the lock. Restart that pod; if the session belongs to no live
  pod, `pg_terminate_backend(pid)` releases it.
- Nothing holds the lock: every replica fails to take it. The database is
  unreachable or the pods are not ready; see
  [DexaflowSchemaNotReady](../schema-not-ready/).
- `scheduler.enabled: false` on purpose: the rule assumes a scheduler role
  runs. Disable the PrometheusRule or the alert in your routing.

See [Control plane HA](/operate/control-plane-ha/) and ADR 0009.
