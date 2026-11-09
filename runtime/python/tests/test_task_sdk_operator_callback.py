"""A captured operator's on_failure_callback, run against the pinned Task SDK (#484).

run_operator builds the operator from its args, which carry no callable, so the
callback is read off the task in dag.py. These tests declare it the ways Airflow
authors do (a `with DAG():` bound to no variable, a called @dag, a bare callable
and the list Airflow normalises it to) and fail when the callback does not run.

They need the Task SDK, so they skip in the Airflow-free runtime job and run in the
``runtime-authoring-contract`` job.
"""

import importlib
import itertools
import sys

import pytest

pytest.importorskip("airflow.sdk")

from leoflow_runtime import runner  # noqa: E402

_counter = itertools.count()

_OPERATORS = '''
from airflow.sdk import BaseOperator


class Boom(BaseOperator):
    def __init__(self, sql=None, **kwargs):
        super().__init__(**kwargs)
        self.sql = sql

    def execute(self, context):
        raise RuntimeError("boom " + str(self.sql))
'''

_DAG = '''
from airflow.sdk import DAG, dag
from {ops} import Boom

SENT = []


def notify(context):
    SENT.append((context["ti"].task_id, repr(context.get("exception"))))


with DAG("ctx_dag", schedule=None):
    Boom(task_id="q", sql="ctx", on_failure_callback=[notify])


@dag(schedule=None)
def deco_dag():
    Boom(task_id="q", sql="deco", on_failure_callback=notify)


deco_dag()
'''


def _modules(tmp_path, monkeypatch) -> tuple[str, str]:
    n = next(_counter)
    ops, dag = f"sdk_ops_{n}", f"sdk_dag_{n}"
    (tmp_path / f"{ops}.py").write_text(_OPERATORS)
    (tmp_path / f"{dag}.py").write_text(_DAG.format(ops=ops))
    monkeypatch.syspath_prepend(str(tmp_path))
    return ops, dag


@pytest.mark.parametrize("dag_id", ["ctx_dag", "deco_dag"])
def test_operator_callback_runs_from_dag_py(tmp_path, monkeypatch, dag_id):
    ops, dag = _modules(tmp_path, monkeypatch)
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(tmp_path / "rv.json"))
    monkeypatch.setenv("LEOFLOW_TASK_ID", "q")
    monkeypatch.setenv("LEOFLOW_DAG_ID", dag_id)
    monkeypatch.setenv("LEOFLOW_ON_FAILURE_CALLBACK", "1")
    monkeypatch.setenv("LEOFLOW_CALLBACK_SOURCE", f"{dag}:q")

    with pytest.raises(RuntimeError, match="boom x"):
        runner.run_operator(f"{ops}.Boom", {"sql": "x"})

    assert importlib.import_module(dag).SENT == [("q", "RuntimeError('boom x')")]


def test_operator_callback_waits_for_the_last_attempt(tmp_path, monkeypatch):
    ops, dag = _modules(tmp_path, monkeypatch)
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(tmp_path / "rv.json"))
    monkeypatch.setenv("LEOFLOW_TASK_ID", "q")
    monkeypatch.setenv("LEOFLOW_DAG_ID", "ctx_dag")
    monkeypatch.setenv("LEOFLOW_ON_FAILURE_CALLBACK", "1")
    monkeypatch.setenv("LEOFLOW_CALLBACK_SOURCE", f"{dag}:q")
    monkeypatch.setenv("LEOFLOW_TRY_NUMBER", "1")
    monkeypatch.setenv("LEOFLOW_MAX_TRIES", "2")

    with pytest.raises(RuntimeError):
        runner.run_operator(f"{ops}.Boom", {"sql": "x"})

    # dag.py was not even imported on an attempt that will be retried.
    assert dag not in sys.modules


