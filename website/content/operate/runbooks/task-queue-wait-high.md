---
title: DexaflowTaskQueueWaitHigh
weight: 50
description: What DexaflowTaskQueueWaitHigh means and what to do when it fires.
---

**Symptom.** The p99 of `dexaflow_task_queue_wait_seconds` (from an attempt's
`queued_at` to its first running report) of one tenant stayed above
`thresholds.queueWaitP99Seconds` (300 s) for 15 minutes.

**Impact.** That tenant's tasks start late; schedules drift and SLAs on run
duration slip.

**First commands.**

```promql
histogram_quantile(0.99, sum by (tenant, le) (rate(dexaflow_task_queue_wait_seconds_bucket[15m])))
dexaflow_task_oldest_waiting_seconds
histogram_quantile(0.99, rate(dexaflow_pod_pending_duration_seconds_bucket[15m]))
dexaflow_dispatch_queue_depth
```

```bash
kubectl -n <tasks-namespace> get pods --field-selector=status.phase=Pending
kubectl -n <tasks-namespace> get events --sort-by=.lastTimestamp | tail -30
```

**Decide.**

- Pods sit `Pending`: the cluster lacks nodes or quota, or an image pulls
  slowly. Scale the node pool or fix the pull.
- `dexaflow_dispatch_queue_depth` grows with `dexaflow_dispatch_at_capacity_total`:
  the dispatch pool is saturated; see [Pools](/operate/pools/).
- Only warm pool tasks wait: see [Warm pools](/operate/warm-pools/).
