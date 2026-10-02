---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /frontend-assessment.html
# --- end AUTO redirect aliases ---
title: Frontend assessment
weight: 30
description: "Background: assessment of the frontend."
---

> **Status:** in-progress baseline. Living doc.
> **Goal:** every SPA view mapped to the routes it hits, with observed
> latency and a yes/no on each known bug. Drives the fix priority order.

## What the SPA expects from us

The embedded SPA (ADR 0017) hits two surface areas:

- `/api/v2/*` — the Airflow 3.2 OpenAPI surface (`gh airflow-api-spec`).
  54 routes registered.
- `/ui/*` — helper endpoints the SPA assumes exist for things the public
  OpenAPI doesn't cover (grid summaries, dashboard tiles, auth/me, etc.).
  20 routes registered. Subset that ships data:
  - `/ui/dags` — list with latest-run + favorite flag baked in
  - `/ui/dags/:dag_id/latest_run`
  - `/ui/grid/runs/:dag_id` — paginated runs for the grid
  - `/ui/grid/ti_summaries/:dag_id` — TI rollups per cell
  - `/ui/grid/structure/:dag_id` — DAG structure as the grid needs it
  - `/ui/structure/structure_data` — alternative structure shape
  - `/ui/dashboard/dag_stats` — pie counts
  - `/ui/dashboard/historical_metrics_data` — sparkline buckets
  - `/ui/calendar/:dag_id` — calendar (empty stub)
  - `/ui/dependencies`, `/ui/backfills`, `/ui/teams` — empty stubs
  - `/ui/connections/hook_meta` — connection type metadata
  - `/ui/next_run_assets/:dag_id`
  - `/ui/config`, `/ui/auth/me`, `/ui/auth/menus`, `/ui/auth/token`

## Phase 0 baseline — server latency is NOT the bottleneck

Measured median over 10 GETs on local Mac (managed PG on Unix socket,
an early development build, idle workspace = 1 DAG):

```
    518 μs  /ui/dags                     (DAGs list)
    450 μs  /ui/grid/runs/:dag_id        (grid runs)
    420 μs  /api/v2/dags                 (dags list v2)
    403 μs  /api/v2/dags/:dag_id/details
    403 μs  /api/v2/dags/:dag_id/dagRuns
    399 μs  /api/v2/eventLogs            (audit log)
    391 μs  /ui/dashboard/dag_stats
    386 μs  /api/v2/connections
    372 μs  /ui/grid/structure/:dag_id
    331 μs  /api/v2/variables
    291 μs  /api/v2/importErrors
    277 μs  /api/v2/monitor/health
    261 μs  /api/v2/monitor/executor
    248 μs  /ui/dashboard/historical_metrics
    246 μs  /api/v2/jobs
    228 μs  /ui/grid/ti_summaries/:dag_id
```

**Every endpoint responds in 0.2–0.5 ms.** A grid view loading 6 endpoints
sequentially is still ~2–3 ms total. Perceived slowness is not server
work — it is the client layer:

