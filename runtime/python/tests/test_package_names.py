"""The authoring package's two names, checked without the Task SDK.

test_leoflow_authoring.py needs the real SDK and skips without it; this guard
runs in every job: the `leoflow` package must stay a pure re-export of
`dexaflow`, so the two can never drift apart.
"""
from __future__ import annotations

import ast
import pathlib

_PKG = pathlib.Path(__file__).resolve().parents[1]


def test_leoflow_package_only_reexports_dexaflow():
    tree = ast.parse((_PKG / "leoflow" / "__init__.py").read_text())
    imports = [n for n in tree.body if isinstance(n, ast.ImportFrom) and n.module != "__future__"]
    assert imports, "leoflow must import from dexaflow"
    assert all(n.module == "dexaflow" for n in imports)
    kinds = (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef)
    defined = [n for n in tree.body if isinstance(n, kinds)]
    assert not defined, "leoflow must define nothing of its own"


def test_dexaflow_package_exists():
    assert (_PKG / "dexaflow" / "__init__.py").is_file()
