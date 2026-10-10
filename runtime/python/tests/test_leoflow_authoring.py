"""The authoring names a ``dag.py`` imports must resolve inside the TASK image (#17).

A ``dag.py`` is not a compile-time-only artifact. ``runner.py`` re-imports the
module for every ``python`` task — ``importlib.import_module(module_name)`` at
runner.py:135 — so every top-level import in the user's DAG runs again inside
the task pod, and again inside the Lite per-DAG venv.

``leoflow`` was reachable only from ``parser/leoflow_parser/_shim``, which the
compiler puts on ``sys.path`` for the duration of a parse and nowhere else. So
the hybrid shape we document — ``from leoflow import dbt_group`` over a
``dag.py`` with Python tasks around the group (ADR 0043) — compiled green and
then died with ``ModuleNotFoundError: No module named 'leoflow'`` on the first
line of the DAG, in Lite and in Pro alike.

Nothing caught it because the only mixed-mode e2e wires BashOperators around the
group, and a bash task never imports ``dag.py``.

These tests run against the REAL task SDK at the version the base image pins, in
a job that installs nothing else — the environment a task pod actually has. An
importorskip here would be worse than no test: it would go green in exactly the
CI where the dependency is absent, which is the CI we have.
"""
from __future__ import annotations

import ast
import pathlib

import pytest

# The repo idiom for a dependency-gated suite (see test_dbt_adapter_contracts.py):
# skip where the dependency is deliberately absent, and run it for real in a job
# that installs it. The main runtime job installs no Airflow on purpose, so this
# file skips there; `runtime-authoring-contract` in ci.yaml installs the Task SDK
# at the version runtime/Dockerfile pins and hard-checks both imports BEFORE
# pytest, so the dedicated job cannot go green by skipping everything.
pytest.importorskip("airflow.sdk", reason="needs the real Task SDK; see runtime-authoring-contract")

from airflow.providers.standard.operators.python import PythonOperator  # noqa: E402
from airflow.sdk import DAG  # noqa: E402

_SHIM = (
    pathlib.Path(__file__).resolve().parents[3]
    / "parser" / "leoflow_parser" / "_shim" / "dexaflow" / "__init__.py"
)


def test_dbt_group_is_importable_at_runtime():
    """The bare import, which is line 1 of the documented hybrid example."""
    from leoflow import dbt_group

    assert callable(dbt_group)


def test_documented_hybrid_example_imports_and_wires():
    """The example at website/content/author-dags/dbt.md, verbatim in shape.

    `pull >> models` dispatches to the REAL operator's __rshift__, so the group
    placeholder has to be something the SDK accepts as a task — not a bare stub
    with its own __rshift__, which is why this derives from the real BaseOperator.
    """
    from leoflow import dbt_group

    def extract(): ...
    def notify(): ...

    with DAG("sales", schedule="@daily") as dag:
        pull = PythonOperator(task_id="extract", python_callable=extract)
        models = dbt_group("transform")
        ping = PythonOperator(task_id="notify", python_callable=notify)

        pull >> models >> ping

    assert models.task_id == "transform"
    assert set(dag.task_dict) == {"extract", "transform", "notify"}
    assert dag.task_dict["extract"].downstream_task_ids == {"transform"}
    assert dag.task_dict["transform"].downstream_task_ids == {"notify"}


def test_dbt_group_refuses_to_execute():
    """A dbt_group is expanded into one task per model by the Go compiler; the
    placeholder is never scheduled. If one ever reaches a pod, say so loudly
    rather than succeeding silently and reporting a green run that did nothing.
    """
    from leoflow import dbt_group

    with pytest.raises(RuntimeError, match="expanded at compile time"):
        dbt_group("transform").execute({})


def test_runtime_package_exports_match_the_parser_shim():
    """Drift guard. Two modules named `leoflow` now exist: the parse-time shim
    (dependency-free by ADR 0024, deriving from the parser's own airflow shim)
    and this runtime one (deriving from the real SDK). A name added to one and
    not the other parses green and then fails in the pod — the #17 shape again.

    Compared by AST so the guard needs neither the parser nor its shim importable.
    """
    import dexaflow

    def public_names(path: pathlib.Path) -> set[str]:
        # Signatures, not just names: a `granularity=` added to one twin and not
        # the other parses green and dies in the pod, which is the shape this
        # guards. AsyncFunctionDef too, so an async export cannot drift unseen.
        tree = ast.parse(path.read_text())
        return {
            (n.name, ast.unparse(n.args))
            for n in tree.body
            if isinstance(n, (ast.FunctionDef, ast.AsyncFunctionDef)) and not n.name.startswith("_")
        } | {
            n.name for n in tree.body
            if isinstance(n, ast.ClassDef) and not n.name.startswith("_")
        }

    runtime_names = public_names(pathlib.Path(dexaflow.__file__))
    assert runtime_names == public_names(_SHIM), (
        "the runtime `dexaflow` package and the parser shim must export the same "
        "public names; a DAG that parses must also import inside the task pod"
    )


@pytest.mark.parametrize("package", ["dexaflow", "leoflow"])
def test_both_package_names_import_dbt_group(package):
    """`dexaflow` is the authoring package; DAGs written before the rename import
    `leoflow`, and they run in the same task image."""
    module = __import__(package)
    assert callable(module.dbt_group)


def test_leoflow_is_the_same_package():
    """Not a copy: the old name re-exports the new one, so a placeholder made
    through either import is the same type the compiler and runner look for."""
    import dexaflow
    import leoflow

    assert leoflow.dbt_group is dexaflow.dbt_group
    assert leoflow._DbtGroup is dexaflow._DbtGroup
