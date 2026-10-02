"""Tests for the Leoflow DAG compiler against fixture DAGs."""
from __future__ import annotations

import json
from pathlib import Path

import pytest
from jsonschema.validators import Draft202012Validator

from leoflow_parser.compiler import compile_dag

FIXTURES = Path(__file__).parent / "fixtures"
SCHEMA_PATH = Path(__file__).parents[2] / "docs" / "api" / "dag-schema.json"


@pytest.fixture(scope="session")
def dag_schema() -> dict:
    return json.loads(SCHEMA_PATH.read_text())


def _compile(monkeypatch, tmp_path: Path, fixture: str, dag_id: str) -> dict:
    # Post-migration: config rides on LEOFLOW_PROJECT_CONFIG_JSON (the Go CLI
    # sets this in production). The path arg is preserved for error messages
    # but no longer read when the env var is set.
    monkeypatch.setenv(
        "LEOFLOW_PROJECT_CONFIG_JSON",
        json.dumps({"dag_id": dag_id, "python_version": "3.11"}),
    )
    return compile_dag(
        str(FIXTURES / f"{fixture}.py"), str(tmp_path / "ignored.json"), "test:v1"
    )


def _tasks_by_id(spec: dict) -> dict:
    return {task["task_id"]: task for task in spec["tasks"]}


def test_simple_linear(monkeypatch, tmp_path, dag_schema):
    spec = _compile(monkeypatch, tmp_path,"simple_linear", "simple_linear")
    Draft202012Validator(dag_schema).validate(spec)

    assert spec["dag_id"] == "simple_linear"
    tasks = _tasks_by_id(spec)
    assert set(tasks) == {"extract", "load"}
    assert tasks["extract"]["type"] == "python"
    assert tasks["extract"]["entrypoint"] == "simple_linear:extract"
    assert tasks["load"]["depends_on"] == ["extract"]
    # load(extract()) binds load's 'value' param to extract's output (TaskFlow
    # value passing) — the parser must record it so the agent injects the XCom.
    # Single-upstream still emits a 1-element list (uniform schema; fan-in is
    # `{param: [a, b, c]}`, single is `{param: [a]}`).
    assert tasks["load"]["xcom_input"] == {"value": ["extract"]}
    assert "xcom_input" not in tasks["extract"]


def test_taskflow_literal_call_args_are_captured(monkeypatch, tmp_path, dag_schema):
    """#115: shard(0), shard(1) bind literal kwargs at DAG-build time.

    The compiler captures them into the per-task ``call_args`` map. The
    runtime delivers them as LEOFLOW_CALL_ARGS_JSON so the user function
    receives n=0, n=1 etc. — without this, the function runs with no args
    and raises TypeError. xcom_input is absent on shard (no upstream
    binding); XCom precedence is owned by the runtime
    (see test_run_xcom_wins_over_literal_call_arg). The field is named
    call_args (not params) to leave Airflow's DAG-run params term free (#148).
    """
    spec = _compile(monkeypatch, tmp_path,"literal_params", "literal_params")
    Draft202012Validator(dag_schema).validate(spec)

    tasks = _tasks_by_id(spec)
    # The three shard invocations create three distinct tasks (Airflow names
    # them shard, shard__1, shard__2 — match by prefix to stay robust against
    # the SDK's naming).
    shards = sorted(k for k in tasks if k.startswith("shard"))
    assert len(shards) == 3, f"expected 3 shard tasks, got {shards}"
    values = sorted(tasks[s].get("call_args", {}).get("n") for s in shards)
    assert values == [0, 1, 2], f"shard literal call_args not captured: {values}"

    # Shards have no XCom inputs (they only take a literal).
    for s in shards:
        assert "xcom_input" not in tasks[s], f"{s} should have no xcom_input"


def test_branching(monkeypatch, tmp_path, dag_schema):
    spec = _compile(monkeypatch, tmp_path,"branching", "branching")
    Draft202012Validator(dag_schema).validate(spec)

    tasks = _tasks_by_id(spec)
    assert set(tasks) == {"start", "left", "right"}
    assert tasks["left"]["depends_on"] == ["start"]
    assert tasks["right"]["depends_on"] == ["start"]
    assert "depends_on" not in tasks["start"]


