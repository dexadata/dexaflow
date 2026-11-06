---
title: DexaflowOldestQueuedTask
weight: 160
description: What DexaflowOldestQueuedTask means and what to do when it fires.
---

**Symptom.** `dexaflow_task_oldest_waiting_seconds{state="queued"}` of a tenant
stayed above `thresholds.oldestQueuedSeconds` (600 s) for 5 minutes. The gauge
is set by the scheduler leader every 15 seconds.

**Impact.** At least one task was handed to the executor and has no running
agent yet. One stuck task may not move the p99 of
[DexaflowTaskQueueWaitHigh](../task-queue-wait-high/); this catches it.

**First commands.**

```sql
SELECT t.name AS tenant, d.dag_id, ti.task_id, ti.queued_at, ti.try_number
FROM task_instances ti
JOIN tenants t ON t.id = ti.tenant_id
JOIN dag_runs r ON r.id = ti.dag_run_id
JOIN dags d ON d.id = r.dag_id
WHERE ti.state = 'queued' ORDER BY ti.queued_at LIMIT 10;
```

**Decide.** Find the attempt's pod (`kubectl get pods -l` with the task's
labels) and read why it does not start. The dispatch-lost reaper fails attempts
whose pod never appears; one that waits on a slow image or a full node is not
failed.

`state="scheduled"` (a task waiting for a pool slot) is exported too and not
alerted on: that wait is the pool size working as configured.
