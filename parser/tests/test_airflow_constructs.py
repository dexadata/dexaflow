"""Airflow constructs most DAGs use that used to fail ``dexaflow compile``
with an opaque error (#2337): EmptyOperator, chain, cross_downstream, Label,
a top-level Variable import, @task.bash, and a list on the left of ``>>``.
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


def _edges(spec: dict) -> set[tuple[str, str]]:
    return {(up, t["task_id"]) for t in spec["tasks"] for up in t.get("depends_on", [])}


def _task(spec: dict, task_id: str) -> dict:
    return next(t for t in spec["tasks"] if t["task_id"] == task_id)


_OPS = """
    from airflow.sdk import DAG
    from airflow.providers.standard.operators.bash import BashOperator

    def op(name):
        return BashOperator(task_id=name, bash_command="echo " + name)
"""


# ───────────────────────────── EmptyOperator ─────────────────────────────


def test_empty_operator_compiles_to_a_bash_task_running_true(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, task
        from airflow.providers.standard.operators.empty import EmptyOperator

        @task
        def work() -> None: ...

        with DAG("g"):
            EmptyOperator(task_id="start") >> work() >> EmptyOperator(task_id="end")
    """)
    assert _task(spec, "start") == {"task_id": "start", "type": "bash", "entrypoint": "true"}
    assert _task(spec, "end")["entrypoint"] == "true"
    assert _edges(spec) == {("start", "work"), ("work", "end")}


def test_empty_operator_keeps_its_trigger_rule(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG
        from airflow.providers.standard.operators.empty import EmptyOperator
        with DAG("g"):
            EmptyOperator(task_id="end", trigger_rule="all_done")
    """)
    assert _task(spec, "end")["trigger_rule"] == "all_done"


def test_empty_operator_subclass_is_an_empty_task(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG
        from airflow.providers.standard.operators.empty import EmptyOperator

        class Marker(EmptyOperator):
            pass

        with DAG("g"):
            Marker(task_id="m")
    """)
    assert _task(spec, "m")["entrypoint"] == "true"


def test_empty_operator_from_airflow_sdk_names_the_right_module(monkeypatch, tmp_path):
    # Airflow's airflow.sdk has no EmptyOperator: accepting it here would compile a
    # DAG whose python tasks then fail to import it in the pod.
    message = _refusal(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, EmptyOperator
        with DAG("g"):
            EmptyOperator(task_id="t")
    """)
    assert "airflow.providers.standard.operators.empty" in message


# ─────────────────────── chain and cross_downstream ───────────────────────


@pytest.mark.parametrize("module", ["airflow.sdk", "airflow.models.baseoperator"])
def test_chain_links_each_element_to_the_next(monkeypatch, tmp_path, module):
    spec = _compile(monkeypatch, tmp_path, _OPS + f"""
    from {module} import chain
    with DAG("g"):
        chain(op("a"), [op("b"), op("c")], [op("d"), op("e")], op("f"))
    """)
    assert _edges(spec) == {
        ("a", "b"), ("a", "c"),
        ("b", "d"), ("c", "e"),
        ("d", "f"), ("e", "f"),
    }


def test_chain_accepts_taskflow_calls(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, chain, task

        @task
        def a() -> None: ...

        @task
        def b() -> None: ...

        with DAG("g"):
            chain(a(), b())
    """)
    assert _edges(spec) == {("a", "b")}


def test_chain_refuses_adjacent_lists_of_different_lengths(monkeypatch, tmp_path):
    src = tmp_path / "dag.py"
    src.write_text(textwrap.dedent(_OPS + """
    from airflow.sdk import chain
    with DAG("g"):
        chain([op("a"), op("b")], [op("c")])
    """))
    monkeypatch.setenv("LEOFLOW_PROJECT_CONFIG_JSON", json.dumps({"schema_version": "1.0"}))
    with pytest.raises(ValueError, match="different lengths"):
        compile_dag(str(src), str(tmp_path / "ignored.json"), "img:v1")


@pytest.mark.parametrize("module", ["airflow.sdk", "airflow.models.baseoperator"])
def test_cross_downstream_links_every_pair(monkeypatch, tmp_path, module):
    spec = _compile(monkeypatch, tmp_path, _OPS + f"""
    from {module} import cross_downstream
    with DAG("g"):
        cross_downstream([op("a"), op("b")], [op("c"), op("d")])
    """)
    assert _edges(spec) == {("a", "c"), ("a", "d"), ("b", "c"), ("b", "d")}


