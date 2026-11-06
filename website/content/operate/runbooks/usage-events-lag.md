---
title: DexaflowUsageEventsLag
weight: 150
description: What DexaflowUsageEventsLag means and what to do when it fires.
---

**Symptom.** `dexaflow_usage_events_lag_seconds` stayed above
`thresholds.usageEventsLagSeconds` (900 s) for 15 minutes: the service API
cannot serve usage events past an open transaction.

**Impact.** Billing and usage readers see no new events; nothing is lost, the
events wait.

**First commands.**

```sql
SELECT pid, now() - xact_start AS open_for, state, left(query, 120)
FROM pg_stat_activity WHERE xact_start IS NOT NULL ORDER BY xact_start LIMIT 5;
```

**Decide.** A long open transaction (often `idle in transaction`) holds the
horizon. End the client that left it open, or `pg_terminate_backend(pid)`.
See [Usage events](/operate/usage-events/).
