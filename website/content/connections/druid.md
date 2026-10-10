---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/druid.html
# --- end AUTO redirect aliases ---
title: Druid connection
linkTitle: Druid
weight: 100
description: Druid connection
---

Connect a task to an Apache Druid cluster (the `DruidDbApiHook`) over a
managed Dexaflow Connection. The query path goes through the Druid broker.

## Declare the provider

```yaml
# dexaflow.yaml
dag_id: druid_smoke
connectors:
  - druid
```

## URI shape

```
druid://<login>:<password>@<host>:<port>/<schema>
```

The control plane builds this URI from the Connection's fields. Reserved
characters in the password (e.g. `@`, `:`, `/`) are percent-escaped by the
URI builder; `DruidDbApiHook` un-escapes them back. The reserved-character
round-trip is pinned by `TestDruidConnectionURIShapeIntegration` (in
`internal/storage/`).

## Fields the UI asks for

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `druid_target`. Exported as `AIRFLOW_CONN_DRUID_TARGET` (uppercased). |
| Conn Type | yes | `druid`. |
| Host | yes | The Druid broker host. |
| Schema | optional | The schema namespace (commonly `druid`). |
| Login | yes | The Druid user. |
| Password | yes | Stored encrypted at rest (ADR 0019). |
| Port | optional | Defaults to `8082` (broker). |
| Extra | optional | JSON — e.g. `{"endpoint":"druid/v2/sql"}`. Encrypted at rest. |

## Example DAG

The provider import must live **inside** the task body — a top-level
provider import fails compilation in the parser sidecar.

```python
# dag.py
from airflow.sdk import DAG, task


@task
def query():
    from airflow.providers.apache.druid.hooks.druid import DruidDbApiHook

    hook = DruidDbApiHook(druid_broker_conn_id="druid_target")
    rows = hook.get_records("SELECT 1")
    print("druid up:", rows)


with DAG("druid_smoke", schedule=None, catchup=False, tags=["example"]):
    query()
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: druid_smoke
python_version: "3.11"
connectors:
  - druid
```

## Security notes

- **Secrets in logs**: never `print()` the URI itself — it carries the
  password. Log host + port + schema only.
- **TLS in transit**: front the broker with HTTPS and set the endpoint in
  **Extra** accordingly.
- **gRPC channel (agent ↔ control plane)**: Connections are only served
  over an authenticated channel (see #58 + ADR 0021).

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery (`AIRFLOW_CONN_<CONN_ID>`).
- #142 — connector cookbook umbrella.
