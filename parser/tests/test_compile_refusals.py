"""Constructs the compiler refuses instead of compiling into a run-time failure
or a silent change of semantics (#2336, #225).

Each refusal names what the construct needs, what to do instead, and the issue
that tracks real support, so the author can act on the message alone.
"""
from __future__ import annotations

import json
import textwrap
from pathlib import Path

import pytest

from leoflow_parser.compiler import compile_dag


def _compile(monkeypatch, tmp_path: Path, body: str) -> dict:
    monkeypatch.setenv("LEOFLOW_PROJECT_CONFIG_JSON", json.dumps({"schema_version": "1.0"}))
    src = tmp_path / "dag.py"
    src.write_text(textwrap.dedent(body))
    return compile_dag(str(src), str(tmp_path / "ignored.json"), "img:v1", dag_version="v1")


def _refusal(monkeypatch, tmp_path: Path, body: str) -> str:
    with pytest.raises(ValueError) as excinfo:
        _compile(monkeypatch, tmp_path, body)
    return str(excinfo.value)


# ───────────── operators that need the Airflow metadata database ─────────────

_METADATA_DB_OPERATORS = [
    ("airflow.providers.standard.sensors.external_task", "ExternalTaskSensor",
     "external_dag_id='up'", "#2340"),
    ("airflow.providers.standard.sensors.external_task", "ExternalTaskMarker",
     "external_dag_id='down', external_task_id='t'", "#2340"),
    ("airflow.providers.standard.operators.trigger_dagrun", "TriggerDagRunOperator",
     "trigger_dag_id='down'", "#2340"),
    ("airflow.providers.standard.operators.latest_only", "LatestOnlyOperator",
     "", "#2348"),
]


@pytest.mark.parametrize("module,cls,args,issue", _METADATA_DB_OPERATORS)
def test_metadata_database_operator_is_refused_at_compile(monkeypatch, tmp_path,
                                                          module, cls, args, issue):
    sep = ", " if args else ""
    message = _refusal(monkeypatch, tmp_path, f"""
        from airflow.sdk import DAG
        from {module} import {cls}
        with DAG("g"):
            {cls}(task_id="t"{sep}{args})
    """)
    assert cls in message
    assert "'t'" in message
    assert "metadata database" in message
    assert issue in message
    assert "#operators-that-need-the-airflow-metadata-database" in message


def test_subclass_of_a_metadata_database_operator_is_refused(monkeypatch, tmp_path):
    message = _refusal(monkeypatch, tmp_path, """
        from airflow.sdk import DAG
        from airflow.providers.standard.sensors.external_task import ExternalTaskSensor

        class WaitForWarehouse(ExternalTaskSensor):
            pass

        with DAG("g"):
            WaitForWarehouse(task_id="wait", external_dag_id="warehouse")
    """)
    assert "WaitForWarehouse" in message
    assert "ExternalTaskSensor" in message
    assert "metadata database" in message


def test_branch_operator_subclass_without_branch_in_its_name_is_refused(monkeypatch, tmp_path):
    # LatestOnlyOperator escaped the name check because it is a branch operator
    # whose name does not say so; any BaseBranchOperator subclass can.
    message = _refusal(monkeypatch, tmp_path, """
        from airflow.sdk import DAG
        from airflow.providers.standard.operators.branch import BaseBranchOperator

        class Chooser(BaseBranchOperator):
            pass

        with DAG("g"):
            Chooser(task_id="choose")
    """)
    assert "Chooser" in message
    assert "branching" in message


