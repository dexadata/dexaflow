---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/gcpbigquery.html
# --- end AUTO redirect aliases ---
title: Google BigQuery connection
linkTitle: Google BigQuery
weight: 150
description: Google BigQuery connection
---

Run queries and load jobs against Google BigQuery from a managed Leoflow
Connection. BigQuery carries no host and no password — the project and dataset
location live in **Extra**, and auth is keyless (Workload Identity / ADC).

## Declare the provider

```yaml
# dexaflow.yaml
dag_id: bigquery_demo
connectors:
  - gcpbigquery
```

## URI shape

BigQuery is an **Extra-only, keyless** connection:

```
gcpbigquery://?__extra__=<url-encoded JSON>
```

The control plane delivers it as `AIRFLOW_CONN_<ID>`; the `__extra__` query
parameter carries `{"project":"...","location":"..."}`. The chain-of-custody
test `TestGcpbigqueryConnectionURIShapeIntegration` pins the scheme and the
`__extra__` round-trip.

## Fields

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `bq_warehouse`. Exported as `AIRFLOW_CONN_BQ_WAREHOUSE`. |
| Conn Type | yes | `gcpbigquery`. |
| Extra | yes | JSON: `{"project":"my-proj","location":"EU"}`. |

No Login/Password: credentials come from the runtime identity (see Security).

## Example DAG (doc-only)

The provider import goes **inside** the `@task` body — a top-level provider
import fails the compile.

```python
# dag.py
from airflow.sdk import DAG, task


@task
def count_rows() -> int:
    from airflow.providers.google.cloud.hooks.bigquery import BigQueryHook

    hook = BigQueryHook(gcp_conn_id="bq_warehouse")
    rows = hook.get_records("SELECT count(*) FROM `my-proj.analytics.events`")
    return int(rows[0][0])


with DAG("bigquery_demo", schedule=None, catchup=False, tags=["example"]):
    count_rows()
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: bigquery_demo
python_version: "3.12"
connectors:
  - gcpbigquery
```

## Security notes

- **Keyless (Workload Identity / ADC).** No service-account key in the
  Connection. On Pro/GKE the task pod runs as a Kubernetes SA bound to a GCP
  service account (Workload Identity); on Lite (subprocess) the host's ADC is
  used ([ADR 0035](/project/adrs/0035-cloud-connector-auth-keyless-first/)). The
  Connection carries only project + location.
- **Never log the URI.** Even keyless, treat it as config, not output.
- Config travels only over the authenticated, TLS agent channel (ADR 0021).

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery.
- ADR 0035 — keyless-first cloud connector auth.
- [google_cloud_platform.md](/connections/google_cloud_platform/) — the generic GCP connection and full keyless background.
- [gcpcloudsql.md](/connections/gcpcloudsql/) — the other GCP data service in this batch.
