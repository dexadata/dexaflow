# postgres_hook_load — load Postgres via PostgresHook + the `connectors:` sugar

The sibling of [`postgres_load`](../postgres_load/): same job (write 20 rows into
an external Postgres), but it uses **Airflow's `PostgresHook`** instead of raw
`psycopg2`, and declares the provider with **one line** of `connectors:` sugar
instead of pinning the driver in `dependencies:`.

It is the end-to-end proof of ADR 0038: the sugar installs the provider, the hook
imports and runs inside the task, and the managed Connection is delivered as
`AIRFLOW_CONN_PG_TARGET`.

## The two ways, side by side

| | `postgres_load` (raw) | `postgres_hook_load` (this) |
|---|---|---|
| dexaflow.yaml | `dependencies: [psycopg2-binary==2.9.10]` | `connectors: [postgres]` |
| In the task | `import psycopg2; psycopg2.connect(dsn)` | `PostgresHook(postgres_conn_id="pg_target")` |
| Needs the Connection? | No (falls back to a local DSN) | **Yes** — the hook resolves `pg_target` |

Both are valid. Use the hook when you want Airflow's hook ergonomics
(`insert_rows`, `get_records`, retries); use raw when you want zero Airflow
surface in the task.

## Why the hook import is inside the task

```python
@task
def load(rows):
    from airflow.providers.postgres.hooks.postgres import PostgresHook  # here, not top level
    ...
```

Leoflow parses your DAG **without providers installed** (it only needs the DAG's
shape). A provider import at the module top level therefore fails the compile —
with an actionable message telling you to move it into the task and declare it via
`connectors:`. Inside the `@task` body it is never executed at parse time, and at
runtime the provider is installed (the `connectors:` line above), so the import
works. See `docs/connections/index.md` → *Installing a connector's provider*.

## How to run it (Lima / subprocess executor)

### 1. Spin up a target Postgres

```sh
docker run --rm -d --name leoflow-warehouse \
  -e POSTGRES_PASSWORD=etl \
  -e POSTGRES_DB=warehouse \
  -p 55432:5432 \
  postgres:16
```

### 2. Create the Connection in the UI

Open `http://localhost:8088` → **Admin → Connections → +**.

| Field | Value |
|---|---|
| Conn Id | `pg_target` |
| Conn Type | `postgres` |
| Host | `localhost` (host) or `host.k3d.internal` (k3d) |
| Schema | `warehouse` |
| Login | `postgres` |
| Password | `etl` |
| Port | `55432` |

Unlike `postgres_load`, this DAG has **no fallback DSN** — `PostgresHook`
resolves `pg_target`, so the Connection must exist or the task fails with a
clear "The conn_id `pg_target` isn't defined" error.

### 3. Trigger the DAG

```sh
leoflow lite path/to/this/example
```

In the UI: open `postgres_hook_load` → **Trigger DAG**.

### 4. Verify

```sh
docker exec leoflow-warehouse psql -U postgres -d warehouse \
  -c "SELECT count(*), min(name), max(score) FROM example_load"
```

Expected: 20 rows, `min(name)` = `cat_0`, scores 0–99. The task log reports
`load: connecting via PostgresHook(pg_target)` then `load: 20 rows in example_load`.

## What can go wrong

- **`pg_target` not defined** → `PostgresHook` raises immediately. Create the
  Connection (step 2). This is by design — the hook has no fallback.
- **Provider not installed** → if you removed the `connectors: [postgres]` line,
  the task fails at runtime with `ModuleNotFoundError: airflow.providers.postgres`.
  Put the line back (that is exactly what the sugar installs).
- **Top-level import** → moving the `PostgresHook` import to the module top level
  fails the **compile** (not the run) with a message pointing you back here.

## Related

- `docs/connections/postgres.md` — the postgres cookbook entry.
- `docs/connections/index.md` — *Installing a connector's provider* (the sugar
  vs. escape-hatch, the connector → package table).
- ADR 0038 — connector dependency ergonomics (`connectors:` sugar).
- ADR 0019 / 0021 — secret encryption + agent delivery.
