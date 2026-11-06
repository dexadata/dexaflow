---
title: DexaflowAPIErrorRatioHigh
weight: 60
description: What DexaflowAPIErrorRatioHigh means and what to do when it fires.
---

**Symptom.** More than `thresholds.apiErrorRatio` (1%) of API requests were
answered with a 5xx for 10 minutes.

**Impact.** The UI, the CLI and API clients fail; deploys and triggers may not
land.

**First commands.**

```promql
sum by (path, status) (rate(dexaflow_http_requests_total{status=~"5.."}[5m]))
```

```bash
kubectl -n <namespace> logs deploy/<release> -c leoflow-server --tail=500 | jq 'select(.status >= 500) | {path, status, cause}'
```

**Decide.**

- One path dominates: read its `cause` field; a database error points at
  [DexaflowDatabasePoolCanceledAcquires](../database-pool-canceled-acquires/),
  a Redis error at [DexaflowRedisCommandFailures](../redis-command-failures/).
- 503 on every path while pods restart: see
  [Ingress errors while no server pod is ready](/operate/troubleshooting/#ingress-errors-while-no-server-pod-is-ready-502-503-504).
