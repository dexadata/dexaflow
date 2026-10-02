"""The compiler carries declared variables/connections from dexaflow.yaml into
the compiled dag.json (ADR 0045, ADR 0055).

These are the Airflow-native words. They are distinct from ``connectors:`` (pip
provider packages, ADR 0038) one letter away — declaring connections must never
touch connectors, and vice versa.
"""
from __future__ import annotations

import json
import os
import sys
import textwrap

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.dirname(HERE))

from leoflow_parser.compiler import compile_dag  # noqa: E402  (sys.path set above)

_TRIVIAL_DAG = """
    from airflow.sdk import DAG
    from airflow.providers.standard.operators.python import PythonOperator

    def _f():
        return None

    with DAG(dag_id="declared_demo") as dag:
        t = PythonOperator(task_id="t", python_callable=_f)
"""


def _write_dag(tmp_path):
    src = tmp_path / "dag.py"
    src.write_text(textwrap.dedent(_TRIVIAL_DAG))
    return src


def test_declared_connections_and_variables_land_in_dag_json(tmp_path, monkeypatch):
    src = _write_dag(tmp_path)
    cfg = {
        "schema_version": "1.0",
        "dag_id": "declared_demo",
        "connections": ["warehouse", "webhook"],
        "variables": ["greeting"],
    }
    monkeypatch.setenv("LEOFLOW_PROJECT_CONFIG_JSON", json.dumps(cfg))

    spec = compile_dag(str(src), "/nonexistent/dexaflow.yaml", "img:v1")

    assert spec["connections"] == ["warehouse", "webhook"]
    assert spec["variables"] == ["greeting"]


def test_absent_declarations_are_omitted(tmp_path, monkeypatch):
    src = _write_dag(tmp_path)
    cfg = {"schema_version": "1.0", "dag_id": "declared_demo"}
    monkeypatch.setenv("LEOFLOW_PROJECT_CONFIG_JSON", json.dumps(cfg))

    spec = compile_dag(str(src), "/nonexistent/dexaflow.yaml", "img:v1")

    # Absent declarations are additive/optional: no key at all (empty = declares
    # nothing), so an existing DAG's compiled shape is unchanged.
    assert "connections" not in spec
    assert "variables" not in spec


def test_empty_declaration_lists_are_omitted(tmp_path, monkeypatch):
    src = _write_dag(tmp_path)
    cfg = {
        "schema_version": "1.0",
        "dag_id": "declared_demo",
        "connections": [],
        "variables": [],
    }
    monkeypatch.setenv("LEOFLOW_PROJECT_CONFIG_JSON", json.dumps(cfg))

    spec = compile_dag(str(src), "/nonexistent/dexaflow.yaml", "img:v1")

    assert "connections" not in spec
    assert "variables" not in spec


def test_connectors_is_untouched_by_connections(tmp_path, monkeypatch):
    """connections: (managed connection ids) and connectors: (pip provider
    packages, ADR 0038) are different keys. Declaring connections must not leak
    into the compiled spec as anything connector-shaped, and connectors: is a
    build-time concern the parser never emits into dag.json."""
    src = _write_dag(tmp_path)
    cfg = {
        "schema_version": "1.0",
        "dag_id": "declared_demo",
        "connections": ["warehouse"],
        "connectors": ["postgres"],
    }
    monkeypatch.setenv("LEOFLOW_PROJECT_CONFIG_JSON", json.dumps(cfg))

    spec = compile_dag(str(src), "/nonexistent/dexaflow.yaml", "img:v1")

    assert spec["connections"] == ["warehouse"]
    # connectors is a build/dependency concern; it never appears in dag.json.
    assert "connectors" not in spec
