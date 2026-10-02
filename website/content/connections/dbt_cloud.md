---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/dbt_cloud.html
# --- end AUTO redirect aliases ---
title: dbt Cloud connection
linkTitle: dbt Cloud
weight: 70
description: dbt Cloud connection
---

Trigger and monitor dbt Cloud jobs from a task via a managed Leoflow Connection and
the dbt Cloud provider hook. The conn_type is `dbt_cloud`. A connection carries the
**account id** (login) plus an **API token** (password) — there is no host, the hook
targets the dbt Cloud API.

> The conn_type has an underscore, so the delivered URI scheme is normalized to
> `dbt-cloud` (Airflow does the same).

## Declare the provider

```yaml
# dexaflow.yaml
dag_id: dbt_cloud_run
connectors:
  - dbt_cloud
```

## Fields the UI asks for

| Field | Where it goes | Notes |
|---|---|---|
| Conn Id | — | e.g. `dbt_cloud_default`. |
| Login | login | The dbt Cloud **account id**, e.g. `12345`. |
| Password | password | The **API token**. Encrypted at rest (ADR 0019). |

The token round-trip (reserved characters) under the normalized `dbt-cloud` scheme is
pinned by `TestDbtCloudConnectionURIShapeIntegration`.

## Example DAG (copy-paste)

Docs-only recipe (needs a real dbt Cloud account). The hook is imported **inside**
the task — a top-level provider import fails the compile (see
[Installing a connector's provider](/connections/#installing-a-connectors-provider)).

```python
# dag.py
from __future__ import annotations

from airflow.sdk import DAG, task


@task
def run_job() -> None:
    from airflow.providers.dbt.cloud.hooks.dbt import DbtCloudHook

    hook = DbtCloudHook(dbt_cloud_conn_id="dbt_cloud_default")
    print("run_job: triggering job via DbtCloudHook(dbt_cloud_default)")
    run = hook.trigger_job_run(job_id=1234, cause="Triggered by Leoflow")
    print(f"run_job: started run {run.json()['data']['id']}")


with DAG("dbt_cloud_run", schedule=None, catchup=False, tags=["example"]):
    run_job()
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: dbt_cloud_run
description: Trigger a dbt Cloud job via DbtCloudHook.
owner: examples
tags:
  - example
python_version: "3.11"
connectors:
  - dbt_cloud
```

### Run it

1. **Admin → Connections → +**, type `dbt_cloud`. Set the account id in Login and the
   API token in Password.
2. `leoflow lite path/to/this/dag` → trigger `dbt_cloud_run`.

## Security notes

- **Scope the API token** to the minimum permissions and rotate it regularly; it is
  encrypted at rest and never echoed back.
- **Never `print()` the URI** — it carries the token.

## Related

- `docs/connections/index.md` — *Installing a connector's provider*.
- ADR 0019 / 0021 — secret encryption + agent delivery.
