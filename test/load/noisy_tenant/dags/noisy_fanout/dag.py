"""noisy_fanout: the noisy tenant's wide fan-out of Load Experiment 10.

One root, FANOUT independent sleepers (5,000 by default) and one join. All the
sleepers are ready in the same tick, so the tenant sits at its pool slots and
the dispatcher's per-tenant cap for the whole run. Dynamic task mapping is not
supported, so the sleepers are declared in a loop.
"""
from __future__ import annotations

import os
import time

from airflow.sdk import DAG, task

FANOUT = int(os.environ.get("NOISY_FANOUT", "") or 5000)
SLEEP_SECONDS = int(os.environ.get("NOISY_SLEEP_SECONDS", "") or 20)


@task
def root() -> None:
    return None


@task
def sleeper() -> None:
    time.sleep(SLEEP_SECONDS)


@task
def join() -> None:
    return None


with DAG("noisy_fanout", schedule=None, catchup=False, tags=["load"]):
    start, end = root(), join()
    for i in range(FANOUT):
        start >> sleeper.override(task_id=f"sleep_{i:04d}")() >> end
