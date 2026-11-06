---
title: DexaflowTasksLost
weight: 120
description: What DexaflowTasksLost means and what to do when it fires.
---

**Symptom.** One reaper failed more than `thresholds.lostTasksPerHour` (10)
attempts in an hour as `agent_lost`, `pod_lost`, `dispatch_lost` or
`warm_worker_lost`.

**Impact.** Tasks fail or retry for infrastructure reasons, not their code.

**First commands.**

```promql
sum by (decision_type) (increase(dexaflow_scheduler_decisions_total{decision_type=~".*_lost.*"}[1h]))
rate(dexaflow_agent_token_rejections_total[1h])
```

```bash
kubectl -n <tasks-namespace> get events --sort-by=.lastTimestamp | grep -iE 'evict|oom|preempt' | tail -20
```

**Decide.**

- `agent_lost` with token rejections: a refused agent credential, see
  [agent_lost that is not a network fault](/operate/troubleshooting/#agent_lost-that-is-not-a-network-fault-a-refused-agent-credential).
- `pod_lost` with evictions or preemptions: node pressure or spot reclaim.
- `dispatch_lost`: pods never started; see
  [DexaflowTaskQueueWaitHigh](../task-queue-wait-high/).
- `warm_worker_lost_oomkilled` rising: raise the task's memory limit.
