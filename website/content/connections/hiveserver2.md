---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/hiveserver2.html
# --- end AUTO redirect aliases ---
title: HiveServer2 connection
linkTitle: HiveServer2
weight: 220
description: HiveServer2 connection
---

Query Apache Hive over HiveServer2 from a task via a managed Dexaflow Connection and
Airflow's `HiveServer2Hook`. The conn_type is `hiveserver2`. A connection carries the
**host:port** plus credentials and the default **database** (schema).

## Declare the provider

```yaml
# dexaflow.yaml
dag_id: hive_query
connectors:
  - hiveserver2
```

## Fields the UI asks for

| Field | Where it goes | Notes |
|---|---|---|
| Conn Id | — | e.g. `hiveserver2_default`. |
| Host | host | The HiveServer2 host, e.g. `hive.example.com`. |
| Port | port | Default `10000`. |
| Login | login | The Hive user. |
| Password | password | Encrypted at rest (ADR 0019). |
| Schema | schema | Default database, e.g. `default`. |

The host:port + user + password round-trip (reserved characters) is pinned by
`TestHiveServer2ConnectionURIShapeIntegration`.

## Example DAG (copy-paste)

Docs-only recipe (needs a real Hive cluster). The hook is imported **inside** the
task — a top-level provider import fails the compile (see
[Installing a connector's provider](/connections/#installing-a-connectors-provider)).

```python
# dag.py
from __future__ import annotations

from airflow.sdk import DAG, task


@task
def query() -> None:
    from airflow.providers.apache.hive.hooks.hive import HiveServer2Hook

    hook = HiveServer2Hook(hiveserver2_conn_id="hiveserver2_default")
    print("query: connecting via HiveServer2Hook(hiveserver2_default)")
    rows = hook.get_records("SELECT COUNT(*) FROM my_table")
    print(f"query: my_table has {rows[0][0]} rows")


with DAG("hive_query", schedule=None, catchup=False, tags=["example"]):
    query()
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: hive_query
description: Query Hive via HiveServer2Hook.
owner: examples
tags:
  - example
python_version: "3.11"
connectors:
  - hiveserver2
```

### Run it

1. **Admin → Connections → +**, type `hiveserver2`. Set Host, Port, Login, Password,
   and the default Schema.
2. `dexaflow lite path/to/this/dag` → trigger `hive_query`.

## Security notes

- **Least privilege**: the Hive user should be scoped to the tables it reads.
- **Never `print()` the URI** — it carries the password.

## Related

- `docs/connections/index.md` — *Installing a connector's provider*.
- ADR 0019 / 0021 — secret encryption + agent delivery.
