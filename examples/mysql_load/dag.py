"""mysql_load — compute rows and load them into an external MySQL/MariaDB.

The target DSN comes from a managed Dexaflow Connection injected as
AIRFLOW_CONN_MY_DB (create it in Admin → Connections); falls back to a local
DSN for a quick demo.

Note: PyMySQL does not accept the Airflow URI string directly — we parse it
with urllib.parse and pass the fields as kwargs. The cookbook page at
docs/connections/mysql.md documents this gotcha.
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
    import pymysql

    raw = os.environ.get("AIRFLOW_CONN_MY_DB") or os.environ.get(
        "MY_DB_URL", "mysql://root:etl@127.0.0.1:53306/warehouse"
    )
    src = "managed Connection my_db" if os.environ.get("AIRFLOW_CONN_MY_DB") else "fallback DSN"
    print(f"load: connecting via {src}")

    # PyMySQL takes kwargs, not a URI. Parse the AIRFLOW_CONN URI into the
    # fields it needs; un-quote the password so percent-escaped reserved
    # characters (@, :, /, ...) come through verbatim.
    url = urlparse(raw)
    conn = pymysql.connect(
        host=url.hostname or "127.0.0.1",
        port=url.port or 3306,
        user=url.username or "",
        password=unquote(url.password or ""),
        database=(url.path or "/warehouse").lstrip("/"),
    )
    try:
        with conn.cursor() as cur:
            cur.execute(
                "CREATE TABLE IF NOT EXISTS example_load (name VARCHAR(64) PRIMARY KEY, score INT)"
            )
            cur.execute("TRUNCATE example_load")
            cur.executemany("INSERT INTO example_load VALUES (%s, %s)", rows)
            conn.commit()
            cur.execute("SELECT COUNT(*) FROM example_load")
            (count,) = cur.fetchone()
            print(f"load: {count} rows in example_load")
    finally:
        conn.close()


with DAG("mysql_load", schedule=None, catchup=False, tags=["example"]):
    load(compute())
