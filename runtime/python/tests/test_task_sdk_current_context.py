"""get_current_context() inside a task body, against the pinned Task SDK (#2480).

Airflow's task runner sets the current context around the user's code, so a
@task body or an operator's execute() can call get_current_context() instead of
taking **context. These tests need the Task SDK, so they skip in the
Airflow-free runtime job and run in the ``runtime-authoring-contract`` job.
"""

import itertools
import json
from pathlib import Path

import pytest

pytest.importorskip("airflow.sdk")

from airflow.sdk import get_current_context  # noqa: E402

from leoflow_runtime import runner  # noqa: E402

_counter = itertools.count()


def _write_module(tmp_path, monkeypatch, body: str) -> str:
    name = f"current_ctx_{next(_counter)}"
    (tmp_path / f"{name}.py").write_text(body)
    monkeypatch.syspath_prepend(str(tmp_path))
    return name


def _env(tmp_path, monkeypatch) -> str:
    rv = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(rv))
    monkeypatch.setenv("LEOFLOW_DAG_ID", "ctx_dag")
    monkeypatch.setenv("LEOFLOW_RUN_ID", "manual__1")
    monkeypatch.setenv("LEOFLOW_DS", "2026-06-14")
    return str(rv)


def test_task_body_reads_get_current_context(tmp_path, monkeypatch):
    rv = _env(tmp_path, monkeypatch)
    monkeypatch.setenv("LEOFLOW_TASK_ID", "probe")
    mod = _write_module(
        tmp_path,
        monkeypatch,
        "from airflow.sdk import get_current_context, task\n"
        "@task\n"
        "def probe():\n"
        "    ctx = get_current_context()\n"
        "    return [ctx['ds'], ctx['ti'].task_id, ctx['dag_run'].run_id]\n",
    )
    runner.run(f"{mod}:probe")
    got = json.loads(Path(rv).read_text())
    assert got == ["2026-06-14", "probe", "manual__1"]


def test_operator_execute_reads_get_current_context(tmp_path, monkeypatch):
    rv = _env(tmp_path, monkeypatch)
    monkeypatch.setenv("LEOFLOW_TASK_ID", "op")
    mod = _write_module(
        tmp_path,
        monkeypatch,
        "from airflow.sdk import BaseOperator, get_current_context\n"
        "class Probe(BaseOperator):\n"
        "    def execute(self, context):\n"
        "        return get_current_context()['ds']\n",
    )
    runner.run_operator(f"{mod}.Probe", {})
    got = json.loads(Path(rv).read_text())
    assert got == "2026-06-14"


def test_context_is_unset_after_the_task(tmp_path, monkeypatch):
    _env(tmp_path, monkeypatch)
    monkeypatch.setenv("LEOFLOW_TASK_ID", "probe")
    mod = _write_module(tmp_path, monkeypatch, "def probe():\n    return 1\n")
    runner.run(f"{mod}:probe")
    with pytest.raises(RuntimeError, match="no context was found"):
        get_current_context()
