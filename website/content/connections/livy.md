---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/livy.html
# --- end AUTO redirect aliases ---
title: Apache Livy connection
linkTitle: Apache Livy
weight: 280
description: Apache Livy connection
---

Submit Spark batches over the Apache Livy REST API from a task via a managed Leoflow
Connection and Airflow's `LivyHook`. The conn_type is `livy`. A connection carries the
**host:port** plus credentials.

## Declare the provider

```yaml
# dexaflow.yaml
dag_id: livy_batch
connectors:
  - livy
```

## Fields the UI asks for

| Field | Where it goes | Notes |
|---|---|---|
| Conn Id | — | e.g. `livy_default`. |
| Host | host | The Livy server host, e.g. `livy.example.com`. |
| Port | port | Default `8998`. |
| Login | login | The Livy user (when auth is enabled). |
| Password | password | Encrypted at rest (ADR 0019). |

The host:port + user + password round-trip (reserved characters) is pinned by
`TestLivyConnectionURIShapeIntegration`.

## Example DAG (copy-paste)

Docs-only recipe (needs a real Livy server). The hook is imported **inside** the
task — a top-level provider import fails the compile (see
[Installing a connector's provider](/connections/#installing-a-connectors-provider)).

```python
# dag.py
from __future__ import annotations

from airflow.sdk import DAG, task


@task
def submit() -> None:
    from airflow.providers.apache.livy.hooks.livy import LivyHook

    hook = LivyHook(livy_conn_id="livy_default")
    print("submit: posting batch via LivyHook(livy_default)")
    batch_id = hook.post_batch(file="s3://my-bucket/jobs/etl.py")
    print(f"submit: started batch {batch_id}")


with DAG("livy_batch", schedule=None, catchup=False, tags=["example"]):
    submit()
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: livy_batch
description: Submit a Spark batch via LivyHook.
owner: examples
tags:
  - example
python_version: "3.11"
connectors:
  - livy
```

### Run it

1. **Admin → Connections → +**, type `livy`. Set Host, Port, and (if auth is enabled)
   Login and Password.
2. `leoflow lite path/to/this/dag` → trigger `livy_batch`.

## Security notes

- **Front Livy with auth** (Kerberos / a gateway) in production; the credentials are
  encrypted at rest.
- **Never `print()` the URI** — it carries the password.

## Related

- `docs/connections/index.md` — *Installing a connector's provider*.
- ADR 0019 / 0021 — secret encryption + agent delivery.
