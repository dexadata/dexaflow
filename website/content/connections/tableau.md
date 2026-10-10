---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/tableau.html
# --- end AUTO redirect aliases ---
title: Tableau connection
linkTitle: Tableau
weight: 500
description: Tableau connection
---

Connect a task to Tableau Server or Tableau Cloud to refresh extracts,
publish workbooks, or query metadata over a managed Dexaflow Connection.
`TableauHook` signs in to the REST API.

## Declare the provider

```yaml
# dexaflow.yaml
dag_id: tableau_signin
connectors:
  - tableau
```

## URI shape

```
tableau://<login>:<password>@<host>/<site>
```

The control plane builds this from the Connection's fields and exports it
as `AIRFLOW_CONN_<CONN_ID>`. The site id is carried in Schema; the password
is percent-escaped.

## Fields the UI asks for

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `tableau_default`. Exported as `AIRFLOW_CONN_TABLEAU_DEFAULT`. |
| Conn Type | yes | `tableau`. |
| Host | yes | Tableau Server URL host, e.g. `tableau.example.com`. |
| Login | yes | Tableau username, e.g. `etl_user`. |
| Password | yes | Account password. Encrypted at rest (ADR 0019). |
| Schema | optional | The site id, e.g. `default` (blank = the Default site). |
| Extra | optional | JSON: `{"token_name":"...","personal_access_token":"..."}` for PAT auth. |

## Example DAG

```python
# dag.py
from airflow.sdk import DAG, task


@task
def list_workbooks():
    from airflow.providers.tableau.hooks.tableau import TableauHook

    with TableauHook(tableau_conn_id="tableau_default") as hook:
        workbooks = hook.get_all(resource_name="workbooks")
        print("workbook count:", len(list(workbooks)))


with DAG("tableau_signin", schedule=None, catchup=False, tags=["example"]):
    list_workbooks()
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: tableau_signin
python_version: "3.12"
connectors:
  - tableau
connections:
  - tableau_default
```

## Security notes

- **Prefer personal access tokens** over username/password: put
  `token_name` + `personal_access_token` in `Extra`. PATs can be revoked
  independently and don't expose the account password.
- **TLS**: Tableau Server should be HTTPS; the host must present a valid
  cert.
- Never log `AIRFLOW_CONN_TABLEAU_DEFAULT`; it carries the password.

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery (`AIRFLOW_CONN_<CONN_ID>`).
- `TestTableauConnectionURIShapeIntegration` — chain-of-custody delivery test.
