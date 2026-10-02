---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/redshift.html
# --- end AUTO redirect aliases ---
title: Amazon Redshift connection
linkTitle: Amazon Redshift
weight: 420
description: Amazon Redshift connection
---

Connect a task to an Amazon Redshift cluster (or Redshift Serverless) over a
managed Leoflow Connection. Redshift speaks the Postgres wire protocol, so the
Connection has the same host-bearing shape as Postgres.

## Declare the provider

Each DAG image bundles only the providers it declares. Add Redshift in
`dexaflow.yaml`:

```yaml
# dexaflow.yaml
dag_id: redshift_demo
connectors:
  - redshift
```

## URI shape

```
redshift://<login>:<password>@<host>:<port>/<schema>
```

The control plane builds this URI from the Connection's fields and percent-escapes
reserved characters in the password (e.g. `@` → `%40`). The receiving
`RedshiftSQLHook` un-escapes back to the original. The chain-of-custody test
`TestRedshiftConnectionURIShapeIntegration` pins the round-trip, including a
password with reserved characters.

## Fields

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `redshift_dw`. Exported as `AIRFLOW_CONN_REDSHIFT_DW`. |
| Conn Type | yes | `redshift`. |
| Host | yes | Cluster endpoint, e.g. `mycluster.abc123.eu-central-1.redshift.amazonaws.com`. |
| Port | optional | Defaults to `5439`. |
| Login | yes* | DB user. Omit when using IAM auth (keyless — preferred). |
| Password | yes* | DB password. Stored encrypted at rest (ADR 0019). Omit for IAM auth. |
| Schema | optional | The database name. |
| Extra | optional | JSON, e.g. `{"sslmode":"require"}` or IAM hints (`{"iam":true,"cluster_identifier":"mycluster"}`). |

\* Login/Password are only needed for the password auth path. **Keyless-first
(IAM role, [ADR 0035](/project/adrs/0035-cloud-connector-auth-keyless-first/))** is the
recommended posture: attach an IAM role to the task identity and let the hook
mint temporary credentials.

## Example DAG (doc-only)

The provider import goes **inside** the `@task` body — a top-level provider
import runs at parse time and fails the compile (the parser image doesn't carry
provider deps).

```python
# dag.py
from airflow.sdk import DAG, task


@task
def count_rows() -> int:
    from airflow.providers.amazon.aws.hooks.redshift_sql import RedshiftSQLHook

    hook = RedshiftSQLHook(redshift_conn_id="redshift_dw")
    rows = hook.get_records("SELECT count(*) FROM analytics.events")
    return int(rows[0][0])


with DAG("redshift_demo", schedule=None, catchup=False, tags=["example"]):
    count_rows()
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: redshift_demo
python_version: "3.12"
connectors:
  - redshift
```

## Security notes

- **Keyless-first.** Prefer an IAM role on the task identity over a stored
  password ([ADR 0035](/project/adrs/0035-cloud-connector-auth-keyless-first/)). The
  Connection then carries only the host + IAM hints, no secret.
- **TLS in transit.** Pass `sslmode=require` (or `verify-full`) in **Extra**.
- **Never log the URI** — it carries the password. Log host + login only.
- Secrets travel only over the authenticated, TLS agent channel (ADR 0021).

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery.
- ADR 0035 — keyless-first cloud connector auth.
- [athena.md](/connections/athena/) — the other AWS data service in this batch.
