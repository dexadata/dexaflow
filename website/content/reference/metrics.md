---
title: Metrics
linkTitle: Metrics
weight: 62
description: Every Prometheus metric family the control plane exports, with its type, labels and meaning.
---

The control plane serves Prometheus metrics at `/metrics` on its metrics listener
(`server.metrics_addr`, `0.0.0.0:9090` by default; see
[Configuration](/reference/configuration/)). Every role serves it, so a
scheduler-only pod in a split install is scraped like the API pod.

Each `dexaflow_*` family below is also published under its name from before the
rename, `leoflow_*`, with the same values, so dashboards and alerts written
against either name keep working. Set
`observability.metrics.drop_legacy_names: true` to publish each family once and
halve the scrape.

Labels carry what the meaning names. `dag_id` and `task_id` grow with the DAGs
you deploy; `path` on the HTTP families is the route template, not the raw URL,
so it stays bounded.

For what to alert on and how to read the scheduler and dispatch families during
an incident, see [Troubleshooting & observability](/operate/troubleshooting/) and
[Scheduler resilience](/operate/scheduler-resilience/).

The table is generated from the collectors the server registers: run
`make gen-metrics-reference` after adding or changing a metric, and
`go test ./internal/tools/metricsref/` fails while the page is stale.

<!-- BEGIN GENERATED metricsref -->
| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `dexaflow_agent_legacy_attempt_token_total` | counter | _(none)_ | Agent RPCs authenticated by a task token without the attempt_epoch claim: one minted before the upgrade, or one an old replica renewed or exchanged during a rolling upgrade. Reports from such a token are read as epoch 0. It falls to zero once the rollout has finished and every pre-upgrade attempt has finished; a later release rejects such tokens. |
| `dexaflow_agent_token_rejections_total` | counter | `reason` | Agent RPCs refused for their bearer token, by reason: expired (the credential lapsed: the agent could not renew it for a whole TTL, or its pod started after it ran out), wrong_audience (a token for another surface), bad_signature (another signing key), wrong_issuer, not_yet_valid, malformed, invalid, or missing (no bearer at all). |
| `dexaflow_alerts_dispatched_total` | counter | `dag_id`, `type`, `result` | Native on-failure alerts dispatched, by channel type and outcome (sent, failed). Operators alert on rate of result="failed" to catch a broken alert path (#424). |
| `dexaflow_auth_failures_total` | counter | `reason` | Authentication failures by reason. |
| `dexaflow_ci_oidc_exchange_total` | counter | `issuer`, `outcome` | CI OIDC token exchanges (auth.ci_oidc, ADR 0092) by issuer entry (unknown when none matched) and outcome (issued, invalid_token, unknown_issuer, no_rule, role_missing, keys_unavailable, error). |
| `dexaflow_db_pool_acquired_conns` | gauge | `pool` | Connections currently lent out by the database pool, by pool (main, health, scheduler). |
| `dexaflow_db_pool_canceled_acquires_total` | counter | `pool` | Acquires that gave up waiting for a connection (their context ended), by pool (main, health, scheduler). |
| `dexaflow_db_pool_empty_acquire_wait_seconds_total` | counter | `pool` | Time acquires spent waiting for a connection, in seconds, by pool (main, health, scheduler). |
| `dexaflow_db_pool_empty_acquires_total` | counter | `pool` | Acquires that found no idle connection and had to wait for one, by pool (main, health, scheduler). |
| `dexaflow_db_pool_idle_conns` | gauge | `pool` | Idle connections in the database pool, by pool (main, health, scheduler). |
| `dexaflow_db_pool_max_conns` | gauge | `pool` | Maximum connections the database pool may open, by pool (main, health, scheduler). |
| `dexaflow_db_pool_total_conns` | gauge | `pool` | Open connections in the database pool, idle, lent out and being opened, by pool (main, health, scheduler). |
| `dexaflow_dispatch_at_capacity_total` | counter | _(none)_ | Dispatch requests rejected because the buffer was full. |
| `dexaflow_dispatch_inner_errors_total` | counter | _(none)_ | Errors returned by the inner dispatcher inside a worker. |
| `dexaflow_dispatch_latency_seconds` | histogram | _(none)_ | End-to-end latency of one buffered dispatch (enqueue to worker completion). |
| `dexaflow_dispatch_queue_depth` | gauge | _(none)_ | Number of dispatch requests currently buffered. |
| `dexaflow_http_request_duration_seconds` | histogram | `method`, `path` | HTTP request duration. |
| `dexaflow_http_requests_total` | counter | `method`, `path`, `status` | HTTP requests. |
| `dexaflow_kubernetes_api_calls_total` | counter | `operation`, `result` | Kubernetes API calls. |
| `dexaflow_log_tail_replay_bytes` | gauge | _(none)_ | Bytes of task-log lines the on_demand live-tail gates hold for a follower who may subscribe, across all log streams. Bounded by logs.tail.replay_budget_bytes when set. |
| `dexaflow_log_tail_replay_evictions_total` | counter | _(none)_ | Live-tail replay rings dropped to keep the total under logs.tail.replay_budget_bytes, the longest silent stream first. A follower of a dropped stream reads the dropped lines from the stored log. |
| `dexaflow_pod_pending_duration_seconds` | histogram | _(none)_ | Pod pending duration. |
| `dexaflow_pods_created_total` | counter | `dag_id`, `result` | Pods created by dag and result. |
| `dexaflow_reconcile_infra_override_total` | counter | `mark` | Durable SUCCESS records the reconciler settled over a reaper's provisional infra mark (agent_lost, pod_lost, dispatch_lost), by the mark overridden: a task that finished while the control plane lost track of it, recovered instead of re-run. |
| `dexaflow_redis_command_failures_total` | counter | `reason` | Redis command failures classified by reason (timeout, connection_refused, auth, canceled, other). Alert on rate(...[5m]) to surface a degrading client-side path before user-visible XCom errors (#312 sibling). |
| `dexaflow_redis_dial_duration_seconds` | histogram | _(none)_ | Wall-clock time per Redis dial: TCP connect plus, with rediss://, the TLS handshake. |
| `dexaflow_redis_dial_failures_total` | counter | `reason` | Failures at TCP/TLS dial time to Redis, classified by reason (tls_handshake, connection_refused, dns, timeout, other). |
| `dexaflow_redis_pool_active_conns` | gauge | _(none)_ | Redis pool: connections currently checked out for a command. |
| `dexaflow_redis_pool_idle_conns` | gauge | _(none)_ | Redis pool: idle connections available for the next command. |
| `dexaflow_redis_pool_timeouts_total` | counter | _(none)_ | Redis pool checkout timeouts: a caller waited PoolTimeout for an idle connection. A non-zero rate means the pool is too small or commands are too slow. |
| `dexaflow_redis_pool_total_conns` | gauge | _(none)_ | Redis pool: total connections (active + idle). Saturating against PoolSize is the leading indicator of throughput issues. |
| `dexaflow_resilience_backstop_disabled` | gauge | `config_key` | 1 when this process booted with the resilience backstop of config_key disabled, 0 when it is in place. Set once at boot by processes that run the scheduler; alert on max(...) > 0. |
| `dexaflow_retention_cycle_duration_seconds` | histogram | _(none)_ | Wall-clock duration of one retention janitor cycle, pauses included. |
| `dexaflow_retention_rows_deleted_total` | counter | `table` | Metadata rows the retention janitor deleted, by table. |
| `dexaflow_retention_rows_eligible` | gauge | `table` | Rows the last retention dry run found eligible for deletion, by table, summed across all tenants. |
| `dexaflow_scheduler_decisions_total` | counter | `decision_type` | Scheduler decisions by type. |
| `dexaflow_scheduler_loop_duration_seconds` | histogram | _(none)_ | Duration of one scheduler loop iteration. |
| `dexaflow_scheduler_reacquire_seconds` | histogram | _(none)_ | Wall-clock seconds between a leader step-down and the same replica reacquiring leadership. A growing P99 signals leader churn that affects scheduling latency (#311). |
| `dexaflow_scheduler_schedule_delay_seconds` | histogram | _(none)_ | Time from a scheduled run's slot to its creation, for the latest slot a tick created for a DAG. |
| `dexaflow_scheduler_step_downs_total` | counter | `reason` | Scheduler leadership step-downs by reason (lock_released, check_timeout, shutdown). Operators alert on rate(...[5m]); a sudden uptick usually indicates a Postgres connection-stability issue (#311). |
| `dexaflow_scheduler_woken_ticks_total` | counter | _(none)_ | Scheduler ticks started early by a task completion instead of the loop interval (scheduler.eager_promotion). |
| `dexaflow_schema_ready` | gauge | `reason` | 1 when the database schema is one this binary can serve, 0 while it waits for it, with the reason (schema absent, schema behind, schema dirty, schema unreadable). Alert on 0 for 5 minutes. |
| `dexaflow_schema_wait_seconds` | gauge | _(none)_ | Seconds since this process found its database schema not ready, 0 when it is ready. |
| `dexaflow_task_cold_start_seconds` | histogram | `dag_id` | Task cold start time. |
| `dexaflow_task_duration_seconds` | histogram | `dag_id`, `task_id`, `task_type` | Task execution duration. |
| `dexaflow_task_pod_creation_duration_seconds` | histogram | _(none)_ | Time to create a task pod. |
| `dexaflow_task_retries_total` | counter | `dag_id`, `task_id` | Task retries. |
| `dexaflow_task_state_transitions_total` | counter | `from_state`, `to_state`, `dag_id` | Task state transitions. |
| `dexaflow_tasks_undispatchable_total` | counter | `reason` | Tasks queued with no executor to launch them, by reason. |
| `dexaflow_tls_reload_failures_total` | counter | `listener` | Rotated TLS certificate or key files that failed to load, by listener (api, agent_grpc). The listener keeps serving the last good certificate; alert on any increase (#637). |
| `dexaflow_unit_misfit_total` | counter | `stage` | Tasks accepted under executor.unit.enforce=warn although their resources exceed pool_slots x unit, by stage (register, set_current, dispatch). |
| `dexaflow_unpinned_image_total` | counter | `stage`, `outcome` | Images not pinned by digest that deploy.require_digest warned about (outcome=warned) or refused (outcome=refused), by stage (register, set_current). Dry runs are not counted. |
| `dexaflow_xcom_pull_total` | counter | `dag_id` | XCom pulls. |
| `dexaflow_xcom_push_total` | counter | `dag_id` | XCom pushes. |
| `dexaflow_xcom_rejected_total` | counter | `reason` | Rejected XCom writes by reason. |
| `dexaflow_xcom_size_bytes` | histogram | `dag_id` | XCom payload size in bytes. |
<!-- END GENERATED metricsref -->
