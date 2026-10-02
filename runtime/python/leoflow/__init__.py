"""Leoflow authoring primitives, available to a ``dag.py`` at RUN time (#17).

This is the twin of ``parser/leoflow_parser/_shim/leoflow``. The two exist for
opposite halves of a DAG's life and must export the same public names:

* the parser shim is on ``sys.path`` only while the compiler parses, is
  dependency-free by ADR 0024, and derives from the parser's own ``airflow``
  shim;
* this one ships in the task image and the Lite per-DAG venv, and derives from
  the real Task SDK.

A ``dag.py`` is not a compile-time-only artifact: ``runner.py`` re-imports the
module for every ``python`` task, so every top-level import in the user's DAG
runs again inside the task pod. Without this package the hybrid shape we
document — ``from leoflow import dbt_group`` over a ``dag.py`` with Python
tasks around the group (ADR 0043) — compiled green and then died on its own
first line with ``ModuleNotFoundError``, in Lite and in Pro alike.

Deriving from the real ``BaseOperator`` is load-bearing, not tidiness:
``pull >> models`` dispatches to the *real* operator's ``__rshift__``, so a
bare stub with its own ``__rshift__`` would never be consulted.
"""
from __future__ import annotations

from airflow.sdk.bases.operator import BaseOperator


class _DbtGroup(BaseOperator):
    """Placeholder for a dbt project embedded as a task group. It registers into
    the active DAG and participates in ``>>`` wiring like any task; the Go
    compiler expands it into one task per dbt node, namespaced under the group
    name, so this object itself is never scheduled."""

    __leoflow_dbt_group__ = True

    def execute(self, context):  # noqa: ARG002 - the SDK passes it; we never use it
        raise RuntimeError(
            "leoflow: dbt_group is expanded at compile time into one task per dbt "
            "model; this placeholder should never execute. Reaching it means the "
            "compiled DAG was not produced by `leoflow compile`."
        )


def dbt_group(name: str) -> _DbtGroup:
    """Embed a dbt project (configured under ``dexaflow.yaml`` ``dbt_groups: <name>``)
    as a task group. Returns the placeholder operator for ``>>`` wiring; its
    ``task_id`` is the group name."""
    return _DbtGroup(task_id=name)
