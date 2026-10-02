"""Shim of `airflow.sdk`: DAG, the @task TaskFlow decorator, and base operators."""
from __future__ import annotations

import functools

from airflow._core import DAG, BaseOperator, Param, XComArg

__all__ = [
    "DAG",
    "BaseOperator",
    "Param",
    "XComArg",
    "task",
    "dag",
    "EmptyOperator",
    "PythonOperator",
]


class PythonOperator(BaseOperator):
    """Classic PythonOperator (name carries 'Python' -> Dexaflow 'python')."""


class _TaskBranchOperator(PythonOperator):
    """The @task.branch shape. Branching needs scheduler skip-state (ADR 0040
    Phase D) that Dexaflow does not have yet; the 'Branch' in the class name routes
    it to the compiler's clean #225 reject instead of an opaque AttributeError at
    import time."""


class EmptyOperator(BaseOperator):
    """No-op operator."""


def _iter_xcomargs(value):
    """Yield every XComArg in value, recursing through lists/tuples/sets/dicts."""
    if isinstance(value, XComArg):
        yield value
    elif isinstance(value, (list, tuple, set)):
        for item in value:
            yield from _iter_xcomargs(item)
    elif isinstance(value, dict):
        for item in value.values():
            yield from _iter_xcomargs(item)


def task(fn=None, **dec_kwargs):
    """TaskFlow @task: calling the wrapped function builds a python operator and
    returns an XComArg, wiring upstream edges from any XComArg arguments
    (including those nested in lists/dicts, e.g. a fan-in)."""

    def wrap(func):
        @functools.wraps(func)
        def maker(*args, **kwargs):
            # @task(trigger_rule=…, …) decorator kwargs apply to the operator.
            op = PythonOperator(task_id=func.__name__, **dec_kwargs)
            op.python_callable = func
            op.op_args = args
            op.op_kwargs = kwargs
            op.function = func  # parity with the SDK's @task (.function unwrap)
            for value in list(args) + list(kwargs.values()):
                for xarg in _iter_xcomargs(value):
                    op.upstream_task_ids.add(xarg.operator.task_id)
            return XComArg(op)

        def _no_dynamic_mapping(*_a, **_k):
            raise NotImplementedError("dynamic task mapping (.expand/.partial)")

        maker.function = func
        maker.expand = _no_dynamic_mapping
        maker.partial = _no_dynamic_mapping
        return maker

    return wrap(fn) if callable(fn) else wrap


def _task_branch(fn=None, **dec_kwargs):
    """@task.branch: TaskFlow branching. Captured as a Branch-named operator so
    the compiler refuses it with the clear ADR 0040 Phase D message, rather than
    the wrapped ``task`` raising an opaque AttributeError for the missing
    attribute at import."""

    def wrap(func):
        @functools.wraps(func)
        def maker(*args, **kwargs):
            op = _TaskBranchOperator(task_id=func.__name__, **dec_kwargs)
            op.python_callable = func
            op.op_args = args
            op.op_kwargs = kwargs
            op.function = func
            for value in list(args) + list(kwargs.values()):
                for xarg in _iter_xcomargs(value):
                    op.upstream_task_ids.add(xarg.operator.task_id)
            return XComArg(op)

        maker.function = func
        return maker

    return wrap(fn) if callable(fn) else wrap


# @task.branch is an attribute of the @task decorator in the TaskFlow API.
task.branch = _task_branch


def dag(dag_id=None, **dag_kwargs):
    """TaskFlow @dag: decorate a function that defines a DAG's tasks. Calling the
    decorated function builds a DAG (id defaults to the function name) and collects
    the tasks its body defines within the DAG context — exactly like a
    ``with DAG(...)`` block. Used bare (``@dag``) the first argument is the
    decorated function; with arguments (``@dag(schedule=…)``) it is the dag_id."""
    func = dag_id if callable(dag_id) else None
    explicit_id = None if callable(dag_id) else dag_id

    def wrap(defining_fn):
        @functools.wraps(defining_fn)
        def factory(*args, **kwargs):
            built = DAG(explicit_id or defining_fn.__name__, **dag_kwargs)
            with built:
                defining_fn(*args, **kwargs)
            return built

        return factory

    return wrap(func) if func is not None else wrap
