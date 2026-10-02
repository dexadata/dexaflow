"""soak_chain: the sequential-dependency shape of the soak battery.

Shape: a 10-deep chain, alternating BashOperator (which Dexaflow compiles to its
native `bash` task type) and a TaskFlow @task (the native `python` type). Every
hop is one scheduler decision that can only be taken after the previous task went
terminal, so a run's wall-clock is dominated by 10 x (dispatch latency + tick
interval), not by the work. That makes this the most sensitive DAG in the battery
to a scheduler whose tick is slowing down: the ingest and fan-out DAGs hide a
slow tick behind their own runtime, this one does not.

The bodies are deliberately trivial (an echo, a print). Making them heavy would
only add noise to the measurement this DAG exists for.

The five python hops are five separate functions rather than one decorated
function reused with `.override(task_id=...)`: Dexaflow's parser shim (ADR 0024)
implements the `@task` decorator itself and its returned object has no
`.override`, so a DAG written the Airflow way fails to compile with
`AttributeError: 'function' object has no attribute 'override'`.

Task types exercised: `bash` (native, from BashOperator), `python` (native).
"""
from __future__ import annotations

from airflow.sdk import DAG, task
from airflow.providers.standard.operators.bash import BashOperator


@task
def hop_py_0() -> None:
    print("python hop 0")


@task
def hop_py_1() -> None:
    print("python hop 1")


@task
def hop_py_2() -> None:
    print("python hop 2")


@task
def hop_py_3() -> None:
    print("python hop 3")


@task
def hop_py_4() -> None:
    print("python hop 4")


with DAG("soak_chain", schedule="*/3 * * * *", catchup=False, max_active_runs=1, tags=["soak"]):
    py_hops = [hop_py_0(), hop_py_1(), hop_py_2(), hop_py_3(), hop_py_4()]
    prev = None
    for i, py in enumerate(py_hops):
        # The native bash path, including Jinja templating of the run context:
        # `{{ ds }}` is rendered by Dexaflow's own templater, not by Airflow.
        b = BashOperator(
            task_id=f"hop_bash_{i}",
            bash_command=f"echo 'bash hop {i} ds={{{{ ds }}}}' && date +%s",
        )
        if prev is not None:
            prev >> b
        b >> py
        prev = py
