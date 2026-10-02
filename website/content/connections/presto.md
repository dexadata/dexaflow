---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/presto.html
# --- end AUTO redirect aliases ---
title: Presto connection
linkTitle: Presto
weight: 400
description: Presto connection
---

Connect a task to a [Presto](https://prestodb.io/) coordinator to run
distributed SQL over a managed Dexaflow Connection. Presto is the upstream
project Trino forked from; the Connection shape is identical, only the
scheme and hook differ.

## Declare the provider

```yaml
# dexaflow.yaml
dag_id: presto_query
connectors:
  - presto
```

## URI shape

```
presto://<login>:<password>@<host>:<port>/<catalog>
```

The control plane builds this from the Connection's fields and exports it
as `AIRFLOW_CONN_<CONN_ID>`. Reserved characters in the password are
percent-escaped; `PrestoHook` un-escapes them on the way out.

## Fields the UI asks for

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `presto_default`. Exported as `AIRFLOW_CONN_PRESTO_DEFAULT`. |
| Conn Type | yes | `presto`. |
| Host | yes | Coordinator hostname, e.g. `presto.example.com`. |
| Port | usually `8080` | Coordinator HTTP port (`8443` for TLS). |
| Login | yes | Presto user, e.g. `etl_user`. |
| Password | optional | Password for `PASSWORD` auth. Encrypted at rest (ADR 0019). |
| Schema | optional | Default catalog, e.g. `hive`. |
| Extra | optional | JSON: `{"protocol":"https","catalog":"hive","schema":"default"}`. |

## Example DAG

```python
# dag.py
from airflow.sdk import DAG, task


@task
def count_rows():
    from airflow.providers.presto.hooks.presto import PrestoHook

    hook = PrestoHook(presto_conn_id="presto_default")
    rows = hook.get_records("SELECT count(*) FROM hive.default.events")
    print("event count:", rows[0][0])


with DAG("presto_query", schedule=None, catchup=False, tags=["example"]):
    count_rows()
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: presto_query
python_version: "3.12"
connectors:
  - presto
connections:
  - presto_default
```

## Security notes

- **TLS**: set the coordinator port to `8443` and `Extra =
  {"protocol":"https"}`. Never run `PASSWORD` auth over plain HTTP.
- **Least privilege**: scope the Presto user to the catalogs the DAG needs.
- Never log `AIRFLOW_CONN_PRESTO_DEFAULT`; it carries the password.

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery (`AIRFLOW_CONN_<CONN_ID>`).
- `TestPrestoConnectionURIShapeIntegration` — chain-of-custody delivery test.
- [Trino connection](/connections/trino/) — the modern fork of Presto.
