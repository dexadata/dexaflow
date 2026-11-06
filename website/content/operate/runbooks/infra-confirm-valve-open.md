---
title: DexaflowInfraConfirmValveOpen
weight: 40
description: What DexaflowInfraConfirmValveOpen means and what to do when it fires.
---

**Symptom.** The scheduler decision `infra_confirm_valve_open` was recorded:
the planner acted on a provisional infra mark (`agent_lost`, `pod_lost`,
`dispatch_lost`) the reconciler did not confirm within 2 minutes.

**Impact.** Retries and failures are decided on marks nothing verified. A task
that actually succeeded may be retried or failed.

**First commands.**

```bash
kubectl -n <namespace> logs deploy/<release> --tail=500 | grep -E 'infra|reconcil'
```

**Decide.** The reconciler is not sweeping or cannot read pods: the same
causes as [DexaflowReaperSettlingValveOpen](../reaper-settling-valve-open/).
Compare with `dexaflow_reconcile_infra_override_total`, which counts the
successes the reconciler settled over such a mark later.

See [Scheduler resilience](/operate/scheduler-resilience/) and ADR 0052.
