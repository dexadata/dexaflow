---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/oracle.html
# --- end AUTO redirect aliases ---
title: Oracle connection
linkTitle: Oracle
weight: 350
description: Oracle connection
---

Connect a task to Oracle via a managed Dexaflow Connection and `OracleHook`. The
conn_type is `oracle`. It is the standard host:port + login/password shape; the
**Schema** field carries the service name / PDB.

## Declare the provider

```yaml
# dexaflow.yaml
dag_id: oracle_load
connectors:
  - oracle
```

## Fields the UI asks for

| Field | Notes |
|---|---|
| Host | DB host. |
| Port | Defaults to `1521`. |
| Login / Password | The Oracle user. Password encrypted at rest (ADR 0019). |
| Schema | The **service name** or PDB, e.g. `ORCLPDB1`. |
| Extra | `{"service_name": "..."}` or `{"sid": "..."}` when not using Schema. |

The host:port + login/password + schema round-trip is covered by the table-driven
`TestConnectionDeliveryChainOfCustodyIntegration` (oracle row).

## Example DAG (copy-paste)

Docs-only recipe (needs an Oracle instance). Hook imported **inside** the task.

```python
# dag.py
from __future__ import annotations

from airflow.sdk import DAG, task


@task
def load() -> None:
    from airflow.providers.oracle.hooks.oracle import OracleHook

    hook = OracleHook(oracle_conn_id="oracle_default")
    print("load: connecting via OracleHook(oracle_default)")
    hook.run("CREATE TABLE example_load (name VARCHAR2(50), score NUMBER)")
    hook.insert_rows("example_load", [("cat_0", 0), ("cat_1", 7)])
    print("load: ok")


with DAG("oracle_load", schedule=None, catchup=False, tags=["example"]):
    load()
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: oracle_load
description: Load rows into Oracle via OracleHook.
owner: examples
tags: [example]
python_version: "3.11"
connectors:
  - oracle
```

{{% alert title="Oracle Instant Client" color="info" %}}
`oracledb` runs in thin mode (no client needed) for most operations; some
features need the Oracle Instant Client in the image (`system_packages:` or a
custom base).
{{% /alert %}}

## Security notes

- **TLS in transit**: Oracle supports encrypted transport (TCPS / native
  network encryption). Configure it in the service descriptor or **Extra**;
  never run credentials over an unencrypted listener.
- **Least privilege**: connect as a dedicated schema user scoped to the objects
  the DAG needs — never `SYS`/`SYSTEM`.
- **Secrets in logs**: never `print()` the URI — it carries the password. Log
  the host + service name + login only.
- **gRPC channel (agent ↔ control plane)**: secrets are served only over an
  authenticated channel (ADR 0021); Pro must run with TLS.

## Related

- `docs/connections/index.md` — *Installing a connector's provider*.
- ADR 0019 / 0021 — secret encryption + agent delivery.
