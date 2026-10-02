---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/elasticsearch.html
# --- end AUTO redirect aliases ---
title: Elasticsearch connection
linkTitle: Elasticsearch
weight: 110
description: Elasticsearch connection
---

Connect a task to an Elasticsearch cluster's SQL endpoint (the
`ElasticsearchSQLHook`) over a managed Leoflow Connection.

## Declare the provider

```yaml
# dexaflow.yaml
dag_id: elasticsearch_smoke
connectors:
  - elasticsearch
```

## URI shape

```
elasticsearch://<login>:<password>@<host>:<port>/<schema>
```

The control plane builds this URI from the Connection's fields. Reserved
characters in the password (e.g. `@`, `:`, `/`) are percent-escaped by the
URI builder; `ElasticsearchSQLHook` un-escapes them back. The
reserved-character round-trip is pinned by
`TestElasticsearchConnectionURIShapeIntegration` (in `internal/storage/`).

## Fields the UI asks for

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `es_target`. Exported as `AIRFLOW_CONN_ES_TARGET` (uppercased). |
| Conn Type | yes | `elasticsearch`. |
| Host | yes | DNS name or IP of a cluster node. |
| Schema | optional | The default schema (commonly `default`). |
| Login | yes | The Elasticsearch user. |
| Password | yes | Stored encrypted at rest (ADR 0019). |
| Port | optional | Defaults to `9200`. |
| Extra | optional | JSON — e.g. `{"use_ssl":true}` to force TLS. Encrypted at rest. |

## Example DAG

The provider import must live **inside** the task body — a top-level
provider import fails compilation in the parser sidecar.

```python
# dag.py
from airflow.sdk import DAG, task


@task
def query():
    from airflow.providers.elasticsearch.hooks.elasticsearch import ElasticsearchSQLHook

    hook = ElasticsearchSQLHook(elasticsearch_conn_id="es_target")
    rows = hook.get_records("SELECT 1")
    print("elasticsearch up:", rows)


with DAG("elasticsearch_smoke", schedule=None, catchup=False, tags=["example"]):
    query()
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: elasticsearch_smoke
python_version: "3.11"
connectors:
  - elasticsearch
```

## Security notes

- **Secrets in logs**: never `print()` the URI itself — it carries the
  password. Log host + port + schema only.
- **TLS in transit**: set `{"use_ssl":true}` in **Extra** to force TLS.
- **gRPC channel (agent ↔ control plane)**: Connections are only served
  over an authenticated channel (see #58 + ADR 0021).

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery (`AIRFLOW_CONN_<CONN_ID>`).
- #142 — connector cookbook umbrella.
