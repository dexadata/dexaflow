---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/gcpcloudsql.html
# --- end AUTO redirect aliases ---
title: Google Cloud SQL connection
linkTitle: Google Cloud SQL
weight: 160
description: Google Cloud SQL connection
---

Connect to a Google Cloud SQL instance (Postgres / MySQL) from a managed
Leoflow Connection. The instance coordinates live in **Extra**; there is no
password on the Connection — auth flows through the Cloud SQL connector via the
runtime identity (ADC).

## Declare the provider

```yaml
# dexaflow.yaml
dag_id: cloudsql_demo
connectors:
  - gcpcloudsql
```

## URI shape

Cloud SQL is an **Extra-only, keyless** connection:

```
gcpcloudsql://?__extra__=<url-encoded JSON>
```

The control plane delivers it as `AIRFLOW_CONN_<ID>`; the `__extra__` query
parameter carries the instance descriptor. The chain-of-custody test
`TestGcpcloudsqlConnectionURIShapeIntegration` pins the scheme and the
`__extra__` round-trip.

## Fields

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `cloudsql_app`. Exported as `AIRFLOW_CONN_CLOUDSQL_APP`. |
| Conn Type | yes | `gcpcloudsql`. |
| Extra | yes | JSON: `{"database_type":"postgres","project_id":"my-proj","location":"europe-west1","instance":"my-inst"}`. |

No Login/Password on the Connection: the Cloud SQL connector opens the proxy
using the task's ambient identity (see Security).

## Example DAG (doc-only)

The provider import goes **inside** the `@task` body — a top-level provider
import fails the compile.

```python
# dag.py
from airflow.sdk import DAG, task


@task
def ping() -> str:
    from airflow.providers.google.cloud.hooks.cloud_sql import CloudSQLHook

    hook = CloudSQLHook(gcp_cloudsql_conn_id="cloudsql_app", api_version="v1beta4")
    instance = hook.get_instance(instance="my-inst")
    return instance["state"]


with DAG("cloudsql_demo", schedule=None, catchup=False, tags=["example"]):
    ping()
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: cloudsql_demo
python_version: "3.12"
connectors:
  - gcpcloudsql
```

## Security notes

- **Keyless (Workload Identity / ADC).** No service-account key in the
  Connection. The Cloud SQL Auth Proxy authenticates with the task's ambient
  identity ([ADR 0035](/project/adrs/0035-cloud-connector-auth-keyless-first/)). Grant
  that identity `roles/cloudsql.client`.
- **Never log the URI.** Treat the instance descriptor as config.
- Config travels only over the authenticated, TLS agent channel (ADR 0021).

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery.
- ADR 0035 — keyless-first cloud connector auth.
- [google_cloud_platform.md](/connections/google_cloud_platform/) — the generic GCP connection and full keyless background.
- [gcpbigquery.md](/connections/gcpbigquery/) — the other GCP data service in this batch.
