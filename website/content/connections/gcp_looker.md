---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/gcp_looker.html
# --- end AUTO redirect aliases ---
title: Google Looker connection
linkTitle: Google Looker
weight: 140
description: Google Looker connection
---

Run Looker queries and trigger PDTs from a task via a managed Leoflow Connection and the
Google provider's `LookerHook`. The conn_type is `gcp_looker`. A connection has **no
password** — the API3 `client_id` and `client_secret` live in **Extra**.

> The conn_type has an underscore, so the delivered URI scheme is normalized to
> `gcp-looker` (Airflow does the same).

## Declare the provider

```yaml
# dexaflow.yaml
dag_id: looker_pdt
connectors:
  - gcp_looker
```

## Fields the UI asks for

| Field | Where it goes | Notes |
|---|---|---|
| Conn Id | — | e.g. `gcp_looker_default`. |
| Host | host | The Looker instance base URL, e.g. `https://my.looker.com`. |
| Extra | extra | e.g. `{"client_id": "x", "client_secret": "y"}` (API3 credentials). Encrypted at rest (ADR 0019). |

The `client_id` / `client_secret` Extra round-trip under the normalized `gcp-looker`
scheme is pinned by `TestGCPLookerConnectionURIShapeIntegration`.

## Example DAG (copy-paste)

Docs-only recipe (needs a real Looker instance). The hook is imported **inside** the
task — a top-level provider import fails the compile (see
[Installing a connector's provider](/connections/#installing-a-connectors-provider)).

```python
# dag.py
from __future__ import annotations

from airflow.sdk import DAG, task


@task
def build_pdt() -> None:
    from airflow.providers.google.cloud.hooks.looker import LookerHook

    hook = LookerHook(looker_conn_id="gcp_looker_default")
    print("build_pdt: starting PDT build via LookerHook(gcp_looker_default)")
    materialization_id = hook.start_pdt_build(model="my_model", view="my_view")
    print(f"build_pdt: materialization {materialization_id}")


with DAG("looker_pdt", schedule=None, catchup=False, tags=["example"]):
    build_pdt()
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: looker_pdt
description: Build a Looker PDT via LookerHook.
owner: examples
tags:
  - example
python_version: "3.11"
connectors:
  - gcp_looker
```

### Run it

1. **Admin → Connections → +**, type `gcp_looker`. Set the instance URL in Host and the
   API3 `client_id` / `client_secret` in Extra.
2. `leoflow lite path/to/this/dag` → trigger `looker_pdt`.

## Security notes

- **Scope the API3 credentials** to a dedicated service account and rotate them
  regularly; the Extra is encrypted at rest and never echoed back.
- **Never `print()` the URI** — it carries the API3 secret.

## Related

- `docs/connections/index.md` — *Installing a connector's provider*.
- ADR 0019 / 0021 — secret encryption + agent delivery.
