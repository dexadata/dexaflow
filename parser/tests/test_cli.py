"""Tests for the parser command-line interface."""
from __future__ import annotations

import json
from pathlib import Path

import pytest
from jsonschema.validators import Draft202012Validator

from leoflow_parser.cli import main

FIXTURES = Path(__file__).parent / "fixtures"
SCHEMA_PATH = Path(__file__).parents[2] / "docs" / "api" / "dag-schema.json"


def test_cli_compile_writes_valid_dag_json(monkeypatch, tmp_path):
    # The Dexaflow Go CLI sets LEOFLOW_PROJECT_CONFIG_JSON when invoking the
    # parser. The --config path is preserved for error messages but is no
    # longer parsed here.
    monkeypatch.setenv("LEOFLOW_PROJECT_CONFIG_JSON", json.dumps({"dag_id": "simple_linear"}))
    config = tmp_path / "ignored.json"
    output = tmp_path / "dag.json"

    code = main(
        [
            "compile",
            "--source", str(FIXTURES / "simple_linear.py"),
            "--config", str(config),
            "--output", str(output),
            "--image", "test:v1",
            "--dag-version", "v9",
        ]
    )

    assert code == 0
    spec = json.loads(output.read_text())
    Draft202012Validator(json.loads(SCHEMA_PATH.read_text())).validate(spec)
    assert spec["dag_version"] == "v9"
    assert spec["image"] == "test:v1"


def test_cli_requires_subcommand():
    with pytest.raises(SystemExit):
        main([])
