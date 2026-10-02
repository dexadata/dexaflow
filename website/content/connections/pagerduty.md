---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/pagerduty.html
# --- end AUTO redirect aliases ---
title: PagerDuty connection
linkTitle: PagerDuty
weight: 360
description: PagerDuty connection
---

Trigger PagerDuty incidents and alerts from a task over a managed Dexaflow
Connection. `PagerdutyHook` uses a REST API token for the REST API and an
Events-API routing key (integration key) for Events v2 alerts.

## Declare the provider

```yaml
# dexaflow.yaml
dag_id: pagerduty_alert
connectors:
  - pagerduty
```

## URI shape

```
pagerduty://:<api_token>@?__extra__=<json>
```

There is no host. The REST API token lives in Password (percent-escaped),
and the Events-API routing key rides in `Extra` under `__extra__`.

## Fields the UI asks for

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `pagerduty_default`. Exported as `AIRFLOW_CONN_PAGERDUTY_DEFAULT`. |
| Conn Type | yes | `pagerduty`. |
| Password | yes | The REST API token. Encrypted at rest (ADR 0019). |
| Extra | yes | JSON: `{"routing_key":"R0UTINGKEY"}` — the Events v2 integration key. |
| Host | no | Leave blank. |

## Example DAG

```python
# dag.py
from airflow.sdk import DAG, task


@task
def trigger():
    from airflow.providers.pagerduty.hooks.pagerduty_events import (
        PagerdutyEventsHook,
    )

    hook = PagerdutyEventsHook(pagerduty_events_conn_id="pagerduty_default")
    hook.create_event(
        summary="DAG failed",
        severity="critical",
        source="dexaflow",
    )


with DAG("pagerduty_alert", schedule=None, catchup=False, tags=["example"]):
    trigger()
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: pagerduty_alert
python_version: "3.12"
connectors:
  - pagerduty
connections:
  - pagerduty_default
```

## Security notes

- **Two distinct secrets**: the REST API token (account-scoped, in
  Password) and the routing key (service-scoped, in `Extra`). Use the
  routing key for alerts so a leak is blast-radius-limited to one service.
- **Rotate tokens** in PagerDuty user/account settings; the routing key
  rotates by recreating the integration.
- Never log `AIRFLOW_CONN_PAGERDUTY_DEFAULT`.

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery (`AIRFLOW_CONN_<CONN_ID>`).
- `TestPagerdutyConnectionURIShapeIntegration` — chain-of-custody delivery test.
