"""soak_fanout: the parallelism shape of the soak battery.

Shape: 1 generator, 8 independent partition tasks that can all be dispatched in
the same tick, 1 reducer that waits for all 8. This is the DAG that tells us
whether parallelism behaves: the eight siblings should all leave `scheduled` in
the same tick and the reducer must not start until the last one is terminal.

`max_active_tasks` is set to 4 on purpose, so the DAG also exercises the
per-DAG concurrency cap: with 8 ready siblings and a cap of 4, the scheduler
must dispatch exactly 4 and hold the rest. A soak that never pressures the cap
would never notice the cap regressing.

Task types exercised: `python` (native).
"""
from __future__ import annotations

import os
import time

from airflow.sdk import DAG, task

SOAK_HOME = os.environ.get("SOAK_DATA_DIR") or os.path.expanduser("~/.leoflow/soak-data")
PARTITIONS = 8


def soak_int(name: str, default: int) -> int:
    try:
        return int(os.environ.get(name, "") or default)
    except ValueError:
        return default


def run_dir(dag_id: str) -> str:
    d = os.path.join(SOAK_HOME, "scratch", dag_id)
    os.makedirs(d, exist_ok=True)
    return d


@task
def generate() -> dict:
    import duckdb

    rows = soak_int("SOAK_FANOUT_ROWS", 200_000)
    out = run_dir("soak_fanout")
    src = os.path.join(out, "source.parquet")
    con = duckdb.connect()
    con.execute("PRAGMA enable_progress_bar=false")
    con.execute(
        f"""
        COPY (
          SELECT i AS id,
                 i % {PARTITIONS} AS part,
                 (random() * 500)::DECIMAL(10, 2) AS value
          FROM range({rows}) t(i)
        ) TO '{src}' (FORMAT parquet)
        """
    )
    print(f"generate: {rows} rows to {src} ({os.path.getsize(src) / 1e6:.1f} MB)")
    return {"src": src, "rows": rows}


def _partition_body(meta: dict, part: int) -> dict:
    import duckdb

    t0 = time.time()
    out = run_dir("soak_fanout")
    dst = os.path.join(out, f"part_{part}.parquet")
    con = duckdb.connect()
    con.execute(
        f"""
        COPY (
          SELECT part, COUNT(*) AS n, SUM(value) AS total
          FROM read_parquet('{meta["src"]}')
          WHERE part = {part}
          GROUP BY part
        ) TO '{dst}' (FORMAT parquet)
        """
    )
    print(f"partition {part}: wrote {dst} in {time.time() - t0:.2f}s")
    return {"part": part, "path": dst}


# Eight explicitly declared siblings rather than a loop-built list: Dexaflow
# compiles the DAG by parsing it, and eight named task_ids keep the assertion in
# the harness ("all eight left `scheduled` within one tick") readable against a
# fixed set of ids instead of a generated one.
@task
def part_0(meta: dict) -> dict:
    return _partition_body(meta, 0)


@task
def part_1(meta: dict) -> dict:
    return _partition_body(meta, 1)


@task
def part_2(meta: dict) -> dict:
    return _partition_body(meta, 2)


@task
def part_3(meta: dict) -> dict:
    return _partition_body(meta, 3)


@task
def part_4(meta: dict) -> dict:
    return _partition_body(meta, 4)


@task
def part_5(meta: dict) -> dict:
    return _partition_body(meta, 5)


@task
def part_6(meta: dict) -> dict:
    return _partition_body(meta, 6)


@task
def part_7(meta: dict) -> dict:
    return _partition_body(meta, 7)


@task
def reduce_all(parts: list) -> dict:
    import duckdb

    paths = [p["path"] for p in parts]
    missing = [p for p in paths if not os.path.exists(p)]
    if missing:
        raise RuntimeError(f"reducer ran before its upstreams produced: {missing}")
    con = duckdb.connect()
    rows = con.execute(
        "SELECT SUM(n), SUM(total) FROM read_parquet(" + repr(paths) + ")"
    ).fetchone()
    print(f"reduce: {len(paths)} partitions, {rows[0]} rows, total {rows[1]}")
    return {"partitions": len(paths), "rows": int(rows[0])}


with DAG(
    "soak_fanout",
    schedule="*/5 * * * *",
    catchup=False,
    max_active_runs=1,
    max_active_tasks=4,
    tags=["soak"],
):
    src = generate()
    reduce_all(
        [
            part_0(src),
            part_1(src),
            part_2(src),
            part_3(src),
            part_4(src),
            part_5(src),
            part_6(src),
            part_7(src),
        ]
    )
