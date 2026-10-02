---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/trino.html
# --- end AUTO redirect aliases ---
title: Trino connection
linkTitle: Trino
weight: 520
description: Trino connection
---

Connect a task to a [Trino](https://trino.io/) coordinator to run
federated SQL across Hive, Iceberg, PostgreSQL, and other catalogs over a
managed Leoflow Connection.

## Declare the provider

```yaml
# dexaflow.yaml
dag_id: trino_query
connectors:
  - trino
```

## URI shape

```
trino://<login>:<password>@<host>:<port>/<catalog>
```

The control plane builds this from the Connection's fields and exports it
as `AIRFLOW_CONN_<CONN_ID>`. Reserved characters in the password are
percent-escaped; `TrinoHook` un-escapes them on the way out.

## Fields the UI asks for

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `trino_default`. Exported as `AIRFLOW_CONN_TRINO_DEFAULT`. |
| Conn Type | yes | `trino`. |
| Host | yes | Coordinator hostname, e.g. `trino.example.com`. |
| Port | usually `8080` | Coordinator HTTP port (`8443` for TLS). |
| Login | yes | Trino user, e.g. `etl_user`. |
| Password | optional | Password for `PASSWORD` auth. Encrypted at rest (ADR 0019). |
| Schema | optional | Default catalog, e.g. `hive`. |
| Extra | optional | JSON: `{"protocol":"https","catalog":"hive","schema":"default"}`. |

## Example DAG

The hook is imported **inside** the task body so the DAG file parses even
where the provider isn't installed.

```python
# dag.py
from airflow.sdk import DAG, task


@task
def count_rows():
    from airflow.providers.trino.hooks.trino import TrinoHook

    hook = TrinoHook(trino_conn_id="trino_default")
    rows = hook.get_records("SELECT count(*) FROM hive.default.events")
    print("event count:", rows[0][0])


with DAG("trino_query", schedule=None, catchup=False, tags=["example"]):
    count_rows()
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: trino_query
python_version: "3.12"
connectors:
  - trino
connections:
  - trino_default
```

## Security notes

- **TLS**: set the coordinator port to `8443` and `Extra =
  {"protocol":"https"}`. Never run `PASSWORD` auth over plain HTTP.
- **Least privilege**: scope the Trino user to the catalogs and schemas
  the DAG needs; Trino's system access control enforces this.
- Never log `AIRFLOW_CONN_TRINO_DEFAULT`; it carries the password.

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery (`AIRFLOW_CONN_<CONN_ID>`).
- `TestTrinoConnectionURIShapeIntegration` — chain-of-custody delivery test.
- [Presto connection](/connections/presto/) — the sibling query engine.
