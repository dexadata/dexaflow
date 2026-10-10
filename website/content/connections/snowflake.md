---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/snowflake.html
# --- end AUTO redirect aliases ---
title: Snowflake connection
linkTitle: Snowflake
weight: 470
description: Snowflake connection
---

Connect a task to Snowflake via a managed Dexaflow Connection and Airflow's
`SnowflakeHook`. Unlike the SQL connectors, a Snowflake connection has **no
meaningful host:port** — its defining fields (account, warehouse, database, role,
region) live in **Extra**, and the connection form renders them as labeled fields
(ADR 0039).

## Declare the provider

One line of `connectors:` sugar installs `apache-airflow-providers-snowflake`
(which pulls the Snowflake driver):

```yaml
# dexaflow.yaml
dag_id: snowflake_load
connectors:
  - snowflake
```

## Fields the UI asks for

| Field | Where it goes | Notes |
|---|---|---|
| Conn Id | — | e.g. `snowflake_default`. Exported as `AIRFLOW_CONN_SNOWFLAKE_DEFAULT`. |
| Login | login | The Snowflake user. |
| Password | password | Stored encrypted at rest (ADR 0019). |
| Schema | schema | Default schema, e.g. `PUBLIC`. |
| **Account** | extra | e.g. `xy12345.eu-central-1`. The form renders this as a labeled field. |
| **Warehouse** | extra | e.g. `COMPUTE_WH`. |
| **Database** | extra | e.g. `ANALYTICS`. |
| **Role** | extra | e.g. `TRANSFORMER`. |
| **Region** | extra | e.g. `eu-central-1` (when not encoded in the account). |

The account/warehouse/role fields are delivered to the task inside the
`AIRFLOW_CONN_*` URI under `__extra__`; `SnowflakeHook` reads them from
`extra_dejson`. The round-trip (password with reserved characters + the Extra
blob) is pinned by `TestSnowflakeConnectionURIShapeIntegration`.

## dbt auth: password or key-pair (service account)

When a dbt task uses this connection, Dexaflow generates its `profiles.yml` from the
connection at runtime. Two auth modes are supported:

| Mode | What to set in Extra | dbt profile emitted |
|---|---|---|
| **Password** (default) | the password in Password | `password: <pw>` |
| **Key-pair** (service account) | `private_key_content` (inline PEM) **or** `private_key_file` (path), plus optional `private_key_passphrase` | `private_key` / `private_key_path` (+ `private_key_passphrase`) |

Key-pair is selected whenever a private key is present and takes precedence over the
password (the two are mutually exclusive, so exactly one lands in the profile;
`private_key_content` and `private_key_file` cannot both be set). Inline keys are
emitted as-is — provide the full PEM (with the `-----BEGIN … -----` armor). The key
is part of Extra, encrypted at rest exactly like the password. Prefer key-pair for
automation — Snowflake is deprecating single-factor password auth for programmatic
access.

## Example DAG (copy-paste)

This recipe lives only here in the docs (it needs a real Snowflake account, so it
is not shipped as a downloadable example). The hook is imported **inside** the
task — a top-level provider import fails the compile (see
[Installing a connector's provider](/connections/#installing-a-connectors-provider)).

```python
# dag.py
from __future__ import annotations

from airflow.sdk import DAG, task


@task
def compute() -> list[tuple]:
    return [(f"cat_{i}", (i * 7) % 100) for i in range(20)]


@task
def load(rows: list[tuple]) -> None:
    from airflow.providers.snowflake.hooks.snowflake import SnowflakeHook

    hook = SnowflakeHook(snowflake_conn_id="snowflake_default")
    print("load: connecting via SnowflakeHook(snowflake_default)")
    hook.run("CREATE TABLE IF NOT EXISTS example_load (name string, score int)")
    hook.run("TRUNCATE example_load")
    hook.insert_rows(table="example_load", rows=rows, target_fields=["name", "score"])
    count = hook.get_first("SELECT COUNT(*) FROM example_load")[0]
    print(f"load: {count} rows in example_load")


with DAG("snowflake_load", schedule=None, catchup=False, tags=["example"]):
    load(compute())
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: snowflake_load
description: Load rows into Snowflake via SnowflakeHook.
owner: examples
tags:
  - example
python_version: "3.11"
connectors:
  - snowflake
```

### Run it

1. Create the Connection in **Admin → Connections → +**, type `snowflake`. Fill
   Login, Password, Schema, and the Account/Warehouse/Database/Role fields.
2. `dexaflow lite path/to/this/dag` → open `snowflake_load` → **Trigger DAG**.
3. The task log reports `load: connecting via SnowflakeHook(snowflake_default)`
   then the row count.

## Security notes

- **Key-pair auth**: prefer it over passwords. Put the private key in Extra
  (`private_key_file` / `private_key_content`); the field is encrypted at rest.
- **Least privilege**: the `role` should be a dedicated transform role, not
  `ACCOUNTADMIN`.
- **Never `print()` the URI** — it carries the password / key. Log host-free
  identifiers (warehouse, role) only.

## Related

- `docs/connections/index.md` — *Installing a connector's provider* (sugar + the
  connector → package table).
- ADR 0039 — generated connector catalog (the form renders the account/warehouse
  fields).
- ADR 0019 / 0021 — secret encryption + agent delivery.
