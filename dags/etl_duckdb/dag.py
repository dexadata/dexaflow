"""etl_duckdb — a real ETL that stages ~1GB on the per-run /staging volume.

extract    generates ~1GB of synthetic sales data with DuckDB → /staging/raw.parquet
transform  aggregates it (DuckDB, out-of-core) → /staging/agg.parquet
load       reads the aggregate and writes the final → /staging/output.csv

XCom carries only the small paths + metrics; the GBs live on /staging, shared
across the run's pods. DuckDB streams, so 1GB processes within a modest pod.
"""
from __future__ import annotations

import os
import time

from airflow.sdk import DAG, task

# NOTE: heavy/task-only deps (duckdb) are imported INSIDE the tasks, not at module
# top level — the DAG parser imports this file to extract structure and does not
# have the task image's dependencies. Top-level `import duckdb` breaks parsing.

STAGING = os.environ.get("LEOFLOW_STAGING_DIR", "/staging")
ROWS = 70_000_000  # ~1.1GB as Parquet of (id, ts, category, amount, qty)


@task
def extract() -> dict:
    import duckdb

    t0 = time.time()
    raw = f"{STAGING}/raw.parquet"
    con = duckdb.connect()
    con.execute("PRAGMA enable_progress_bar=false")
    con.execute(
        f"""
        COPY (
          SELECT i AS id,
                 TIMESTAMP '2026-01-01' + (i % 8640000) * INTERVAL 1 SECOND AS ts,
                 'cat_' || (i % 50) AS category,
                 (random() * 1000)::DECIMAL(10, 2) AS amount,
                 (random() * 10)::INT AS qty
          FROM range({ROWS}) t(i)
        ) TO '{raw}' (FORMAT parquet)
        """
    )
    size_mb = round(os.path.getsize(raw) / 1e6, 1)
    print(f"extract: wrote {ROWS:,} rows -> {raw} ({size_mb} MB) in {time.time() - t0:.1f}s")
    return {"raw": raw, "rows": ROWS, "size_mb": size_mb}


@task
def transform(meta: dict) -> dict:
    import duckdb

    t0 = time.time()
    raw, agg = meta["raw"], f"{STAGING}/agg.parquet"
    con = duckdb.connect()
    con.execute(
        f"""
        COPY (
          SELECT category,
                 COUNT(*)     AS n,
                 SUM(amount)  AS revenue,
                 AVG(amount)  AS avg_amount,
                 SUM(qty)     AS total_qty
          FROM read_parquet('{raw}')
          GROUP BY category
          ORDER BY revenue DESC
        ) TO '{agg}' (FORMAT parquet)
        """
    )
    groups = con.execute(f"SELECT COUNT(*) FROM read_parquet('{agg}')").fetchone()[0]
    print(f"transform: {meta['rows']:,} rows -> {groups} groups -> {agg} in {time.time() - t0:.1f}s")
    return {"agg": agg, "groups": groups}


@task
def load(meta: dict) -> None:
    import duckdb
    import psycopg2

    t0 = time.time()
    agg, out = meta["agg"], f"{STAGING}/output.csv"
    con = duckdb.connect()
    con.execute(f"COPY (SELECT * FROM read_parquet('{agg}')) TO '{out}' (HEADER, DELIMITER ',')")
    rows = con.execute(
        f"SELECT category, n, revenue, avg_amount, total_qty FROM read_parquet('{agg}') ORDER BY revenue DESC"
    ).fetchall()

    # Real "L": load the aggregate into an EXTERNAL postgres. Prefer the managed
    # Dexaflow Connection — the agent injects it as AIRFLOW_CONN_ETL_TARGET over a
    # secure gRPC pull (never in the pod spec; visible in Admin → Connections,
    # encrypted at rest). Fall back to a direct DSN for local runs.
    managed = os.environ.get("AIRFLOW_CONN_ETL_TARGET")
    dsn = managed or os.environ.get("ETL_TARGET_DSN", "postgresql://postgres:etl@host.k3d.internal:55432/warehouse")
    print(f"load: connecting via {'managed Connection etl_target' if managed else 'fallback DSN'}")
    conn = psycopg2.connect(dsn)
    try:
        cur = conn.cursor()
        cur.execute(
            "CREATE TABLE IF NOT EXISTS category_revenue ("
            "category text PRIMARY KEY, n bigint, revenue numeric, avg_amount numeric, total_qty bigint)"
        )
        cur.execute("TRUNCATE category_revenue")
        cur.executemany("INSERT INTO category_revenue VALUES (%s, %s, %s, %s, %s)", rows)
        conn.commit()
        cur.execute("SELECT COUNT(*) FROM category_revenue")
        loaded = cur.fetchone()[0]
    finally:
        conn.close()

    print(f"load: wrote {out} ({round(os.path.getsize(out) / 1e3, 1)} KB) in {time.time() - t0:.1f}s")
    print(f"load: loaded {loaded} rows into EXTERNAL postgres warehouse.category_revenue via {dsn.split('@')[-1]}")
    print(f"load: top categories by revenue: {rows[:3]}")
    # Physical proof: every staged file is on the shared /staging volume (raw written
    # by extract, agg by transform, output by load — three different pods, one disk).
    print("load: /staging contents (physical proof before GC reclaims the volume):")
    for name in sorted(os.listdir(STAGING)):
        path = f"{STAGING}/{name}"
        print(f"  - {name}: {os.path.getsize(path) / 1e6:.1f} MB")


with DAG("etl_duckdb", schedule=None, catchup=False, tags=["etl"]):
    load(transform(extract()))
