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
