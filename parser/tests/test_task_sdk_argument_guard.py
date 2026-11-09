"""Every argument of the pinned Task SDK's BaseOperator and DAG is either mapped
into dag.json, refused at compile, or listed as accepted and ignored (#2449).

A Task SDK bump that adds an argument fails here, in the same pull request,
instead of compiling DAGs that use it and dropping it without a word, which is
how trigger_rule in default_args, end_date and the DAG callbacks were lost.

The parser itself has no dependencies, so this needs the Task SDK installed
next to it: it skips in the parser job and runs in the runtime authoring
contract job, which installs the version runtime/Dockerfile pins. The SDK is
read in a separate interpreter, because the parser's tests put the structural
shim on sys.path under the same ``airflow`` name.
"""
from __future__ import annotations

import json
import subprocess
import sys

import pytest

from leoflow_parser import compiler

_LIST_ARGUMENTS = """
import inspect, json
from airflow.sdk import DAG, BaseOperator
def names(fn):
    return sorted(n for n, p in inspect.signature(fn).parameters.items()
                  if n != "self" and p.kind not in (p.VAR_KEYWORD, p.VAR_POSITIONAL))
print(json.dumps({"task": names(BaseOperator.__init__), "dag": names(DAG.__init__)}))
"""


@pytest.fixture(scope="module")
def sdk_arguments() -> dict[str, list[str]]:
    probe = subprocess.run([sys.executable, "-I", "-c", _LIST_ARGUMENTS],
                           capture_output=True, text=True, check=False)
    if probe.returncode != 0:
        if "No module named 'airflow" in probe.stderr:
            pytest.skip("the Airflow Task SDK is not installed")
        raise AssertionError(probe.stderr)
    return json.loads(probe.stdout)


def _unclassified(names, *tables) -> list[str]:
    return [name for name in names if not any(name in table for table in tables)]


def test_every_task_sdk_operator_argument_is_classified(sdk_arguments):
    missing = _unclassified(
        sdk_arguments["task"], compiler._MAPPED_TASK_ARGS, compiler._COSMETIC_TASK_ARGS,
        compiler._UNHONORED_TASK_ARGS, compiler._REFUSED_TASK_CALLBACKS)
    assert not missing, (
        f"BaseOperator arguments the compiler neither maps, refuses nor ignores on "
        f"purpose: {missing}. Map each one, refuse it in _UNHONORED_TASK_ARGS, or "
        "list it in _COSMETIC_TASK_ARGS with the reason it changes nothing at run time.")


def test_every_task_sdk_dag_argument_is_classified(sdk_arguments):
    missing = _unclassified(
        sdk_arguments["dag"], compiler._MAPPED_DAG_ARGS, compiler._COSMETIC_DAG_ARGS,
        compiler._UNHONORED_DAG_ARGS)
    assert not missing, (
        f"DAG arguments the compiler neither maps, refuses nor ignores on purpose: "
        f"{missing}. Map each one, refuse it in _UNHONORED_DAG_ARGS, or list it in "
        "_COSMETIC_DAG_ARGS with the reason it changes nothing at run time.")


def test_the_tables_do_not_overlap():
    task_tables = [set(compiler._MAPPED_TASK_ARGS), set(compiler._COSMETIC_TASK_ARGS),
                   set(compiler._UNHONORED_TASK_ARGS), set(compiler._REFUSED_TASK_CALLBACKS)]
    dag_tables = [set(compiler._MAPPED_DAG_ARGS), set(compiler._COSMETIC_DAG_ARGS),
                  set(compiler._UNHONORED_DAG_ARGS)]
    for tables in (task_tables, dag_tables):
        seen: set[str] = set()
        for table in tables:
            assert not seen & table, f"listed twice: {sorted(seen & table)}"
            seen |= table
