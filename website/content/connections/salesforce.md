---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/salesforce.html
# --- end AUTO redirect aliases ---
title: Salesforce connection
linkTitle: Salesforce
weight: 430
description: Salesforce connection
---

Connect a task to a Salesforce org to run SOQL queries and read/write
objects over a managed Leoflow Connection. `SalesforceHook` (built on
`simple-salesforce`) authenticates with username + password + security
token, or with a connected-app flow configured in `Extra`.

## Declare the provider

```yaml
# dexaflow.yaml
dag_id: salesforce_query
connectors:
  - salesforce
```

## URI shape

```
salesforce://<login>:<password>@?__extra__=<json>
```

There is **no host** — Salesforce is reached at the org's instance URL,
which lives in `Extra`. The username is in Login, the password in Password
(percent-escaped), and the security token, instance URL, and API version
ride in `Extra` under `__extra__`.

## Fields the UI asks for

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `salesforce_default`. Exported as `AIRFLOW_CONN_SALESFORCE_DEFAULT`. |
| Conn Type | yes | `salesforce`. |
| Login | yes | Salesforce username, e.g. `user@example.com`. |
| Password | yes | Account password. Encrypted at rest (ADR 0019). |
| Extra | yes | JSON: `{"instance_url":"https://x.my.salesforce.com","security_token":"...","version":"59.0"}`. |

## Example DAG

```python
# dag.py
from airflow.sdk import DAG, task


@task
def count_accounts():
    from airflow.providers.salesforce.hooks.salesforce import SalesforceHook

    hook = SalesforceHook(salesforce_conn_id="salesforce_default")
    result = hook.make_query("SELECT count() FROM Account")
    print("account count:", result["totalSize"])


with DAG("salesforce_query", schedule=None, catchup=False, tags=["example"]):
    count_accounts()
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: salesforce_query
python_version: "3.12"
connectors:
  - salesforce
connections:
  - salesforce_default
```

## Security notes

- **Security token rotation**: the token is invalidated when the password
  changes; update the Connection's `Extra` on every reset.
- **Connected apps**: prefer OAuth/JWT for production; set the consumer key
  and secret in `Extra` rather than storing a raw password.
- Never log `AIRFLOW_CONN_SALESFORCE_DEFAULT`; it carries the password and
  security token.

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery (`AIRFLOW_CONN_<CONN_ID>`).
- `TestSalesforceConnectionURIShapeIntegration` — chain-of-custody delivery test.
