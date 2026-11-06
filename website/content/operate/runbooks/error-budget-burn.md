---
title: DexaflowErrorBudgetFastBurn and DexaflowErrorBudgetSlowBurn
linkTitle: Error budget burn
weight: 110
description: What the error budget burn alerts mean and what to do when one fires.
---

**Symptom.** One [service level objective](/operate/service-level-objectives/)
spends its 30 day error budget too fast. The `slo` label names it:

- **Fast** (critical): above 14.4 times the allowed error rate over the last
  hour and the last 5 minutes. At this rate the month's budget is gone in two
  days.
- **Slow** (warning): above 6 times over the last 6 hours and the last 30
  minutes; gone in five days.

**Impact.** Depends on the objective: runs created late
(`schedule_punctuality`), tasks starting late (`task_start`), attempts failed
by the platform and retried (`platform_failures`), or API callers and the UI
seeing errors (`api_availability`).

**First commands.**

```promql
dexaflow:slo_errors:ratio_rate5m
dexaflow:slo_errors:ratio_rate1h
1 - avg_over_time(dexaflow:slo_errors:ratio_rate1h[30d]) / on (namespace, slo) (1 - dexaflow:slo_objective)
```

**Decide**, by `slo`:

- `schedule_punctuality`: the leader's tick is slow or stopped. Read
  `dexaflow_scheduler_loop_duration_seconds` and the
  [scheduler step-downs](/operate/runbooks/scheduler-step-downs/) page.
- `task_start`: tasks wait for pods or workers. Follow
  [DexaflowTaskQueueWaitHigh](/operate/runbooks/task-queue-wait-high/).
- `platform_failures`: the reapers fail attempts. Follow
  [DexaflowTasksLost](/operate/runbooks/tasks-lost/); the `decision_type` of
  `dexaflow_scheduler_decisions_total` says which reaper.
- `api_availability`: follow
  [DexaflowAPIErrorRatioHigh](/operate/runbooks/api-error-ratio-high/); the
  `path` label of `dexaflow_http_requests_total` says which endpoint.

A slow burn that a known event explains (a planned database failover, a node
pool upgrade) can wait for working hours; record it so the month's report
accounts for the budget it spent.
