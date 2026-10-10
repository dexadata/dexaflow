---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/neo4j.html
# --- end AUTO redirect aliases ---
title: Neo4j connection
linkTitle: Neo4j
weight: 330
description: Neo4j connection
---

Connect a task to a Neo4j graph database (the `Neo4jHook`) over a managed
Dexaflow Connection. The database name lives in the Schema field.

## Declare the provider

```yaml
# dexaflow.yaml
dag_id: neo4j_smoke
connectors:
  - neo4j
```

## URI shape

```
neo4j://<login>:<password>@<host>:<port>/<database>
```

The control plane builds this URI from the Connection's fields. Reserved
characters in the password (e.g. `@`, `:`, `/`) are percent-escaped by the
URI builder; `Neo4jHook` un-escapes them back. The reserved-character
round-trip is pinned by `TestNeo4jConnectionURIShapeIntegration` (in
`internal/storage/`).

## Fields the UI asks for

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `neo4j_target`. Exported as `AIRFLOW_CONN_NEO4J_TARGET` (uppercased). |
| Conn Type | yes | `neo4j`. |
| Host | yes | DNS name or IP of the Bolt endpoint. |
| Schema | optional | The database name (defaults to `neo4j`). |
| Login | yes | The Neo4j user. |
| Password | yes | Stored encrypted at rest (ADR 0019). |
| Port | optional | Defaults to `7687` (Bolt). |
| Extra | optional | JSON — e.g. `{"encrypted":true}` to force TLS. Encrypted at rest. |

## Example DAG

The provider import must live **inside** the task body — a top-level
provider import fails compilation in the parser sidecar.

```python
# dag.py
from airflow.sdk import DAG, task


@task
def query():
    from airflow.providers.neo4j.hooks.neo4j import Neo4jHook

    hook = Neo4jHook(conn_id="neo4j_target")
    rows = hook.run("RETURN 1 AS ok")
    print("neo4j up:", rows)


with DAG("neo4j_smoke", schedule=None, catchup=False, tags=["example"]):
    query()
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: neo4j_smoke
python_version: "3.11"
connectors:
  - neo4j
```

## Security notes

- **Secrets in logs**: never `print()` the URI itself — it carries the
  password. Log host + port + database only.
- **TLS in transit**: set `{"encrypted":true}` in **Extra** to force a
  TLS Bolt connection.
- **gRPC channel (agent ↔ control plane)**: Connections are only served
  over an authenticated channel (see #58 + ADR 0021).

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery (`AIRFLOW_CONN_<CONN_ID>`).
- #142 — connector cookbook umbrella.
