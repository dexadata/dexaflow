---
title: DexaflowReaperSettlingValveOpen
weight: 30
description: What DexaflowReaperSettlingValveOpen means and what to do when it fires.
---

**Symptom.** The scheduler decision `reap_settling_valve_open` was recorded:
the leader never settled within twice the settling grace, and the reapers ran
anyway.

**Impact.** Reapers act without the reconciler's sweep behind them, so a task
that is alive can be failed as lost. The cause (reconciler or informer broken)
also stops outcomes from being settled from pods.

**First commands.**

```bash
kubectl -n <namespace> logs deploy/<release> --tail=500 | grep -E 'settling valve|reconcil|informer'
kubectl auth can-i list pods -n <tasks-namespace> --as system:serviceaccount:<namespace>:<release>
```

**Decide.**

- The informer cannot list or watch pods (RBAC, API server errors): fix access;
  the next leader tick settles.
- The reconciler sweep errors on the database: check the scheduler pool
  (`dexaflow_db_pool_*{pool="scheduler"}`).

See [Scheduler resilience](/operate/scheduler-resilience/#observability).
