---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/databricks.html
# --- end AUTO redirect aliases ---
title: Databricks connection
linkTitle: Databricks
weight: 50
description: Databricks connection
---

Trigger Databricks jobs / SQL from a task via a managed Dexaflow Connection and the
Databricks provider hooks. The conn_type is `databricks`. A connection carries the
**workspace host** plus a **Personal Access Token** (PAT).

## Declare the provider

```yaml
# dexaflow.yaml
dag_id: databricks_job
connectors:
  - databricks
```

## Fields the UI asks for

| Field | Where it goes | Notes |
|---|---|---|
| Conn Id | — | e.g. `databricks_default`. |
| Host | host | The workspace URL, e.g. `dbc-12345678-9abc.cloud.databricks.com`. |
| Password | password | The **Personal Access Token** (`dapi…`). Encrypted at rest (ADR 0019). |
| Extra | extra | Optional, e.g. `{"http_path": "/sql/1.0/warehouses/abc123"}` for SQL warehouses. |

The PAT round-trip (reserved characters) + host + `http_path` Extra are pinned by
`TestDatabricksConnectionURIShapeIntegration`.

## dbt auth: PAT or OAuth M2M (service principal)

When a dbt task uses this connection, Dexaflow generates its `profiles.yml` from the
connection at runtime (nothing baked in the image). Two auth modes are supported:

| Mode | What to set in Extra | dbt profile emitted |
|---|---|---|
| **PAT** (default) | `http_path`, and the token in Password (or `token` in Extra) | `token: <pat>` |
| **OAuth M2M** (service principal) | `http_path`, `client_id`, `client_secret` (and optionally `auth_type: oauth`) | `auth_type: oauth` + `client_id` + `client_secret` |

```json
// Extra for OAuth M2M — Databricks' recommended auth for automation/CI
{"http_path": "/sql/1.0/warehouses/abc", "client_id": "…", "client_secret": "…", "auth_type": "oauth"}
```

OAuth M2M is selected whenever `client_id`/`client_secret` are present (or
`auth_type: oauth` is explicit) and takes precedence over a PAT; the two are
mutually exclusive, so exactly one lands in the profile. `client_secret` is part
of Extra, which is encrypted at rest exactly like the PAT.

## Example DAG (copy-paste)

Docs-only recipe (needs a real Databricks workspace). The hook is imported
**inside** the task.

```python
# dag.py
from __future__ import annotations

from airflow.sdk import DAG, task


@task
def run_now() -> None:
    from airflow.providers.databricks.hooks.databricks import DatabricksHook

    hook = DatabricksHook(databricks_conn_id="databricks_default")
    print("run_now: triggering job via DatabricksHook(databricks_default)")
    run_id = hook.run_now({"job_id": 1234})
    state = hook.get_run_state(run_id)
    print(f"run_now: run {run_id} state {state.life_cycle_state}")


with DAG("databricks_job", schedule=None, catchup=False, tags=["example"]):
    run_now()
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: databricks_job
description: Trigger a Databricks job via DatabricksHook.
owner: examples
tags:
  - example
python_version: "3.11"
connectors:
  - databricks
```

### Run it

1. **Admin → Connections → +**, type `databricks`. Set Host (workspace URL) and
   the PAT in Password.
2. `dexaflow lite path/to/this/dag` → trigger `databricks_job`.

## Security notes

- **Scope the PAT** to the workspace and rotate it regularly; it is encrypted at
  rest and never echoed back.
- Prefer a **service principal** over a personal identity for production jobs — for
  dbt, that means OAuth M2M (`client_id`/`client_secret`) rather than a PAT (see
  *dbt auth* above).
- **Never `print()` the URI** — it carries the PAT.

## Related

- `docs/connections/index.md` — *Installing a connector's provider*.
- ADR 0019 / 0021 — secret encryption + agent delivery.
