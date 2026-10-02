---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/zendesk.html
# --- end AUTO redirect aliases ---
title: Zendesk connection
linkTitle: Zendesk
weight: 540
description: Zendesk connection
---

Connect a task to the Zendesk Support API over a managed Leoflow Connection.
The subdomain host, agent email, API token, and an Extra blob are encrypted at
rest and delivered to the task as `AIRFLOW_CONN_<CONN_ID>`.

## Declare the provider

```yaml
# dexaflow.yaml
dag_id: zendesk_export
connectors:
  - zendesk
```

## URI shape

```
zendesk://<login>:<password>@<host>?__extra__=<json>
```

The **Login** is the agent email and the **Password** is the API token; the
control plane percent-escapes the token and `ZendeskHook` un-escapes it. The
Extra blob (`token`, `use_token`) rides in `__extra__`.

## Fields the UI asks for

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `zendesk_default`. Exported as `AIRFLOW_CONN_ZENDESK_DEFAULT`. |
| Conn Type | yes | `zendesk`. |
| Host | yes | The subdomain host, e.g. `company.zendesk.com`. |
| Login | yes | The agent email, e.g. `agent@example.com`. |
| Password | yes | The API token. Stored encrypted at rest (ADR 0019). |
| Extra | optional | JSON, e.g. `{"token":"<api-token>","use_token":true}`. |

## Example DAG

The hook is imported inside the task body so DAG parsing stays import-light.

```python
# dag.py
from airflow.sdk import DAG, task


@task
def fetch():
    from airflow.providers.zendesk.hooks.zendesk import ZendeskHook

    hook = ZendeskHook(zendesk_conn_id="zendesk_default")
    return hook.get_ticket(ticket_id=1)


with DAG("zendesk_export", schedule=None, catchup=False, tags=["example"]):
    fetch()
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: zendesk_export
python_version: "3.11"
connectors:
  - zendesk
```

## Security notes

- **Token auth**: set `use_token: true` in Extra and store the token in
  Password (or in Extra's `token`). The token grants API access for the agent.
- **Secrets in logs**: never `print()` the URI — it carries the API token.
- **gRPC channel (agent ↔ control plane)**: secrets are served only over an
  authenticated channel (ADR 0021); Pro must run with TLS.

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery.
- #67 — connectors umbrella.
- #138 — the chain-of-custody contract test this page documents.
