---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /examples.html
# --- end AUTO redirect aliases ---
title: Examples
weight: 70
description: Runnable example DAGs covering the common authoring patterns.
---

A gallery of ready-to-run DAGs under [`examples/`](https://github.com/dexadata/dexaflow/tree/main/examples),
covering every Dexaflow task type and the common patterns. Each is compile-valid
(`dexaflow compile`) and authored parser-safe (heavy imports live *inside* the
tasks). Run any of them with:

```bash
dexaflow lite examples/<name>      # hot-reload at http://localhost:8088, then Trigger
```

## The gallery

| Example | Shows | Task type | Deps |
|---|---|---|---|
| `taskflow_sales` | TaskFlow ETL, data via XCom | python | — |
| `xcom_typed` | typed XCom payloads + validation | python | — |
| `ml_hparam_search` | **map-reduce** ML pattern: parallel trials → pick best | python | — |
| `fan_out_aggregate` | fan-out to N shards → fan-in aggregate (map-reduce) | python | — |
| `montecarlo_pi` | parallel π estimate (Monte Carlo map-reduce) | python | — |
| `http_jsonplaceholder` | call a public JSON API | python | requests |
| `weather_open_meteo` | public weather API (no key) | python | requests |
| `api_chain` | chain two API calls | python | requests |
| `duckdb_http_csv` | DuckDB reads a remote CSV, aggregates | python | duckdb |
| `postgres_load` | load to external Postgres via a Connection | python | psycopg2 |
| `csv_report` | **scheduled** (cron `0 6 * * *`) report | python | — |
| `bash_pipeline` | shell tasks | **bash** (BashOperator) | — |
| `http_operator` | HTTP request run **in a pod** | **airflow_operator** (HttpOperator, ADR 0047) | — |

The core Dexaflow task types are represented — **python** (TaskFlow `@task` /
PythonOperator) and **bash** (BashOperator), both run in a pod. An `HttpOperator`
compiles to an **`airflow_operator`** and runs in a pod too (ADR 0040); the old
native inline `http_api` type was removed (ADR 0047/0048, #512).
For a measured ~1 GB pipeline see the [ETL case study](/author-dags/etl-case-study/).

{{% alert title="Import heavy deps inside the task" color="success" %}}
`import duckdb` / `import requests` go **inside** the task function — the DAG
parser imports the module to extract structure and does not have the task
image's dependencies.
{{% /alert %}}

## Removing a DAG (clear vs. deregister)

Dexaflow is GitOps: the **source is the source of truth**, so deleting is two
distinct actions (ADR 0020).

| Action | Effect |
|---|---|
| **Clear history** (UI trash · `dexaflow dags delete <id>`) | deletes runs/tasks; the **DAG stays registered** |
| **Deregister** (`dexaflow dags delete <id> --deregister`) | removes the DAG artifact (DAG + versions) |

But deregister alone is **not permanent while the source exists** — it gets
re-registered:

- **Lite (`dexaflow lite`):** the watcher re-registers the DAG on the next reload.
  To remove it for good, **delete the DAG's file** (or stop/point `dexaflow lite`
  elsewhere).
- **Pro (CI deploy):** the next deploy that still includes the DAG
  re-registers it as a new version. To remove it for good, **drop it from the
  repo/CI**, then optionally deregister to clear what is registered now.
- **Demo:** seeded once at boot with no watcher/CI re-registering, so a clear or
  deregister sticks until you re-seed.

In short: to truly remove a DAG, **remove its source** (file in Lite, repo in
prod); `--deregister` just clears the current registration. (The embedded Airflow
UI's trash maps to *clear history*; an explicit "Clear vs Deregister" dialog is
planned for the custom UI — ADR 0018/0020.)
