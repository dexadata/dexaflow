"""Regression tests for the migration from in-parser PyYAML to Go-marshalled JSON.

Before this migration the parser shipped a 5890-line vendored copy of PyYAML
just to read ``dexaflow.yaml`` once. The CLI (Go) now parses the YAML with
``gopkg.in/yaml.v3`` and hands the result to the parser as JSON via
``LEOFLOW_PROJECT_CONFIG_JSON`` — single source of truth, zero third-party
Python deps.

These tests are the regression contract: if anyone reintroduces a YAML read
path or accidentally drops the env-var handshake, the failure surfaces here
instead of as a silent breakage of every ``dexaflow compile``.
"""
from __future__ import annotations

import json
import os
import sys
import textwrap

import pytest

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.dirname(HERE))

from leoflow_parser.compiler import compile_dag  # noqa: E402  (sys.path is set above)


def _write_dag(tmp_path, body: str):
    src = tmp_path / "dag.py"
    src.write_text(textwrap.dedent(body))
    return src


_TRIVIAL_DAG = """
    from airflow.sdk import DAG
    from airflow.providers.standard.operators.python import PythonOperator

    def _f():
        return None

    with DAG(dag_id="env_var_demo") as dag:
        t = PythonOperator(task_id="t", python_callable=_f)
"""


def test_compile_dag_reads_config_from_env_var(tmp_path, monkeypatch):
    """Production contract: the Go CLI marshals the resolved project config
    into ``LEOFLOW_PROJECT_CONFIG_JSON``; the parser reads from there, not
    from disk. The config_path argument is preserved for error messages only.
    """
    src = _write_dag(tmp_path, _TRIVIAL_DAG)
    cfg = {
        "schema_version": "1.0",
        "dag_id": "env_var_demo",
        "owner": "carried-from-env",
        "tags": ["from-env-marker"],
    }
    monkeypatch.setenv("LEOFLOW_PROJECT_CONFIG_JSON", json.dumps(cfg))

    # The config path is intentionally bogus — the parser MUST NOT touch disk
    # for config when the env var is set. If it does, this test fails with a
    # FileNotFoundError.
    spec = compile_dag(str(src), "/nonexistent/dexaflow.yaml", "img:v1")

    assert spec["owner"] == "carried-from-env"
    assert spec["tags"] == ["from-env-marker"]


def test_compile_dag_env_var_takes_precedence_over_disk(tmp_path, monkeypatch):
    """When the env var IS set, the parser MUST NOT silently fall back to
    a stale on-disk yaml/json. Earlier behavior would have re-parsed the file
    and lost defaults the Go side had applied."""
    src = _write_dag(tmp_path, _TRIVIAL_DAG)
    cfg_path = tmp_path / "dexaflow.yaml"
    # Deliberately invalid content. If the parser reads this file the test
    # fails with a parse error.
    cfg_path.write_text("@@@ not valid yaml or json @@@\n")

    env_cfg = {"schema_version": "1.0", "dag_id": "env_var_demo", "owner": "env-wins"}
    monkeypatch.setenv("LEOFLOW_PROJECT_CONFIG_JSON", json.dumps(env_cfg))

    spec = compile_dag(str(src), str(cfg_path), "img:v1")
    assert spec["owner"] == "env-wins"


def test_compile_dag_falls_back_to_json_file_when_env_unset(tmp_path, monkeypatch):
    """Test-time / direct-invocation contract: when no env var is set, the
    parser still accepts a JSON config from disk. This keeps the function
    callable from tests without the Go CLI in the loop."""
    src = _write_dag(tmp_path, _TRIVIAL_DAG)
    cfg_path = tmp_path / "leoflow.json"
    cfg_path.write_text(json.dumps({
        "schema_version": "1.0",
        "dag_id": "env_var_demo",
        "owner": "from-json-file",
    }))
    monkeypatch.delenv("LEOFLOW_PROJECT_CONFIG_JSON", raising=False)

    spec = compile_dag(str(src), str(cfg_path), "img:v1")
    assert spec["owner"] == "from-json-file"


def test_compile_dag_fails_clearly_without_env_and_without_json(tmp_path, monkeypatch):
    """Misconfiguration contract: no env var AND no JSON file → loud failure
    naming the env var, not a silent fall-through to a stale path."""
    src = _write_dag(tmp_path, _TRIVIAL_DAG)
    monkeypatch.delenv("LEOFLOW_PROJECT_CONFIG_JSON", raising=False)

    with pytest.raises(Exception) as excinfo:
        compile_dag(str(src), str(tmp_path / "missing.json"), "img:v1")
    msg = str(excinfo.value)
    assert "LEOFLOW_PROJECT_CONFIG_JSON" in msg, (
        "the error must name the env var so operators know what to set; got: " + msg
    )


def test_compile_dag_rejects_yaml_path_with_actionable_error(tmp_path, monkeypatch):
    """Migration-era contract: if someone passes a ``.yaml`` path (the old
    shape) without setting the env var, fail with a message that points at
    the env var rather than silently dying on missing PyYAML.
    """
    src = _write_dag(tmp_path, _TRIVIAL_DAG)
    cfg_path = tmp_path / "dexaflow.yaml"
    cfg_path.write_text("dag_id: env_var_demo\nowner: yaml-author\n")
    monkeypatch.delenv("LEOFLOW_PROJECT_CONFIG_JSON", raising=False)

    with pytest.raises(Exception) as excinfo:
        compile_dag(str(src), str(cfg_path), "img:v1")
    assert "LEOFLOW_PROJECT_CONFIG_JSON" in str(excinfo.value) or "JSON" in str(excinfo.value)


def test_parser_package_has_no_vendored_yaml():
    """The 5890-line PyYAML vendor was removed at the migration cut. This
    test fails the build if anyone reintroduces it — keeping the parser at
    zero third-party runtime deps (ADR 0024 + the alpha simplification)."""
    import leoflow_parser

    pkg_dir = os.path.dirname(os.path.abspath(leoflow_parser.__file__))
    vendor_dir = os.path.join(pkg_dir, "_vendor", "yaml")
    assert not os.path.isdir(vendor_dir), (
        "_vendor/yaml/ reappeared in the parser — the migration to Go-marshalled "
        "JSON config was meant to delete it. If you genuinely need YAML in the "
        "parser, revisit the design instead of re-vendoring."
    )


def test_parser_does_not_put_yaml_on_sys_path():
    """The earlier __init__.py inserted ``_vendor`` onto sys.path so
    ``import yaml`` resolved to the vendored copy. After the migration the
    parser must NOT manipulate sys.path for that purpose — otherwise the
    package would silently advertise an ``import yaml`` capability that the
    parser itself no longer uses, leaking a fragile coupling to user DAGs."""
    # Reload to defeat any earlier import. We just check the path snapshot.
    sys_path = list(sys.path)
    suspects = [p for p in sys_path if p.endswith(os.path.join("leoflow_parser", "_vendor"))]
    assert not suspects, (
        "leoflow_parser put a `_vendor` dir on sys.path: " + repr(suspects) +
        " — that's the old PyYAML coupling. Remove it."
    )