1. **TanStack Query `staleTime`** — the SPA serves the cache without
   re-fetching for the configured window (default seconds; configurable
   via `/ui/config`'s `auto_refresh_interval` since #247). Manual triggers
   sit invisible until the next poll.
2. **DagRun-state aggregation lag** — a TI finishes at the agent, but the
   DagRun row's `state`/`end_date` updates one scheduler-tick later. The
   SPA reads DagRun, not aggregated TI, so it can render "running" for
   the tick interval after the last TI succeeded.
3. **Lima LAN latency** — over Wi-Fi to the m900 box, each call adds
   10–50 ms RTT. Multiplied by the waterfall it adds up.
4. **Polling rather than push** — Airflow SPA was designed for pull;
   we have no SSE/WebSocket channel for "DAG state changed" events.

Implication: the fix surface is **response headers + post-mutation
payload + push channel**, not query optimisation. ADR-0017 compatible.

## View → routes matrix (provisional)

Filled from /tmp/leoflow-lite.log captures and code inspection. Latency
column is local Mac (managed-PG, an early development build) and will be
re-measured in Phase 0.

| View | Routes hit | Latency (local) | Known bugs |
|---|---|---|---|
| **Dashboard** (`/`) | `/ui/dashboard/dag_stats`, `/ui/dashboard/historical_metrics_data`, `/api/v2/assets/events` | TBD | — |
| **DAGs list** (`/dags`) | `/ui/dags`, `/ui/grid/ti_summaries/:dag_id` (per row), `/api/v2/dagTags`, `/api/v2/dagWarnings`, `/api/v2/importErrors` | TBD | #212 freezes |
| **DAG overview** (`/dags/:dag_id`) | `/api/v2/dags/:dag_id/details`, `/ui/dags/:dag_id/latest_run`, `/api/v2/dags/:dag_id/dagRuns` | TBD | — |
| **Grid view** (`/dags/:dag_id`) | `/ui/grid/runs/:dag_id`, `/ui/grid/ti_summaries/:dag_id`, `/ui/grid/structure/:dag_id` | TBD | #212 freezes, #271 staleTime, #209 slow on scheduled runs |
| **Run detail** | `/api/v2/dags/:dag_id/dagRuns/:dag_run_id`, `/api/v2/dags/.../taskInstances`, `/api/v2/dags/.../hitlDetails` | TBD | — |
| **Task instance** | `/api/v2/dags/.../taskInstances/:task_id`, `/api/v2/dags/.../taskInstances/:task_id/logs/N`, `/api/v2/dags/.../taskInstances/:task_id/tries` | TBD | #213 empty dagRunId → 404, #216 empty logs (symptom of #213), #214 copy-logs (LAN clipboard), #215 React #520, #211 mark-state |
| **Code tab** | `/api/v2/dagSources/:dag_id` (or `/api/v2/dags/:dag_id/dagVersions/:version_number`) | TBD | — |
| **Audit Log (DAG)** | `/api/v2/eventLogs?dag_id=...` | TBD | — |
| **Audit Log (global)** | `/api/v2/eventLogs` | TBD | — |
| **Connections** | `/api/v2/connections`, `/ui/connections/hook_meta` | TBD | — |
| **Variables** | `/api/v2/variables` | TBD | — |
| **Providers** | `/api/v2/providers` | TBD | — |
| **Pools** | `/api/v2/pools` | TBD | — |
| **Assets** | `/api/v2/assets`, `/api/v2/assets/events` | TBD | — |
| **Config** | `/api/v2/config` | TBD | — |
| **Cluster Activity** | `/api/v2/jobs`, `/api/v2/monitor/executor`, `/api/v2/monitor/health` | TBD | — |
| **Login** | `/auth/token` (form post) | TBD | — |
| **DAG with broken syntax** | `/api/v2/importErrors`, grid renders 0 runs | — | #217 SEVERE: SPA freezes whole UI |

## Known bug clustering

Grouping by likely root cause, since fixes share code paths:

### Cluster A — empty path-param routing (#213, #216)
SPA constructs URLs like `/dagRuns//taskInstances/...` when its router has
no run_id yet (race during initial mount). Our server returns 404 (correct)
but the symptom in the UI is "empty logs forever". Fix surface: either
short-circuit the empty-id case server-side (200 with empty payload) or
add a router-guard hint in the response (`X-Dexaflow-Empty-Id: 1`) the SPA
can read in its query key invalidation. Likely server-side workaround only.

### Cluster B — mark-state instant feedback (#211, parts of #271)
PATCH succeeds in ~10ms (see #272 timing log) but the UI lags because
TanStack Query's `staleTime` for the affected query is non-zero. Fix
surface: response headers (`Cache-Control: no-store` on PATCH responses
and the related GETs) or returning the post-mutation row shape so the
SPA can `setQueryData` instead of re-fetching. Server-side; ADR 0017
compatible.

### Cluster C — LAN clipboard (#214)
`navigator.clipboard` only works on secure contexts (HTTPS/localhost).
Lima users hit it over `http://192.168.x.y:8088`. Pure documentation +
fallback recommendation (`--host 0.0.0.0` + a local-tunnel hint).

### Cluster D — table model undefined data (#215)
React #520 = controlled/uncontrolled boundary crash. Typically a row
model receives `undefined` instead of an array. Likely our response shape
diverges from Airflow on some edge case (empty list vs null). Need to
diff the response with upstream's reference and align.

### Cluster E — broken-DAG freeze (#217)
The SPA hits an importErrors-only path and an infinite-loop render. We
already serve `/api/v2/importErrors` (#139). Need to identify which view
the freeze is on and what additional data the SPA expects.

### Cluster F — refresh latency / staleness (#209, #210, #212, #271)
Two distinct causes mixed in one symptom: (i) TanStack staleTime keeps
the cache, so manual triggers don't reflect; (ii) DagRun-state aggregation
in our server has lag (a finished TI takes N seconds to roll up into the
DagRun row). Need server-side instrumentation + cache-busting headers.

## Phase plan

- **Phase 0 — Baseline** (this PR): map+latency every view above, confirm
  bug clusters, output the live matrix.
- **Phase 1 — Cluster B, C, A** (server-side, ADR-safe quick wins).
- **Phase 2 — Cluster F** (response headers + scheduler tick budget).
- **Phase 3 — Cluster D, E** (response-shape diffs vs upstream).
- **Phase 4 — Decision per remaining bug**: live with / workaround /
  break ADR 0017.
