"""Author-declared DAG-run params (typed defaults + JSON-Schema).

A DAG may declare ``params=`` so its run conf gets defaults and trigger-time
validation. The compiler emits them into ``spec["params"]`` as
``{name: {default, schema}}``: a bare value has an empty schema, a ``Param``
carries the JSON-Schema built from its kwargs. A DAG that declares no params
emits no ``params`` key (back-compatible shape).
"""
from __future__ import annotations

import json
import textwrap
from pathlib import Path

from leoflow_parser.compiler import compile_dag


def _compile(monkeypatch, tmp_path: Path, body: str, config: dict | None = None) -> dict:
    monkeypatch.setenv(
        "LEOFLOW_PROJECT_CONFIG_JSON",
        json.dumps(config or {"schema_version": "1.0"}),
    )
    src = tmp_path / "dag.py"
    src.write_text(textwrap.dedent(body))
    return compile_dag(str(src), str(tmp_path / "ignored.json"), "img:v1", dag_version="v1")


def test_bare_value_param_emits_default_and_empty_schema(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, task
        @task
        def a() -> None: ...
        with DAG("g", params={"limit": 5}):
            a()
    """)
    assert spec["params"] == {"limit": {"default": 5, "schema": {}}}


def test_typed_param_emits_default_and_json_schema(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, Param, task
        @task
        def a() -> None: ...
        with DAG("g", params={"n": Param(3, type="integer", minimum=1)}):
            a()
    """)
    assert spec["params"] == {
        "n": {"default": 3, "schema": {"type": "integer", "minimum": 1}}
    }


def test_param_import_from_models_path_also_resolves(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, task
        from airflow.models.param import Param
        @task
        def a() -> None: ...
        with DAG("g", params={"env": Param("prod", enum=["dev", "prod"])}):
            a()
    """)
    assert spec["params"] == {
        "env": {"default": "prod", "schema": {"enum": ["dev", "prod"]}}
    }


def test_mixed_params_bare_and_typed(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, Param, task
        @task
        def a() -> None: ...
        with DAG("g", params={"limit": 5, "name": Param("x", type="string", maxLength=8)}):
            a()
    """)
    assert spec["params"] == {
        "limit": {"default": 5, "schema": {}},
        "name": {"default": "x", "schema": {"type": "string", "maxLength": 8}},
    }


def test_no_params_declared_emits_no_params_key(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, task
        @task
        def a() -> None: ...
        with DAG("g"):
            a()
    """)
    assert "params" not in spec


def test_required_param_no_default_omits_default_key(monkeypatch, tmp_path):
    # A Param with no default is required: the compiler omits the default key so
    # the control plane can tell required apart from a null default.
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, Param, task
        @task
        def a() -> None: ...
        with DAG("g", params={"n": Param(type="integer", minimum=1)}):
            a()
    """)
    assert spec["params"] == {"n": {"schema": {"type": "integer", "minimum": 1}}}
    assert "default" not in spec["params"]["n"]


def test_explicit_null_default_is_kept(monkeypatch, tmp_path):
    # An explicit None default is a real default of null, distinct from required.
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, Param, task
        @task
        def a() -> None: ...
        with DAG("g", params={"n": Param(None, type="integer")}):
            a()
    """)
    assert "default" in spec["params"]["n"]
    assert spec["params"]["n"]["default"] is None


def _task(spec: dict, task_id: str) -> dict:
    return next(t for t in spec["tasks"] if t["task_id"] == task_id)


def test_bash_task_params_are_captured(monkeypatch, tmp_path):
    # BashOperator(params=...) feeds {{ params.x }} in its command; the compiler
    # kept only bash_command, so the task params never reached the run (#1843).
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, Param
        from airflow.providers.standard.operators.bash import BashOperator
        with DAG("g"):
            BashOperator(task_id="b", bash_command="echo {{ params.table }}",
                         params={"table": "orders", "n": Param(3, type="integer")})
    """)
    assert _task(spec, "b")["params"] == {"table": "orders", "n": 3}


def test_taskflow_task_params_are_captured(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, task
        @task(params={"table": "orders"})
        def a() -> None: ...
        with DAG("g"):
            a()
    """)
    assert _task(spec, "a")["params"] == {"table": "orders"}


def test_task_params_omit_dag_params_and_required_ones(monkeypatch, tmp_path):
    # Airflow merges the DAG's params into each task's; the control plane layers
    # the DAG defaults itself, so a task carries only what it declares or
    # overrides. A required Param has no value to carry.
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, Param, task
        @task(params={"region": "eu", "limit": 5, "must": Param(type="string")})
        def a() -> None: ...
        @task
        def b() -> None: ...
        with DAG("g", params={"region": "us", "limit": 5}):
            a()
            b()
    """)
    assert _task(spec, "a")["params"] == {"region": "eu"}
    assert "params" not in _task(spec, "b")


def test_task_params_must_be_json(monkeypatch, tmp_path):
    import pytest

    with pytest.raises(ValueError, match="param 'when' on task 'b'"):
        _compile(monkeypatch, tmp_path, """
            import datetime
            from airflow.sdk import DAG
            from airflow.providers.standard.operators.bash import BashOperator
            with DAG("g"):
                BashOperator(task_id="b", bash_command="echo",
                             params={"when": datetime.date(2026, 1, 1)})
        """)