# ─────────────────────────── lists on the left ───────────────────────────


def test_list_on_the_left_of_rshift_is_upstream(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, _OPS + """
    with DAG("g"):
        [op("a"), op("b")] >> op("c")
        op("d") << [op("e")]
    """)
    assert _edges(spec) == {("a", "c"), ("b", "c"), ("e", "d")}


def test_list_of_taskflow_calls_on_the_left(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, task

        @task
        def a() -> None: ...

        @task
        def b() -> None: ...

        @task
        def c() -> None: ...

        with DAG("g"):
            [a(), b()] >> c()
    """)
    assert _edges(spec) == {("a", "c"), ("b", "c")}


def test_set_downstream_and_set_upstream(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, _OPS + """
    with DAG("g"):
        a, b, c = op("a"), op("b"), op("c")
        a.set_downstream([b])
        c.set_upstream(b)
    """)
    assert _edges(spec) == {("a", "b"), ("b", "c")}


# ───────────────────────────────── Label ─────────────────────────────────


@pytest.mark.parametrize("module", ["airflow.sdk", "airflow.utils.edgemodifier"])
def test_label_wires_the_edge_and_is_dropped(monkeypatch, tmp_path, module):
    spec = _compile(monkeypatch, tmp_path, _OPS + f"""
    from {module} import Label
    with DAG("g"):
        op("a") >> Label("ok") >> [op("b"), op("c")]
        [op("d"), op("e")] >> Label("join") >> op("f")
        op("h") << Label("back") << op("g")
    """)
    assert _edges(spec) == {
        ("a", "b"), ("a", "c"),
        ("d", "f"), ("e", "f"),
        ("g", "h"),
    }
    assert "label" not in json.dumps(spec)


def test_label_inside_chain(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, _OPS + """
    from airflow.sdk import Label, chain
    with DAG("g"):
        chain(op("a"), Label("then"), op("b"))
    """)
    assert _edges(spec) == {("a", "b")}


# ──────────────────────────────── Variable ────────────────────────────────


@pytest.mark.parametrize("module", ["airflow.sdk", "airflow.models", "airflow.models.variable"])
def test_variable_import_at_module_top_compiles(monkeypatch, tmp_path, module):
    spec = _compile(monkeypatch, tmp_path, f"""
        from airflow.sdk import DAG, task
        from {module} import Variable

        @task
        def read() -> str:
            return Variable.get("region")

        with DAG("g"):
            read()
    """)
    assert _task(spec, "read")["type"] == "python"


@pytest.mark.parametrize("method", ["get", "set"])
def test_variable_call_at_module_top_is_refused(monkeypatch, tmp_path, method):
    args = '"region"' if method == "get" else '"region", "eu"'
    message = _refusal(monkeypatch, tmp_path, f"""
        from airflow.sdk import DAG, Variable
        REGION = Variable.{method}({args})
        with DAG("g"):
            pass
    """)
    assert f"Variable.{method}()" in message
    assert "inside the task" in message
    assert "{{ var.value.<key> }}" in message


# ─────────────────────────────── @task.bash ───────────────────────────────


def test_task_bash_compiles_to_a_python_task(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, task

        @task
        def name() -> str:
            return "x"

        @task.bash
        def greet(who: str) -> str:
            return f"echo hello {who}"

        @task.bash(retries=2)
        def done() -> str:
            return "echo done"

        with DAG("g"):
            greet(name()) >> done()
    """)
    greet = _task(spec, "greet")
    assert greet["type"] == "python"
    assert greet["entrypoint"] == "dag:greet"
    assert greet["xcom_input"] == {"who": ["name"]}
    assert _task(spec, "done")["retries"] == 2
    assert _edges(spec) == {("name", "greet"), ("greet", "done")}


def test_task_bash_override_keeps_it_a_bash_task(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, task

        @task.bash
        def cmd() -> str:
            return "true"

        with DAG("g"):
            cmd.override(task_id="first")() >> cmd.override(task_id="second")()
    """)
    assert _task(spec, "first")["entrypoint"] == "dag:cmd"
    assert _edges(spec) == {("first", "second")}


def test_task_bash_refuses_on_failure_callback(monkeypatch, tmp_path):
    message = _refusal(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, task

        def alert(context) -> None: ...

        @task.bash(on_failure_callback=alert)
        def cmd() -> str:
            return "false"

        with DAG("g"):
            cmd()
    """)
    assert "@task.bash" in message
    assert "'cmd'" in message
