"""postgres_load — compute rows and load them into an external Postgres.

The target DSN comes from a managed Dexaflow Connection injected as
AIRFLOW_CONN_PG_TARGET (create it in Admin → Connections); falls back to a local
DSN for a quick run.
"""
from __future__ import annotations

import os

from airflow.sdk import DAG, task


@task
def compute() -> list[tuple]:
    rows = [(f"cat_{i}", (i * 7) % 100) for i in range(20)]
    print(f"compute: {len(rows)} rows")
    return rows


@task
def load(rows: list[tuple]) -> None:
    import psycopg2

    dsn = os.environ.get("AIRFLOW_CONN_PG_TARGET") or os.environ.get(
        "PG_TARGET_DSN", "postgresql://postgres:etl@host.k3d.internal:55432/warehouse"
    )
    src = "managed Connection pg_target" if os.environ.get("AIRFLOW_CONN_PG_TARGET") else "fallback DSN"
    print(f"load: connecting via {src}")
    with psycopg2.connect(dsn) as conn:
        cur = conn.cursor()
        cur.execute("CREATE TABLE IF NOT EXISTS example_load (name text PRIMARY KEY, score int)")
        cur.execute("TRUNCATE example_load")
        cur.executemany("INSERT INTO example_load VALUES (%s, %s)", rows)
        conn.commit()
        cur.execute("SELECT COUNT(*) FROM example_load")
        print(f"load: {cur.fetchone()[0]} rows in example_load")


with DAG("postgres_load", schedule=None, catchup=False, tags=["example"]):
    load(compute())
