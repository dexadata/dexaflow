"""mssql_load — compute rows and load them into an external SQL Server.

The target DSN comes from a managed Dexaflow Connection injected as
AIRFLOW_CONN_MSSQL_TARGET (create it in Admin → Connections); falls back to
a local DSN for a quick demo.

Note: pymssql does not accept the Airflow URI string directly — we parse it
with urllib.parse and pass the fields as kwargs. The cookbook page at
docs/connections/mssql.md documents this gotcha (and the alternative
pyodbc/SQLAlchemy paths).
"""
from __future__ import annotations

import os
from urllib.parse import unquote, urlparse

from airflow.sdk import DAG, task


@task
def compute() -> list[tuple]:
    rows = [(f"cat_{i}", (i * 7) % 100) for i in range(20)]
    print(f"compute: {len(rows)} rows")
    return rows


@task
def load(rows: list[tuple]) -> None:
    import pymssql

    raw = os.environ.get("AIRFLOW_CONN_MSSQL_TARGET") or os.environ.get(
        "MSSQL_TARGET_DSN", "mssql://sa:Etl%401234@127.0.0.1:51433/warehouse"
    )
    src = (
        "managed Connection mssql_target"
        if os.environ.get("AIRFLOW_CONN_MSSQL_TARGET")
        else "fallback DSN"
    )
    print(f"load: connecting via {src}")

    # pymssql takes kwargs, not a URI. Parse and un-quote the password so
    # percent-escaped reserved characters (@, :, /, ...) come through verbatim.
    url = urlparse(raw)
    conn = pymssql.connect(
        server=url.hostname or "127.0.0.1",
        port=url.port or 1433,
        user=url.username or "",
        password=unquote(url.password or ""),
        database=(url.path or "/warehouse").lstrip("/"),
    )
    try:
        cur = conn.cursor()
        cur.execute(
            "IF OBJECT_ID('example_load', 'U') IS NULL "
            "CREATE TABLE example_load (name NVARCHAR(64) PRIMARY KEY, score INT)"
        )
        cur.execute("TRUNCATE TABLE example_load")
        cur.executemany("INSERT INTO example_load VALUES (%s, %s)", rows)
        conn.commit()
        cur.execute("SELECT COUNT(*) FROM example_load")
        (count,) = cur.fetchone()
        print(f"load: {count} rows in example_load")
    finally:
        conn.close()


with DAG("mssql_load", schedule=None, catchup=False, tags=["example"]):
    load(compute())
