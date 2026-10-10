---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/athena.html
# --- end AUTO redirect aliases ---
title: Amazon Athena connection
linkTitle: Amazon Athena
weight: 10
description: Amazon Athena connection
---

Run SQL against Amazon Athena (serverless Presto/Trino over S3) from a managed
Dexaflow Connection. Athena has no host or port — the region, schema, and work
group live in **Extra**.

## Declare the provider

```yaml
# dexaflow.yaml
dag_id: athena_demo
connectors:
  - athena
```

## URI shape

Athena is an **Extra-bearing** connection. The control plane delivers it as
`AIRFLOW_CONN_<ID>`:

```
athena://[:<aws_secret_key>@]?__extra__=<url-encoded JSON>
```

The `__extra__` query parameter carries the JSON blob (region, work group,
schema). The optional password carries an AWS secret access key for the explicit
credential path; **keyless IAM is preferred** (see Security). The
chain-of-custody test `TestAthenaConnectionURIShapeIntegration` pins both the
secret-key round-trip and the `__extra__` round-trip.

## Fields

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `athena_lake`. Exported as `AIRFLOW_CONN_ATHENA_LAKE`. |
| Conn Type | yes | `athena`. |
| Login | optional | AWS access key id (explicit-credential path only). |
| Password | optional | AWS secret access key (explicit-credential path only). Encrypted at rest (ADR 0019). |
| Extra | yes | JSON: `{"region_name":"eu-central-1","schema_name":"default","work_group":"primary"}`. |

Leave Login/Password empty for keyless IAM (the recommended posture).

## Example DAG (doc-only)

The provider import goes **inside** the `@task` body — a top-level provider
import fails the compile.

```python
# dag.py
from airflow.sdk import DAG, task


@task
def query() -> str:
    from airflow.providers.amazon.aws.hooks.athena_sql import AthenaSQLHook

    hook = AthenaSQLHook(athena_conn_id="athena_lake")
    rows = hook.get_records("SELECT count(*) FROM events")
    return str(rows[0][0])


with DAG("athena_demo", schedule=None, catchup=False, tags=["example"]):
    query()
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: athena_demo
python_version: "3.12"
connectors:
  - athena
```

## Security notes

- **Keyless-first.** Prefer an IAM role on the task identity (instance profile,
  IRSA on EKS, or Workload Identity bridge) over storing an AWS secret key in the
  Connection ([ADR 0035](/project/adrs/0035-cloud-connector-auth-keyless-first/)). With
  keyless, Extra carries only the region/work-group — no secret.
- **Never log the URI** — it may carry the AWS secret key.
- Secrets travel only over the authenticated, TLS agent channel (ADR 0021).

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery.
- ADR 0035 — keyless-first cloud connector auth.
- [redshift.md](/connections/redshift/) — the other AWS data service in this batch.
