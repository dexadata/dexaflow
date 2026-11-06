"""@task.bash against the pinned Task SDK (#2337).

The parser compiles a @task.bash task to a python task. In the pod the runtime
calls the decorated function and runs the command it returned with bash, as
Airflow's _BashDecoratedOperator does. These tests need the Task SDK, so they
skip in the Airflow-free runtime job and run in the ``runtime-authoring-contract``
job.
"""

import itertools

import pytest

pytest.importorskip("airflow.sdk")

from leoflow_runtime import runner  # noqa: E402

_counter = itertools.count()


def _write_module(tmp_path, monkeypatch, body: str) -> str:
    name = f"taskbash_{next(_counter)}"
    (tmp_path / f"{name}.py").write_text(body)
    monkeypatch.syspath_prepend(str(tmp_path))
    return name


def test_task_bash_runs_the_returned_command(tmp_path, monkeypatch):
    execs = []
    monkeypatch.setattr(runner.os, "execvp", lambda *a: execs.append(a))
    monkeypatch.setenv("LEOFLOW_DS", "2026-06-14")
    mod = _write_module(tmp_path, monkeypatch, (
        "from airflow.sdk import task\n"
        "@task.bash\n"
        "def greet(name='x'):\n"
        "    return 'echo hello ' + name + ' {{ ds }}'\n"
    ))

    runner.run(f"{mod}:greet")

    assert execs == [("bash", ["bash", "-c", "echo hello x 2026-06-14"])]


def test_plain_task_is_not_taken_for_task_bash(tmp_path, monkeypatch):
    execs = []
    monkeypatch.setattr(runner.os, "execvp", lambda *a: execs.append(a))
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(tmp_path / "rv.json"))
    mod = _write_module(tmp_path, monkeypatch, (
        "from airflow.sdk import task\n"
        "@task\n"
        "def cmd():\n"
        "    return 'echo not run'\n"
    ))

    runner.run(f"{mod}:cmd")

    assert execs == []
    assert (tmp_path / "rv.json").read_text() == '"echo not run"'
