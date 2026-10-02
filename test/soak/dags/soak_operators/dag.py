"""soak_operators: the non-native execution path of the soak battery.

Everything here compiles to Dexaflow's `airflow_operator` task type, which is the
path with the most moving parts: the runtime has to resolve a class path, import
a provider package, construct the operator, hand it a Dexaflow-managed Connection
rendered as an AIRFLOW_CONN_* env var, and execute it. A native `python` task
touches none of that. Over a long run the question this DAG answers is whether
that path degrades differently from the native one: whether resolution cost
stays flat, and whether anything a provider import leaves behind accumulates.

Every endpoint is local by construction:

* `http_get` points at the soak fixture server on 127.0.0.1 (test/soak/fixture),
  through the managed Connection `soak_http`. No public API is contacted, on
  purpose: a weekend of requests against a stranger's service is not something
  we do, and an outage on their side would teach us nothing about our scheduler.
* `sql_upsert` / `sql_count` point at the soak Postgres container through the
  managed Connection `soak_pg`, into its own `soak_warehouse` database, so the
  workload never shares a table with Dexaflow's own metadata.
* `wait_a_moment` is a reschedule-mode HttpSensor pointed at the fixture's
  `/fixture/ready` endpoint, which answers 404 for the first three pokes of a
  given run and 200 after that. It is in the battery because `up_for_reschedule`
  is a scheduler state with its own re-dispatch path, and a DAG that never enters
  it would leave that path uncovered for the whole run. A DateTimeSensor would
  have been the obvious choice and does not work here: its `target_time` needs a
  future timestamp, and Dexaflow renders neither `{{ macros.* }}` nor anything
  else that could compute one, so the template reaches the provider verbatim and
  fails inside pendulum. The fixture-counter approach needs no clock and repeats
  identically on every run.

Task types exercised: `airflow_operator` (non-native) throughout, plus one
native `python` task to anchor the comparison inside the same run.
"""
from __future__ import annotations

import os

from airflow.sdk import DAG, task
from airflow.providers.http.operators.http import HttpOperator
from airflow.providers.common.sql.operators.sql import SQLExecuteQueryOperator
from airflow.providers.http.sensors.http import HttpSensor

SOAK_HOME = os.environ.get("SOAK_DATA_DIR") or os.path.expanduser("~/.leoflow/soak-data")


@task
def native_anchor() -> str:
    """A native python task in the same run as the operator tasks.

    Its purpose is comparison, not work: the harness reads the queued-to-running
    latency of this task and of the operator tasks from the same run, so the two
    paths are measured under identical scheduler conditions.
    """
    import time

    return f"anchor@{time.time():.3f}"


with DAG(
    "soak_operators",
    schedule="*/5 * * * *",
    catchup=False,
    max_active_runs=1,
    tags=["soak"],
):
    # The non-native HTTP path. `endpoint` is relative: the host comes from the
    # managed Connection, so no URL is baked into the compiled dag.json.
    http_get = HttpOperator(
        task_id="http_get",
        http_conn_id="soak_http",
        method="GET",
        endpoint="/fixture/events?n=50",
    )

    # The non-native SQL path, twice: a write and a read-back, so a silently
    # failing write cannot pass as a green run.
    sql_upsert = SQLExecuteQueryOperator(
        task_id="sql_upsert",
        conn_id="soak_pg",
        sql=(
            "CREATE TABLE IF NOT EXISTS soak_beats "
            "(run_id text PRIMARY KEY, seen_at timestamptz NOT NULL DEFAULT now()); "
            "INSERT INTO soak_beats (run_id) VALUES ('{{ run_id }}') "
            "ON CONFLICT (run_id) DO UPDATE SET seen_at = now();"
        ),
    )
    sql_count = SQLExecuteQueryOperator(
        task_id="sql_count",
        conn_id="soak_pg",
        sql="SELECT count(*) FROM soak_beats;",
    )

    # Reschedule mode: the sensor releases its slot on every not-ready poke and
    # is re-dispatched, so one run walks the up_for_reschedule path three times
    # before succeeding. `{{ run_id }}` is rendered by Dexaflow, which gives each
    # run its own poke counter on the fixture.
    wait_a_moment = HttpSensor(
        task_id="wait_a_moment",
        http_conn_id="soak_http",
        endpoint="/fixture/ready?key={{ run_id }}&after=3",
        mode="reschedule",
        poke_interval=15,
        timeout=180,
    )

    anchor = native_anchor()

    http_get >> sql_upsert >> sql_count
    anchor >> wait_a_moment
