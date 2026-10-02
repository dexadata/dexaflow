---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/datadog.html
# --- end AUTO redirect aliases ---
title: Datadog connection
linkTitle: Datadog
weight: 60
description: Datadog connection
---

Submit metrics, events, and query monitors from a task over a managed
Dexaflow Connection. `DatadogHook` authenticates with an API key + app key
against the Datadog site (US, EU, …).

## Declare the provider

```yaml
# dexaflow.yaml
dag_id: datadog_metric
connectors:
  - datadog
```

## URI shape

```
datadog:?__extra__=<json>
```

There is **no host and no password** — every field (API host, API key, app
key, source type) lives in `Extra` under `__extra__`. This is the
extra-only shape.

## Fields the UI asks for

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `datadog_default`. Exported as `AIRFLOW_CONN_DATADOG_DEFAULT`. |
| Conn Type | yes | `datadog`. |
| Extra | yes | JSON with `api_host`, `api_key`, `app_key`, `source_type_name` — see below. |

`Extra` example:

```json
{
  "api_host": "https://api.datadoghq.eu",
  "api_key": "...",
  "app_key": "...",
  "source_type_name": "airflow"
}
```

## Example DAG

```python
# dag.py
from airflow.sdk import DAG, task


@task
def send():
    from airflow.providers.datadog.hooks.datadog import DatadogHook

    hook = DatadogHook(datadog_conn_id="datadog_default")
    hook.send_metric(
        metric_name="leoflow.dag.runs",
        datapoint=1,
        tags=["dag:datadog_metric"],
    )


with DAG("datadog_metric", schedule=None, catchup=False, tags=["example"]):
    send()
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: datadog_metric
python_version: "3.12"
connectors:
  - datadog
connections:
  - datadog_default
```

## Security notes

- **API key vs app key**: the API key authorizes submission; the app key
  authorizes reads/queries. Scope the app key narrowly.
- **Pick the right site**: `api_host` must match your Datadog org's region
  (`datadoghq.com`, `datadoghq.eu`, …) or calls 403.
- Never log `AIRFLOW_CONN_DATADOG_DEFAULT`; the keys ride in `__extra__`.

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery (`AIRFLOW_CONN_<CONN_ID>`).
- `TestDatadogConnectionURIShapeIntegration` — chain-of-custody delivery test.
