---
title: DexaflowSchedulerStepDowns
weight: 110
description: What DexaflowSchedulerStepDowns means and what to do when it fires.
---

**Symptom.** More than `thresholds.stepDownsPerHour` (3) leader step-downs in
one hour, not counting `reason="shutdown"` (rollouts).

**Impact.** Every step-down pauses scheduling and restarts the settling grace,
during which no reaper acts. Frequent ones stretch task start times.

**First commands.**

```promql
sum by (reason) (increase(dexaflow_scheduler_step_downs_total[1h]))
histogram_quantile(0.99, rate(dexaflow_scheduler_reacquire_seconds_bucket[1h]))
```

**Decide.** `check_timeout` and `lock_released` mean the leader lost its
Postgres session or could not confirm it in time: look at network stability to
the database, failovers of a managed Postgres, and connection limits.
