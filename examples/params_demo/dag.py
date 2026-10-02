"""params_demo — declares typed run params so the trigger dialog renders its
native form.

A DAG's ``params=`` are the run parameters an operator supplies at trigger time.
Dexaflow serves them to the embedded UI in Airflow's param-dict shape, so the
"Trigger Dag w/ config" dialog renders a generated form (with the raw JSON editor
still one toggle away) instead of an empty config box. This example declares one
of each shape the form handles:

- ``sample_size`` — a bare default (no schema): any value is accepted, the field
  is prefilled with the default.
- ``region`` — a typed enum with a default: renders as a select of its choices.
- ``run_label`` — a required param (no default): the form flags it until filled,
  and the backend rejects a trigger that omits it.

The single task echoes the three values via Jinja templating (``{{ params.x }}``),
the documented way to read a run's params in a task command.
"""
from __future__ import annotations

from airflow.providers.standard.operators.bash import BashOperator
from airflow.sdk import DAG, Param

with DAG(
    "params_demo",
    schedule=None,
    catchup=False,
    tags=["example"],
    params={
        "sample_size": 100,
        "region": Param("us", type="string", enum=["us", "eu", "apac"], title="Region"),
        "run_label": Param(type="string", title="Run label"),
    },
):
    BashOperator(
        task_id="report",
        bash_command=(
            "echo 'run_label={{ params.run_label }} "
            "region={{ params.region }} "
            "sample_size={{ params.sample_size }}'"
        ),
    )
