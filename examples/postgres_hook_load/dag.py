"""postgres_hook_load — load rows into an external Postgres via PostgresHook.

The sibling of examples/postgres_load: same job, but it uses Airflow's
PostgresHook instead of raw psycopg2. The provider is declared with one line of
`connectors:` sugar in dexaflow.yaml (no driver to remember), and the hook reads
the managed Connection `pg_target` (injected as AIRFLOW_CONN_PG_TARGET).

Note the hook is imported INSIDE the task body, not at module top level: Dexaflow
parses the DAG without providers installed, so a top-level provider import fails
the compile. Inside the task it is fine — the parser never executes task bodies,
and at runtime the provider is installed.
"""
from __future__ import annotations

from airflow.sdk import DAG, task


@task
def compute() -> list[tuple]:
    rows = [(f"cat_{i}", (i * 7) % 100) for i in range(20)]
    print(f"compute: {len(rows)} rows")
    return rows


@task
def load(rows: list[tuple]) -> None:
    # Imported here, not at module top level — see the module docstring.
    from airflow.providers.postgres.hooks.postgres import PostgresHook

    hook = PostgresHook(postgres_conn_id="pg_target")
    print("load: connecting via PostgresHook(pg_target)")
    hook.run(
        [
            "CREATE TABLE IF NOT EXISTS example_load (name text PRIMARY KEY, score int)",
            "TRUNCATE example_load",
        ]
    )
    hook.insert_rows(table="example_load", rows=rows, target_fields=["name", "score"])
    count = hook.get_first("SELECT COUNT(*) FROM example_load")[0]
    print(f"load: {count} rows in example_load")


with DAG("postgres_hook_load", schedule=None, catchup=False, tags=["example"]):
    load(compute())
