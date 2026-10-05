---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /adr/0010-observability.html
# --- end AUTO redirect aliases ---
title: "ADR 0010: Observability Stack from Day One"
linkTitle: 0010 · Observability Stack from Day One
weight: 100
description: "ADR 0010: Observability Stack from Day One"
---

**Status:** Accepted
**Date:** 2026-05-21

## Context

One of Leoflow's stated differentiators is **native observability**, which Airflow famously lacks. This must be backed by actual implementation from the first commit, not bolted on later.

## Decision

Every Leoflow binary ships with **three pillars of observability** built in:

1. **Metrics** — Prometheus, exposed at `/metrics`.
2. **Tracing** — OpenTelemetry, exporting via OTLP to any compatible collector (Jaeger, Tempo, Honeycomb, Datadog).
3. **Logs** — Structured JSON via `log/slog`, with consistent fields (`trace_id`, `span_id`, `dag_id`, `task_id`, `run_id`).

All three are wired together via OpenTelemetry context propagation, so a log line, a trace span, and a metric label can be correlated.

## Required Metrics for the MVP

The following metrics **must exist** before the MVP is considered complete. They are the contract Leoflow makes with operators.

### Scheduler

| Metric | Type | Labels |
|---|---|---|
| `leoflow_scheduler_loop_duration_seconds` | Histogram | — |
| `leoflow_scheduler_decisions_total` | Counter | `decision_type` (schedule/skip/defer) |
| ~~`leoflow_scheduler_leader`~~ | Gauge | `replica_id` |
| ~~`leoflow_active_dag_runs`~~ | Gauge | `dag_id`, `state` |
| ~~`leoflow_queued_tasks`~~ | Gauge | `dag_id` |

> **Superseded in part by #1323.** The three gauges struck out above were
> registered but nothing ever set them, so they never produced a series. They
> are removed from the binary and from this required set. Leadership and run
> and queue depth can be read from the database or the API until a gauge is
> wired to a real source.

### Task Lifecycle

| Metric | Type | Labels |
|---|---|---|
| `leoflow_task_state_transitions_total` | Counter | `from_state`, `to_state`, `dag_id` |
| `leoflow_task_duration_seconds` | Histogram | `dag_id`, `task_id`, `task_type` |
| `leoflow_task_retries_total` | Counter | `dag_id`, `task_id` |
| `leoflow_task_pod_creation_duration_seconds` | Histogram | — |
| `leoflow_task_cold_start_seconds` | Histogram | `dag_id` |

### XCom

| Metric | Type | Labels |
|---|---|---|
| `leoflow_xcom_size_bytes` | Histogram | `dag_id` |
| `leoflow_xcom_push_total` | Counter | `dag_id` |
| `leoflow_xcom_pull_total` | Counter | `dag_id` |
| `leoflow_xcom_rejected_total` | Counter | `reason` (too_large/schema_mismatch/expired) |

### API

| Metric | Type | Labels |
|---|---|---|
| `leoflow_http_requests_total` | Counter | `method`, `path`, `status` |
| `leoflow_http_request_duration_seconds` | Histogram | `method`, `path` |
| `leoflow_auth_failures_total` | Counter | `reason` |

### Executor (K8s)

| Metric | Type | Labels |
|---|---|---|
| `leoflow_pods_created_total` | Counter | `dag_id`, `result` (success/error) |
| ~~`leoflow_pods_running`~~ | Gauge | none |
| `leoflow_pod_pending_duration_seconds` | Histogram | — |
| `leoflow_kubernetes_api_calls_total` | Counter | `operation`, `result` |

> **Superseded in part by #1323.** `leoflow_pods_running` (and its `dexaflow_`
> name) was registered but never set, so every scrape reported a constant `0`
> that a dashboard reads as a real measurement. It is removed; count task pods
> with kube-state-metrics.

## Tracing

Every task instance gets a root span with these attributes:

- `leoflow.dag_id`
- `leoflow.task_id`
- `leoflow.run_id`
- `leoflow.try_number`

Child spans:

- `scheduler.decision`
- `executor.create_pod`
- `agent.fetch_xcom`
- `agent.execute_user_code`
- `agent.push_xcom`

The trace continues across the gRPC boundary between the Control Plane and the Agent via standard OTel context propagation.

## Logs

Every log line is JSON, written to `stdout`. Common fields:

```json
{
  "time": "2026-05-21T14:23:11.482Z",
  "level": "INFO",
  "msg": "task transitioned to RUNNING",
  "trace_id": "abc123...",
  "span_id": "def456...",
  "dag_id": "etl_vendas",
  "task_id": "extract",
  "run_id": "scheduled__2026-05-21",
  "try_number": 1,
  "tenant_id": "default"
}
```

No human-readable formatters in production builds. JSON only. Operators use `jq` or log aggregators.

## Health Checks

Every binary exposes:

- `/healthz` — liveness. Returns 200 if the process is alive.
- `/readyz` — readiness. Returns 200 only if dependencies (Postgres, Redis) are reachable.

K8s deployments use these for liveness and readiness probes.

> **Superseded in part by #1023.** "Reachable" turned out to be too weak a bar
> for the dependency that carries a schema. A Postgres `Ping` succeeds whenever
> the *connection* is healthy and says nothing about what is behind it, so a
> database emptied by a node recycle, restored from an older backup, failed over
> to a lagging replica or repointed by a changed `database.url` kept `/readyz`
> at 200 while the control plane could not serve one authenticated request.
> Readiness now also asserts that `schema_migrations` is present and not behind
> the version the running binary embeds, with one deliberate exemption: a
> migration in flight *above* that version keeps the pod ready, because the
> migrate Job is a `pre-upgrade` hook and failing it would empty the Service on
> every slow upgrade. The decision above is left as written — it is the record
> of what was decided in 2026-05, and this note is the record of what replaced
> it.

## Consequences

- The dependency footprint grows. `client_golang`, `go.opentelemetry.io/otel`, and `slog` are mandatory.
- Performance overhead is real but small. Metrics and traces are cheap when batched. Logs at INFO level are negligible.
- The CI pipeline must validate that every new metric is registered with a description and that no metric explodes label cardinality.
- Operators get a Grafana dashboard template shipped with the project (in `helm/dashboards/`).

## Alternatives Rejected

- **Add observability later:** rejected because retrofitting tracing across an existing codebase is enormously expensive.
- **Only logs, no metrics or traces:** rejected because logs alone cannot answer "is the system slow right now?".
- **Custom metrics format:** rejected because Prometheus is the industry standard.
