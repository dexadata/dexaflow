"""Golden fidelity gate (ADR 0024): the production compiler — now backed by the
structural shim, no Apache Airflow — must reproduce the exact dag.json the real
Airflow-based compiler produced for every shipped example.

The golden files were generated once with apache-airflow installed
(``LEOFLOW_PARSER_BACKEND=airflow``) and the pass-through ``image`` stripped; the
test compiles each example with the default (shim) backend and compares. This
locks fidelity in CI without installing Airflow.

Regenerate (needs an Airflow venv), per example:
    LEOFLOW_PARSER_BACKEND=airflow leoflow_parser compile \\
        --source examples/<name>/dag.py --config examples/<name>/dexaflow.yaml \\
        --output golden.json --image leoflow/<name>:golden --dag-version v1
    # then drop the "image" key.
"""
from __future__ import annotations

import json
from pathlib import Path

import pytest

from leoflow_parser.compiler import compile_dag

REPO = Path(__file__).resolve().parents[2]
GOLDEN_DIR = Path(__file__).parent / "golden"
GOLDENS = sorted(GOLDEN_DIR.glob("*.json"))


@pytest.mark.parametrize("golden_file", GOLDENS, ids=lambda p: p.stem)
def test_compiler_matches_golden(golden_file, monkeypatch):
    """Each golden runs the parser against a real example DAG. PyYAML
    moved out of the parser at the alpha cut (handshake is now Go→JSON via
    LEOFLOW_PROJECT_CONFIG_JSON), so the test loads the example's
    dexaflow.yaml here with the dev-only PyYAML and seeds the env var."""
    import yaml  # dev-only; not a parser runtime dep

    name = golden_file.stem
    example = REPO / "examples" / name
    want = json.loads(golden_file.read_text())

    cfg = yaml.safe_load((example / "dexaflow.yaml").read_text()) or {}
    monkeypatch.setenv("LEOFLOW_PROJECT_CONFIG_JSON", json.dumps(cfg))

    got = compile_dag(
        str(example / "dag.py"),
        str(example / "dexaflow.yaml"),
        image=f"leoflow/{name}:golden",
        dag_version="v1",
    )
    got.pop("image", None)  # pass-through, stripped from the golden too

    assert json.dumps(got, sort_keys=True) == json.dumps(want, sort_keys=True), (
        f"\n{name}: shim-backed compiler differs from the Airflow golden.\n"
        f"got:    {json.dumps(got, sort_keys=True)}\n"
        f"golden: {json.dumps(want, sort_keys=True)}"
    )
