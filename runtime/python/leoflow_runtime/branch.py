"""The in-pod branch decision (ADR 0064 D1).

A branch or short-circuit task runs like any python task. Its result is then
read as Airflow reads it: a branch task's result names the task ids to follow,
a short-circuit task's result is a condition. The decision is written to the
file the agent names in ``LEOFLOW_BRANCH_PATH``; the agent reports it with the
task's success, and the control plane checks it again before storing it.
"""

from __future__ import annotations

import json
import os
from collections.abc import Iterable

BRANCH_KIND_ENV = "LEOFLOW_BRANCH_KIND"
BRANCH_PATH_ENV = "LEOFLOW_BRANCH_PATH"
BRANCH_TASK_IDS_ENV = "LEOFLOW_BRANCH_TASK_IDS"
BRANCH_DOWNSTREAM_ENV = "LEOFLOW_BRANCH_DOWNSTREAM"

# The XCom key Airflow's SkipMixin pushes the decision under. Display only: the
# XCom tab shows it as Airflow does, and nothing reads it back.
SKIPMIXIN_KEY = "skipmixin_key"


class BranchDecisionError(RuntimeError):
    """A branch callable returned something Airflow refuses. The task fails and
    takes a retry, as on Airflow."""


def normalise(result, dag_task_ids: set[str]) -> list[str]:
    """Normalise a branch callable's result with Airflow's rules: a ``str`` is
    one id, an iterable of ``str`` is a set of ids, ``None`` is none. Anything
    else, a member that is not a ``str`` or an id the DAG does not declare is
    refused. The ids come back sorted."""
    if isinstance(result, str):
        ids = {result}
    elif result is None:
        ids = set()
    elif isinstance(result, Iterable):
        ids = set(result)
        wrong = sorted((repr(i), type(i).__name__) for i in ids if not isinstance(i, str))
        if wrong:
            raise BranchDecisionError(
                "'branch_task_ids' expected all task IDs are strings. "
                f"Invalid tasks found: {wrong}.")
    else:
        raise BranchDecisionError(
            "'branch_task_ids' must be either None, a task ID, or an Iterable of IDs, "
            f"but got {type(result).__name__!r}.")
    unknown = sorted(ids - dag_task_ids)
    if unknown:
        raise BranchDecisionError(
            f"'branch_task_ids' must contain only valid task_ids. Invalid tasks found: {unknown}.")
    return sorted(ids)


def _ids(env: str) -> list[str]:
    raw = os.environ.get(env)
    return json.loads(raw) if raw else []


def record(result, pushed: dict, log) -> None:
    """Write the decision of a branch or short-circuit task, when the agent asked
    for one, and add the skipmixin_key XCom to ``pushed``. A plain task, or an
    agent that set no path, writes nothing."""
    kind = os.environ.get(BRANCH_KIND_ENV)
    path = os.environ.get(BRANCH_PATH_ENV)
    if not kind or not path:
        return
    children = _ids(BRANCH_DOWNSTREAM_ENV)
    if kind == "short_circuit":
        falsy = not bool(result)
        decision = {"short_circuit_falsy": falsy}
        log(f"short-circuit condition is {'falsy' if falsy else 'truthy'}")
        if falsy and children:
            pushed[SKIPMIXIN_KEY] = {"skipped": children}
    elif kind == "branch":
        follow = normalise(result, set(_ids(BRANCH_TASK_IDS_ENV)))
        for task_id in follow:
            if task_id not in children:
                log(f"branch chose {task_id!r}, which is not a direct downstream task: "
                    "it selects only what lies below it")
        decision = {"follow_task_ids": follow}
        log(f"branch follows {follow}")
        if children:
            pushed[SKIPMIXIN_KEY] = {"followed": [c for c in children if c in follow]}
    else:
        raise BranchDecisionError(f"unknown branch kind {kind!r}")
    with open(path, "w", encoding="utf-8") as fh:
        json.dump(decision, fh)
