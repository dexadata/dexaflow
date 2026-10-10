---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/vertica.html
# --- end AUTO redirect aliases ---
title: Vertica connection
linkTitle: Vertica
weight: 530
description: Vertica connection
---

Connect a task to a Vertica analytics database (the `VerticaHook`) over a
managed Dexaflow Connection. The database lives in the Schema field.

## Declare the provider

```yaml
# dexaflow.yaml
dag_id: vertica_smoke
connectors:
  - vertica
```

## URI shape

```
vertica://<login>:<password>@<host>:<port>/<database>
```

The control plane builds this URI from the Connection's fields. Reserved
characters in the password (e.g. `@`, `:`, `/`) are percent-escaped by the
URI builder; `VerticaHook` un-escapes them back. The reserved-character
round-trip is pinned by `TestVerticaConnectionURIShapeIntegration` (in
`internal/storage/`).

## Fields the UI asks for

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `vertica_target`. Exported as `AIRFLOW_CONN_VERTICA_TARGET` (uppercased). |
| Conn Type | yes | `vertica`. |
| Host | yes | DNS name or IP. |
| Schema | yes | The database name. |
| Login | yes | The Vertica user. |
| Password | yes | Stored encrypted at rest (ADR 0019). |
| Port | optional | Defaults to `5433`. |
| Extra | optional | JSON — e.g. `{"ssl":true}` to force TLS. Encrypted at rest. |

## Example DAG

The provider import must live **inside** the task body — a top-level
provider import fails compilation in the parser sidecar.

```python
# dag.py
from airflow.sdk import DAG, task


@task
def query():
    from airflow.providers.vertica.hooks.vertica import VerticaHook

    hook = VerticaHook(vertica_conn_id="vertica_target")
    rows = hook.get_records("SELECT version()")
    print("vertica up:", rows)


with DAG("vertica_smoke", schedule=None, catchup=False, tags=["example"]):
    query()
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: vertica_smoke
python_version: "3.11"
connectors:
  - vertica
```

## Security notes

- **Secrets in logs**: never `print()` the URI itself — it carries the
  password. Log host + port + database only.
- **TLS in transit**: set `{"ssl":true}` in **Extra** to force TLS.
- **gRPC channel (agent ↔ control plane)**: Connections are only served
  over an authenticated channel (see #58 + ADR 0021).

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery (`AIRFLOW_CONN_<CONN_ID>`).
- #142 — connector cookbook umbrella.