def test_mixed_operators(monkeypatch, tmp_path, dag_schema):
    spec = _compile(monkeypatch, tmp_path,"mixed_operators", "mixed_operators")
    Draft202012Validator(dag_schema).validate(spec)

    tasks = _tasks_by_id(spec)
    assert tasks["extract"]["type"] == "bash"
    assert tasks["extract"]["entrypoint"] == "echo extract"
    assert tasks["transform"]["type"] == "python"
    assert tasks["transform"]["depends_on"] == ["extract"]
    assert tasks["notify"]["type"] == "airflow_operator"  # ADR 0047: HttpOperator runs in a pod
    assert tasks["notify"]["operator_class"] == "airflow.providers.http.operators.http.HttpOperator"
    assert tasks["notify"]["operator_args"]["method"] == "POST"
    assert tasks["notify"]["operator_args"]["endpoint"] == "https://example.com/hook"
    assert tasks["notify"]["depends_on"] == ["transform"]


# Issue #225: BranchPythonOperator (and friends) were silently translated to a
# plain `python` task by the legacy substring-match in _operator_type — the
# parser would compile a branching DAG and every "skipped" branch would
# execute at runtime. The fix refuses these at compile time with a clear
# error; this test locks the contract.
def test_branching_python_operator_is_rejected(monkeypatch, tmp_path):
    with pytest.raises(ValueError) as excinfo:
        _compile(monkeypatch, tmp_path, "branching_python_operator", "branching_python_operator")
    msg = str(excinfo.value).lower()
    assert "branchpythonoperator" in msg or "branching" in msg, (
        f"the compile error must name branching so the user understands; got: {excinfo.value}"
    )
    assert "not supported" in msg or "supported" in msg, (
        f"the compile error should point at the supported list; got: {excinfo.value}"
    )


def test_missing_dag_raises(monkeypatch, tmp_path):
    empty = tmp_path / "empty.py"
    empty.write_text("x = 1\n")
    monkeypatch.setenv("LEOFLOW_PROJECT_CONFIG_JSON", json.dumps({"dag_id": "nope"}))
    with pytest.raises(ValueError):
        compile_dag(str(empty), str(tmp_path / "ignored.json"), "test:v1")


def test_min_idle_workers_is_never_emitted(monkeypatch, tmp_path, dag_schema):
    """Pin the dormant warm-pool seam: the compiler MUST NOT emit
    ``min_idle_workers`` into dag.json.

    The field exists downstream (dag-schema.json accepts it; the Go DAGSpec
    carries it; EffectiveMinIdle and the scheduler store read it) but has no
    author entry point today: it is absent from the authoring schema
    (dexaflow.yaml, additionalProperties:false) and the compiler never writes
    it. So a compiled artifact never carries the key and the Go
    ``spec.MinIdleWorkers`` is always 0. This test locks that inert contract so
    the seam cannot silently become half-wired.

    Even a config that smuggles the key in is dropped: the compiler copies only
    whitelisted config keys, never arbitrary ones.

    When you DO wire author-declared warmth later, this test must change
    deliberately, and the change is not just here — you must ALSO: add
    ``min_idle_workers`` to the authoring schema (leoflow-yaml-schema.json) and
    have the compiler emit it; keep the staging exclusion (a DAG with
    ``staging.enabled`` falls back to a dedicated pod and cannot be warm); and
    lead the operator docs with the safe default (0 = scale-to-zero).
    """
    monkeypatch.setenv(
        "LEOFLOW_PROJECT_CONFIG_JSON",
        json.dumps(
            {
                "dag_id": "simple_linear",
                "python_version": "3.11",
                # An author trying to smuggle warmth in via the project config:
                # the compiler must not pass it through.
                "min_idle_workers": 3,
            }
        ),
    )
    spec = compile_dag(
        str(FIXTURES / "simple_linear.py"), str(tmp_path / "ignored.json"), "test:v1"
    )
    Draft202012Validator(dag_schema).validate(spec)
    assert "min_idle_workers" not in spec, (
        "the compiler emitted min_idle_workers; the warm-pool seam is dormant "
        "and must stay unwired. To wire it later: add the key to the authoring "
        "schema (leoflow-yaml-schema.json) AND emit it here; keep the staging "
        "exclusion (staging.enabled => dedicated pod, never warm); and lead the "
        "operator docs with the safe default (0 = scale-to-zero). Then update "
        f"this test deliberately. Got spec keys: {sorted(spec)}"
    )