_TASK_DAG = '''
from airflow.sdk import DAG, dag, task

SENT = []


def notify(context):
    SENT.append(context["ti"].task_id)


@task
def load():
    raise RuntimeError("boom load")


@task(on_failure_callback=None)
def opted_out():
    raise RuntimeError("boom opted_out")


@dag(schedule=None, default_args={"on_failure_callback": notify, "retries": 1})
def inherit_dag():
    load()
    opted_out()


inherit_dag()
'''


def _task_dag(tmp_path, monkeypatch) -> str:
    name = f"sdk_task_dag_{next(_counter)}"
    (tmp_path / f"{name}.py").write_text(_TASK_DAG)
    monkeypatch.syspath_prepend(str(tmp_path))
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(tmp_path / "rv.json"))
    monkeypatch.setenv("LEOFLOW_DAG_ID", "inherit_dag")
    monkeypatch.setenv("LEOFLOW_ON_FAILURE_CALLBACK", "1")
    return name


def test_task_callback_from_default_args_runs(tmp_path, monkeypatch):
    """A @task's decorator does not carry a callback set in the DAG's default_args;
    Airflow applies it to the task in the DAG, and the runtime reads it there on the
    final attempt (#2441). Before, the compiler dropped it and nothing ran."""
    dag = _task_dag(tmp_path, monkeypatch)
    monkeypatch.setenv("LEOFLOW_TASK_ID", "load")

    with pytest.raises(RuntimeError, match="boom load"):
        runner.run(f"{dag}:load")

    assert importlib.import_module(dag).SENT == ["load"]


def test_task_callback_from_default_args_waits_for_the_last_attempt(tmp_path, monkeypatch):
    dag = _task_dag(tmp_path, monkeypatch)
    monkeypatch.setenv("LEOFLOW_TASK_ID", "load")
    monkeypatch.setenv("LEOFLOW_TRY_NUMBER", "1")
    monkeypatch.setenv("LEOFLOW_MAX_TRIES", "2")

    with pytest.raises(RuntimeError, match="boom load"):
        runner.run(f"{dag}:load")

    assert importlib.import_module(dag).SENT == []


def test_task_none_opts_out_of_default_args_callback(tmp_path, monkeypatch):
    """on_failure_callback=None on the task wins over default_args, as in Airflow,
    even if the marker were set."""
    dag = _task_dag(tmp_path, monkeypatch)
    monkeypatch.setenv("LEOFLOW_TASK_ID", "opted_out")

    with pytest.raises(RuntimeError, match="boom opted_out"):
        runner.run(f"{dag}:opted_out")

    assert importlib.import_module(dag).SENT == []


def test_operator_callback_from_default_args_runs(tmp_path, monkeypatch):
    """A captured operator reads an inherited callback off the task in dag.py the
    same way as its own (#2441): Airflow applied the DAG's default_args to it."""
    n = next(_counter)
    ops, dag = f"sdk_ops_da_{n}", f"sdk_dag_da_{n}"
    (tmp_path / f"{ops}.py").write_text(_OPERATORS)
    (tmp_path / f"{dag}.py").write_text(f'''
from airflow.sdk import DAG
from {ops} import Boom

SENT = []


def notify(context):
    SENT.append(context["ti"].task_id)


with DAG("da_dag", schedule=None, default_args={{"on_failure_callback": notify}}):
    Boom(task_id="q", sql="da")
''')
    monkeypatch.syspath_prepend(str(tmp_path))
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(tmp_path / "rv.json"))
    monkeypatch.setenv("LEOFLOW_TASK_ID", "q")
    monkeypatch.setenv("LEOFLOW_DAG_ID", "da_dag")
    monkeypatch.setenv("LEOFLOW_ON_FAILURE_CALLBACK", "1")
    monkeypatch.setenv("LEOFLOW_CALLBACK_SOURCE", f"{dag}:q")

    with pytest.raises(RuntimeError, match="boom x"):
        runner.run_operator(f"{ops}.Boom", {"sql": "x"})

    assert importlib.import_module(dag).SENT == ["q"]
