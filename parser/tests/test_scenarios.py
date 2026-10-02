"""Broad scenario coverage for the shim-backed compiler (ADR 0024).

Supported scenarios assert the resulting structure; unsupported scenarios assert
a clear "not supported by Leoflow" error. Parity of the supported cases with real
Airflow was verified against `LEOFLOW_PARSER_BACKEND=airflow` (schedule forms,
trigger rules, classic PythonOperator, dag= kwarg, dedup, fan-in).
"""
from __future__ import annotations

import json
import textwrap
from pathlib import Path

import pytest

from leoflow_parser.compiler import compile_dag


def _compile(monkeypatch, tmp_path: Path, body: str, config: dict | None = None) -> dict:
    monkeypatch.setenv(
        "LEOFLOW_PROJECT_CONFIG_JSON",
        json.dumps(config or {"schema_version": "1.0"}),
    )
    src = tmp_path / "dag.py"
    src.write_text(textwrap.dedent(body))
    return compile_dag(str(src), str(tmp_path / "ignored.json"), "img:v1", dag_version="v1")


def _task(spec: dict, task_id: str) -> dict:
    return next(t for t in spec["tasks"] if t["task_id"] == task_id)


# ─────────────────────────── supported scenarios ───────────────────────────

@pytest.mark.parametrize("expr,want", [
    ("None", None),
    ('"0 6 * * *"', "0 6 * * *"),
    ('"@daily"', "@daily"),
    ('"@hourly"', "@hourly"),
])
def test_schedule_forms(monkeypatch, tmp_path, expr, want):
    spec = _compile(monkeypatch, tmp_path,f"""
        from airflow.sdk import DAG, task
        @task
        def a() -> None: ...
        with DAG("g", schedule={expr}):
            a()
    """)
    assert spec.get("schedule") == want


def test_classic_python_operator(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path,"""
        from airflow.providers.standard.operators.python import PythonOperator
        from airflow.sdk import DAG
        def work(): ...
        with DAG("g"):
            PythonOperator(task_id="run", python_callable=work)
    """)
    t = _task(spec, "run")
    assert t["type"] == "python" and t["entrypoint"] == "dag:work"


def test_operator_trigger_rule_emitted(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path,"""
        from airflow.providers.standard.operators.bash import BashOperator
        from airflow.sdk import DAG
        with DAG("g"):
            a = BashOperator(task_id="a", bash_command="echo a")
            b = BashOperator(task_id="b", bash_command="echo b", trigger_rule="all_done")
            a >> b
    """)
    assert _task(spec, "b")["trigger_rule"] == "all_done"


def test_duplicate_task_id_is_suffixed(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path,"""
        from airflow.sdk import DAG, task
        @task
        def w() -> None: ...
        with DAG("g"):
            [w() for _ in range(3)]
    """)
    assert {t["task_id"] for t in spec["tasks"]} == {"w", "w__1", "w__2"}


def test_fan_in_list_captures_all_upstream_in_xcom_input(monkeypatch, tmp_path):
    """Fan-in: `combine([part() for _ in range(3)])` binds a list of upstream
    XCom outputs to `combine.xs`. The parser MUST capture every upstream in
    xcom_input so the agent fetches each value and the runtime delivers the
    list to the function. (Single-upstream params still emit a 1-element list,
    keeping the schema uniform.)
    """
    spec = _compile(monkeypatch, tmp_path,"""
        from airflow.sdk import DAG, task
        @task
        def part() -> int: return 1
        @task
        def combine(xs: list) -> None: ...
        with DAG("g"):
            combine([part() for _ in range(3)])
    """)
    combine_task = _task(spec, "combine")
    assert combine_task["depends_on"] == ["part", "part__1", "part__2"]
    assert combine_task["xcom_input"] == {"xs": ["part", "part__1", "part__2"]}


def test_dag_id_selection_with_multiple_dags(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path,"""
        from airflow.sdk import DAG, task
        @task
        def a() -> None: ...
        with DAG("first"):
            a()
        with DAG("second"):
            a()
    """, config={"schema_version": "1.0", "dag_id": "second"})
    assert spec["dag_id"] == "second"


@pytest.mark.parametrize("package", ["dexaflow", "leoflow"])
def test_dbt_group_mixed_with_operators(monkeypatch, tmp_path, package):
    # A dbt_group() placeholder coexists with real operators in one DAG (ADR 0043):
    # it registers like a task, participates in >> wiring, and compiles to a
    # `dbt_group` task the Go compiler later expands into one task per dbt node.
    # The authoring package is `dexaflow`; DAGs written before the rename import
    # it as `leoflow`, which must keep compiling identically.
    spec = _compile(monkeypatch, tmp_path, f"""
        from {package} import dbt_group
        from airflow.providers.standard.operators.python import PythonOperator
        from airflow.sdk import DAG
        def work(): ...
        with DAG("g"):
            extract = PythonOperator(task_id="extract", python_callable=work)
            transform = dbt_group("analytics")
            notify = PythonOperator(task_id="notify", python_callable=work)
            extract >> transform >> notify
    """)
    assert {t["task_id"] for t in spec["tasks"]} == {"extract", "analytics", "notify"}
    grp = _task(spec, "analytics")
    assert grp["type"] == "dbt_group"
    assert grp["depends_on"] == ["extract"]
    assert _task(spec, "notify")["depends_on"] == ["analytics"]


# ────────────────────────── unsupported scenarios ──────────────────────────

@pytest.mark.parametrize("body", [
    # dynamic task mapping
    """
    from airflow.sdk import DAG, task
    @task
    def a(x): ...
    with DAG("g"):
        a.expand(x=[1, 2, 3])
    """,
    # task groups
    """
    from airflow.sdk import DAG
    from airflow.utils.task_group import TaskGroup
    with DAG("g"):
        with TaskGroup("grp"):
            pass
    """,
    # chain helper
    """
    from airflow.sdk import DAG, task, chain
    @task
    def a(): ...
    with DAG("g"):
        a()
    """,
    # NOTE: a top-level provider import (e.g. an amazon S3 operator) is NOT in
    # this list — it is no longer treated as "unsupported". It gets a dedicated
    # actionable message pointing at connectors: (ADR 0038 #2), covered by
    # test_shim_edges.test_top_level_provider_import_gives_actionable_message.
])
def test_unsupported_constructs_error_clearly(monkeypatch, tmp_path, body):
    with pytest.raises(ValueError) as ei:
        _compile(monkeypatch, tmp_path, body)
    assert "not supported by Leoflow" in str(ei.value)


def test_unsupported_trigger_rule_errors(monkeypatch, tmp_path):
    with pytest.raises(ValueError) as ei:
        _compile(monkeypatch, tmp_path, """
            from airflow.providers.standard.operators.bash import BashOperator
            from airflow.sdk import DAG
            with DAG("g"):
                BashOperator(task_id="a", bash_command="x", trigger_rule="none_failed")
        """)
    assert "trigger rule" in str(ei.value)
