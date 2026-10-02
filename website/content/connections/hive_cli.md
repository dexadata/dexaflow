---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/hive_cli.html
# --- end AUTO redirect aliases ---
title: Hive CLI connection
linkTitle: Hive CLI
weight: 210
description: Hive CLI connection
---

Run HiveQL through the Hive CLI / Beeline from a task via a managed Dexaflow Connection
and Airflow's `HiveCliHook`. The conn_type is `hive_cli`. A connection carries the
**host:port** plus credentials, and beeline/kerberos tuning in **Extra**.

> The conn_type has an underscore, so the delivered URI scheme is normalized to
> `hive-cli` (Airflow does the same).

## Declare the provider

```yaml
# dexaflow.yaml
dag_id: hive_cli_run
connectors:
  - hive_cli
```

## Fields the UI asks for

| Field | Where it goes | Notes |
|---|---|---|
| Conn Id | — | e.g. `hive_cli_default`. |
| Host | host | The Hive host, e.g. `hive.example.com`. |
| Port | port | Default `10000`. |
| Login | login | The Hive user. |
| Password | password | Encrypted at rest (ADR 0019). |
| Extra | extra | e.g. `{"use_beeline": true, "principal": "hive/_HOST@REALM"}`. |

The host + password + Extra round-trip under the normalized `hive-cli` scheme is
pinned by `TestHiveCliConnectionURIShapeIntegration`.

## Example DAG (copy-paste)

Docs-only recipe (needs a real Hive cluster). The hook is imported **inside** the
task — a top-level provider import fails the compile (see
[Installing a connector's provider](/connections/#installing-a-connectors-provider)).

```python
# dag.py
from __future__ import annotations

from airflow.sdk import DAG, task


@task
def load() -> None:
    from airflow.providers.apache.hive.hooks.hive import HiveCliHook

    hook = HiveCliHook(hive_cli_conn_id="hive_cli_default")
    print("load: running HiveQL via HiveCliHook(hive_cli_default)")
    hook.run_cli("INSERT OVERWRITE TABLE summary SELECT k, COUNT(*) FROM events GROUP BY k")
    print("load: summary refreshed")


with DAG("hive_cli_run", schedule=None, catchup=False, tags=["example"]):
    load()
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: hive_cli_run
description: Run HiveQL via HiveCliHook.
owner: examples
tags:
  - example
python_version: "3.11"
connectors:
  - hive_cli
```

### Run it

1. **Admin → Connections → +**, type `hive_cli`. Set Host, Port, Login, Password, and
   any beeline/kerberos hints in Extra.
2. `dexaflow lite path/to/this/dag` → trigger `hive_cli_run`.

## Security notes

- **Kerberos**: prefer a keytab/principal over a password for production clusters.
- **Never `print()` the URI** — it carries the password.

## Related

- `docs/connections/index.md` — *Installing a connector's provider*.
- ADR 0019 / 0021 — secret encryption + agent delivery.
