"""bash_pipeline — BashOperator tasks (Dexaflow's 'bash' task type).

Shows the classic operator path: each task is a BashOperator, which Dexaflow
compiles to a 'bash' task and the agent runs as a shell command in the pod.
"""
from __future__ import annotations

from airflow.providers.standard.operators.bash import BashOperator
from airflow.sdk import DAG

with DAG("bash_pipeline", schedule=None, catchup=False, tags=["example"]):
    prepare = BashOperator(
        task_id="prepare",
        bash_command="echo 'preparing workspace' && mkdir -p /tmp/bashpipe && echo ok",
    )
    crunch = BashOperator(
        task_id="crunch",
        bash_command="seq 1 1000 | awk '{s+=$1} END {print \"sum 1..1000 =\", s}'",
    )
    prepare >> crunch
