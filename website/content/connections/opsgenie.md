---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/opsgenie.html
# --- end AUTO redirect aliases ---
title: Opsgenie connection
linkTitle: Opsgenie
weight: 340
description: Opsgenie connection
---

Send alerts to Opsgenie from a task over a managed Dexaflow Connection. The
host and API key are encrypted at rest and delivered to the task as
`AIRFLOW_CONN_<CONN_ID>`.

## Declare the provider

```yaml
# dexaflow.yaml
dag_id: opsgenie_alert
connectors:
  - opsgenie
```

## URI shape

```
opsgenie://:<api-key>@<host>
```

Opsgenie authenticates with an API key, not a login: the key lives in the
**Password** field, so the URI has an empty username. The control plane
percent-escapes the key; `OpsgenieAlertHook` un-escapes it back. There is no
port.

## Fields the UI asks for

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `opsgenie_default`. Exported as `AIRFLOW_CONN_OPSGENIE_DEFAULT`. |
| Conn Type | yes | `opsgenie`. |
| Host | yes | The API host, e.g. `api.opsgenie.com` (or `api.eu.opsgenie.com`). |
| Login | no | Leave blank — Opsgenie uses an API key, not a username. |
| Password | yes | The Opsgenie API key. Stored encrypted at rest (ADR 0019). |
| Extra | optional | JSON for hook-specific options. |

## Example DAG

The hook is imported inside the task body so DAG parsing stays import-light.

```python
# dag.py
from airflow.sdk import DAG, task


@task
def page():
    from airflow.providers.opsgenie.hooks.opsgenie import OpsgenieAlertHook

    hook = OpsgenieAlertHook(opsgenie_conn_id="opsgenie_default")
    hook.create_alert(payload={"message": "Dexaflow pipeline failed"})


with DAG("opsgenie_alert", schedule=None, catchup=False, tags=["example"]):
    page()
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: opsgenie_alert
python_version: "3.11"
connectors:
  - opsgenie
```

## Security notes

- **API key is the credential**: store it in Password, never in Extra in
  plaintext form you log. The key grants alert-create rights.
- **Secrets in logs**: never `print()` the URI — it carries the API key.
- **gRPC channel (agent ↔ control plane)**: secrets are served only over an
  authenticated channel (ADR 0021); Pro must run with TLS.

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery.
- #67 — connectors umbrella.
- #138 — the chain-of-custody contract test this page documents.
