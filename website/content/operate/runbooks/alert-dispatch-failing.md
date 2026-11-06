---
title: DexaflowAlertDispatchFailing
weight: 140
description: What DexaflowAlertDispatchFailing means and what to do when it fires.
---

**Symptom.** `dexaflow_alerts_dispatched_total{result="failed"}` rose: an
on-failure alert of a DAG did not reach its channel.

**Impact.** Task failures happen without the people who own the DAG hearing
about them.

**First commands.**

```promql
sum by (type, dag_id) (increase(dexaflow_alerts_dispatched_total{result="failed"}[1h]))
```

```bash
kubectl -n <namespace> logs deploy/<release> | grep -i 'alert' | tail -20
```

**Decide.** A revoked webhook or token, an SMTP change, or an egress rule that
blocks the destination. Fix the connection the DAG's alert uses.
