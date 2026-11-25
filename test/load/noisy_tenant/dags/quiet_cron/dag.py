"""quiet_cron: the quiet tenant's every-minute DAG of Load Experiment 10.

Two short tasks, so a run starts, dispatches and ends well inside its minute.
Its runs' delays are what the experiment reads; deploy it more than once under
other dag ids (quiet_cron_1, quiet_cron_2, ...) for more samples per window.
"""
from __future__ import annotations

from airflow.sdk import DAG, task


@task
def tick() -> int:
    return 1


@task
def tock(x: int) -> int:
    return x + 1


with DAG("quiet_cron", schedule="* * * * *", catchup=False, max_active_runs=1, tags=["load"]):
    tock(tick())
