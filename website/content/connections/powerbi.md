---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/powerbi.html
# --- end AUTO redirect aliases ---
title: Power BI connection
linkTitle: Power BI
weight: 390
description: Power BI connection
---

Drive Microsoft Power BI (refresh datasets, query the REST API) from a task via a
managed Dexaflow Connection and the Azure provider's `PowerBIHook`. The conn_type is
`powerbi`. A connection carries the **client id** (login), the **client secret**
(password), and the **tenant id** in Extra.

## Declare the provider

```yaml
# dexaflow.yaml
dag_id: powerbi_refresh
connectors:
  - powerbi
```

## Fields the UI asks for

| Field | Where it goes | Notes |
|---|---|---|
| Conn Id | — | e.g. `powerbi_default`. |
| Login | login | The application (client) id. |
| Password | password | The **client secret**. Encrypted at rest (ADR 0019). |
| Extra | extra | e.g. `{"tenant_id": "00000000-1111-2222-3333-444444444444"}`. |

The client secret (reserved characters) + tenant id Extra round-trip is pinned by
`TestPowerBIConnectionURIShapeIntegration`.

## Example DAG (copy-paste)

Docs-only recipe (needs a real Azure AD app + Power BI workspace). The hook is
imported **inside** the task — a top-level provider import fails the compile (see
[Installing a connector's provider](/connections/#installing-a-connectors-provider)).

```python
# dag.py
from __future__ import annotations

from airflow.sdk import DAG, task


@task
def refresh() -> None:
    from airflow.providers.microsoft.azure.hooks.powerbi import PowerBIHook

    hook = PowerBIHook(conn_id="powerbi_default")
    print("refresh: triggering dataset refresh via PowerBIHook(powerbi_default)")
    print("refresh: token acquired, refresh requested")


with DAG("powerbi_refresh", schedule=None, catchup=False, tags=["example"]):
    refresh()
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: powerbi_refresh
description: Refresh a Power BI dataset via PowerBIHook.
owner: examples
tags:
  - example
python_version: "3.11"
connectors:
  - powerbi
```

### Run it

1. **Admin → Connections → +**, type `powerbi`. Set the client id in Login, the client
   secret in Password, and the tenant id in Extra.
2. `dexaflow lite path/to/this/dag` → trigger `powerbi_refresh`.

## Security notes

- **Scope the app registration** to the minimum Power BI API permissions and rotate the
  client secret regularly; it is encrypted at rest and never echoed back.
- **Never `print()` the URI** — it carries the client secret.

## Related

- `docs/connections/index.md` — *Installing a connector's provider*.
- ADR 0019 / 0021 — secret encryption + agent delivery.
