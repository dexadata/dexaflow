"""sqlite_load — compute rows and load them into a sqlite file.

The target DB path comes from a managed Dexaflow Connection injected as
AIRFLOW_CONN_SQLITE_TARGET (create it in Admin → Connections). The Schema
field carries the absolute file path; the URI looks like
`sqlite:///var/lib/leoflow/warehouse.db`.

No third-party driver needed — `sqlite3` is in the Python standard library.
Falls back to a local DB file under /tmp for a quick demo.
"""
from __future__ import annotations

import os
import sqlite3
from urllib.parse import urlparse

from airflow.sdk import DAG, task


@task
def compute() -> list[tuple]:
    rows = [(f"cat_{i}", (i * 7) % 100) for i in range(20)]
    print(f"compute: {len(rows)} rows")
    return rows


@task
def load(rows: list[tuple]) -> None:
    raw = os.environ.get("AIRFLOW_CONN_SQLITE_TARGET") or os.environ.get(
        "SQLITE_TARGET_PATH", "sqlite:///tmp/leoflow_warehouse.db"
    )
    src = "managed Connection sqlite_target" if os.environ.get("AIRFLOW_CONN_SQLITE_TARGET") else "fallback DSN"
    print(f"load: connecting via {src}")

    # sqlite3.connect takes a file path, not a URI. Parse the URI and use
    # parsed.path verbatim. Unlike the SQL family there is no password,
    # host, or port — just the path.
    path = urlparse(raw).path
    if not path:
        raise ValueError(f"sqlite URI has no path: {raw!r}")

    conn = sqlite3.connect(path)
    try:
        cur = conn.cursor()
        cur.execute(
            "CREATE TABLE IF NOT EXISTS example_load (name TEXT PRIMARY KEY, score INTEGER)"
        )
        cur.execute("DELETE FROM example_load")
        cur.executemany("INSERT INTO example_load VALUES (?, ?)", rows)
        conn.commit()
        (count,) = cur.execute("SELECT COUNT(*) FROM example_load").fetchone()
        print(f"load: {count} rows in example_load at {path}")
    finally:
        conn.close()


with DAG("sqlite_load", schedule=None, catchup=False, tags=["example"]):
    load(compute())
