"""Leoflow authoring primitives available to a ``dag.py`` at parse time (ADR 0043).

Only structure is recorded here; the heavy config (project, granularity,
connection) lives in ``dexaflow.yaml`` and is resolved by the Go compiler after
parsing. A ``dag.py`` is never executed at runtime, so these stubs exist solely
for the parser/shim.
"""
from __future__ import annotations

from airflow._core import BaseOperator


class _DbtGroup(BaseOperator):
    """Placeholder for a dbt project embedded as a task group. It registers into
    the active DAG and participates in ``>>`` wiring like any task; the compiler
    maps it to a ``dbt_group`` task that the Go compiler later expands into one
    task per dbt node, namespaced under the group name."""

    __leoflow_dbt_group__ = True


def dbt_group(name: str) -> _DbtGroup:
    """Embed a dbt project (configured under ``dexaflow.yaml`` ``dbt_groups: <name>``)
    as a task group. Returns the placeholder operator for ``>>`` wiring; its
    ``task_id`` is the group name."""
    return _DbtGroup(task_id=name)