def test_other_standard_sensors_still_compile(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG
        from airflow.providers.standard.sensors.filesystem import FileSensor
        with DAG("g"):
            FileSensor(task_id="f", filepath="/tmp/x", mode="reschedule")
    """)
    assert spec["tasks"][0]["type"] == "airflow_operator"


# ───────────── task arguments the scheduler does not honor ─────────────

@pytest.mark.parametrize("kwarg,needle", [
    ("depends_on_past=True", "depends_on_past"),
    ("wait_for_downstream=True", "wait_for_downstream"),
    ("priority_weight=10", "priority_weight"),
])
def test_unhonored_task_argument_is_refused_on_a_task(monkeypatch, tmp_path, kwarg, needle):
    message = _refusal(monkeypatch, tmp_path, f"""
        from airflow.sdk import DAG, task
        @task({kwarg})
        def a(): ...
        with DAG("g"):
            a()
    """)
    assert needle in message
    assert "'a'" in message
    assert "ignore" in message


@pytest.mark.parametrize("kwarg,needle", [
    ("depends_on_past=True", "depends_on_past"),
    ("priority_weight=5", "priority_weight"),
])
def test_unhonored_task_argument_is_refused_on_a_provider_operator(monkeypatch, tmp_path,
                                                                   kwarg, needle):
    message = _refusal(monkeypatch, tmp_path, f"""
        from airflow.sdk import DAG
        from airflow.providers.postgres.operators.postgres import PostgresOperator
        with DAG("g"):
            PostgresOperator(task_id="q", sql="select 1", {kwarg})
    """)
    assert needle in message
    assert "'q'" in message


def test_unhonored_task_argument_is_refused_from_default_args(monkeypatch, tmp_path):
    message = _refusal(monkeypatch, tmp_path, """
        from airflow.sdk import DAG
        from airflow.providers.standard.operators.bash import BashOperator
        with DAG("g", default_args={"depends_on_past": True}):
            BashOperator(task_id="b", bash_command="true")
    """)
    assert "depends_on_past" in message
    assert "default_args" in message
    assert "#2350" in message


@pytest.mark.parametrize("kwargs", [
    "depends_on_past=False",
    "wait_for_downstream=False",
    "priority_weight=1",
])
def test_default_values_of_unhonored_arguments_still_compile(monkeypatch, tmp_path, kwargs):
    spec = _compile(monkeypatch, tmp_path, f"""
        from airflow.sdk import DAG
        from airflow.providers.standard.operators.bash import BashOperator
        with DAG("g", default_args={{"depends_on_past": False}}):
            BashOperator(task_id="b", bash_command="true", {kwargs})
    """)
    assert spec["tasks"][0]["task_id"] == "b"


# ───────────── setup and teardown, task groups ─────────────

def test_as_setup_is_refused_with_a_message(monkeypatch, tmp_path):
    message = _refusal(monkeypatch, tmp_path, """
        from airflow.sdk import DAG
        from airflow.providers.standard.operators.bash import BashOperator
        with DAG("g"):
            BashOperator(task_id="create", bash_command="true").as_setup()
    """)
    assert "setup and teardown" in message
    assert "all_done" in message


def test_as_teardown_is_refused_with_a_message(monkeypatch, tmp_path):
    message = _refusal(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, task
        @task
        def drop(): ...
        with DAG("g"):
            drop().as_teardown()
    """)
    assert "setup and teardown" in message


@pytest.mark.parametrize("decorator", ["setup", "teardown"])
def test_setup_and_teardown_decorators_are_refused(monkeypatch, tmp_path, decorator):
    message = _refusal(monkeypatch, tmp_path, f"""
        from airflow.sdk import DAG, {decorator}
        @{decorator}
        def step(): ...
        with DAG("g"):
            step()
    """)
    assert "setup and teardown" in message


@pytest.mark.parametrize("import_line", [
    "from airflow.sdk import TaskGroup",
    "from airflow.utils.task_group import TaskGroup",
    "from airflow.sdk import task_group",
    "from airflow.decorators import task_group",
])
def test_task_group_is_refused_with_flatten_advice(monkeypatch, tmp_path, import_line):
    message = _refusal(monkeypatch, tmp_path, f"""
        {import_line}
        from airflow.sdk import DAG
        with DAG("g"):
            pass
    """)
    assert "flatten" in message
    assert "#2338" in message
    assert "cannot import name" not in message
