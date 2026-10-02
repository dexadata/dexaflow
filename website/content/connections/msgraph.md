---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/msgraph.html
# --- end AUTO redirect aliases ---
title: Microsoft Graph connection
linkTitle: Microsoft Graph
weight: 300
description: Microsoft Graph connection
---

Call the Microsoft Graph API from a task via a managed Leoflow Connection and the Azure
provider's `KiotaRequestAdapterHook`. The conn_type is `msgraph`. A connection carries
the **client id** (login), the **client secret** (password), and the **tenant id** /
**api_version** in Extra.

## Declare the provider

```yaml
# dexaflow.yaml
dag_id: msgraph_users
connectors:
  - msgraph
```

## Fields the UI asks for

| Field | Where it goes | Notes |
|---|---|---|
| Conn Id | — | e.g. `msgraph_default`. |
| Login | login | The application (client) id. |
| Password | password | The **client secret**. Encrypted at rest (ADR 0019). |
| Extra | extra | e.g. `{"tenant_id": "00000000-1111-2222-3333-444444444444", "api_version": "v1.0"}`. |

The client secret (reserved characters) + tenant/api_version Extra round-trip is pinned
by `TestMSGraphConnectionURIShapeIntegration`.

## Example DAG (copy-paste)

Docs-only recipe (needs a real Azure AD app). The hook is imported **inside** the
task — a top-level provider import fails the compile (see
[Installing a connector's provider](/connections/#installing-a-connectors-provider)).

```python
# dag.py
from __future__ import annotations

from airflow.sdk import DAG, task


@task
def list_users() -> None:
    from airflow.providers.microsoft.azure.hooks.msgraph import KiotaRequestAdapterHook

    hook = KiotaRequestAdapterHook(conn_id="msgraph_default")
    print("list_users: building Graph adapter via KiotaRequestAdapterHook(msgraph_default)")
    adapter = hook.get_conn()
    print(f"list_users: adapter ready ({adapter})")


with DAG("msgraph_users", schedule=None, catchup=False, tags=["example"]):
    list_users()
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: msgraph_users
description: Call Microsoft Graph via KiotaRequestAdapterHook.
owner: examples
tags:
  - example
python_version: "3.11"
connectors:
  - msgraph
```

### Run it

1. **Admin → Connections → +**, type `msgraph`. Set the client id in Login, the client
   secret in Password, and the tenant id / api_version in Extra.
2. `leoflow lite path/to/this/dag` → trigger `msgraph_users`.

## Security notes

- **Scope the app registration** to the minimum Graph permissions and rotate the client
  secret regularly; it is encrypted at rest and never echoed back.
- **Never `print()` the URI** — it carries the client secret.

## Related

- `docs/connections/index.md` — *Installing a connector's provider*.
- ADR 0019 / 0021 — secret encryption + agent delivery.
