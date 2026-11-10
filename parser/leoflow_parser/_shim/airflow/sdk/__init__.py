"""Shim of `airflow.sdk`: DAG, the @task TaskFlow decorator, and base operators."""
from __future__ import annotations

import functools

from airflow._core import (
    DAG,
    MAPPING_REFUSAL,
    BaseOperator,
    Label,
    Param,
    Variable,
    XComArg,
    chain,
    cross_downstream,
)
from airflow._names import (
    BaseHook,
    BaseNotifier,
    BaseSensorOperator,
    Connection,
    Context,
    Metadata,
    ObjectStoragePath,
    ParamsDict,
    PokeReturnValue,
    TriggerRule,
    WeightRule,
    get_current_context,
    get_parsing_context,
    literal,
)

# EmptyOperator is not exported: Airflow's airflow.sdk has no such name, so a DAG
# importing it from here would compile and then fail to import in the task pod.
# It lives in airflow.providers.standard.operators.empty, as in Airflow.
__all__ = [
    "DAG",
    "BaseOperator",
    "Label",
    "Param",
    "Variable",
    "XComArg",
    "chain",
    "cross_downstream",
    "task",
    "dag",
    "PythonOperator",
    "BaseHook",
    "BaseNotifier",
    "BaseSensorOperator",
    "Connection",
    "Context",
    "Metadata",
    "ObjectStoragePath",
    "ParamsDict",
    "PokeReturnValue",
    "TriggerRule",
    "WeightRule",
    "get_current_context",
    "get_parsing_context",
    "literal",
]


class PythonOperator(BaseOperator):
    """Classic PythonOperator (name carries 'Python' -> Dexaflow 'python')."""


class _TaskBranchOperator(PythonOperator):
    """The @task.branch shape. Branching needs scheduler skip-state (ADR 0040
    Phase D) that Dexaflow does not have yet; the 'Branch' in the class name routes
    it to the compiler's clean #225 reject instead of an opaque AttributeError at
    import time."""


class _TaskBashOperator(PythonOperator):
    """The @task.bash shape (#2337): the decorated function runs in the task pod
    and returns the command, which the runtime then runs with bash. It compiles
    to a python task; the marker tells the compiler which one it is."""

    __leoflow_task_bash__ = True


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


# Operator kwargs .override() cannot honor. The runtime reads a @task's
# on_failure_callback off the module-level decorator, which never sees an
# override, so accepting one here would run the wrong callback or none (#1681).
_OVERRIDE_REJECTED = ("on_failure_callback",)


def _task_factory(func, op_class, dec_kwargs, *, mappable):
    """Build the callable a TaskFlow decorator returns for ``func``.

    Calling it builds an ``op_class`` operator and returns an XComArg, wiring
    upstream edges from any XComArg arguments (including those nested in
    lists/dicts, e.g. a fan-in). ``dec_kwargs`` are the operator kwargs given to
    the decorator; a ``task_id`` among them replaces the function name, which
    stays the task's entrypoint either way (the compiler reads it off
    ``python_callable``)."""

    @functools.wraps(func)
    def maker(*args, **kwargs):
        # @task(trigger_rule=…, …) decorator kwargs apply to the operator.
        op_kwargs = dict(dec_kwargs)
        task_id = op_kwargs.pop("task_id", None) or func.__name__
        op = op_class(task_id=task_id, **op_kwargs)
        op.python_callable = func
        op.op_args = args
        op.op_kwargs = kwargs
        op.function = func  # parity with the SDK's @task (.function unwrap)
        for value in list(args) + list(kwargs.values()):
            for xarg in _iter_xcomargs(value):
                op.upstream_task_ids.add(xarg.operator.task_id)
        return XComArg(op)

    def override(**kwargs):
        """Airflow's ``_TaskDecorator.override``: a new task factory whose operator
        kwargs are the decorator's with ``kwargs`` merged over them. The original
        is left unchanged, and the result can be overridden again."""
        for name in _OVERRIDE_REJECTED:
            if name in kwargs:
                raise NotImplementedError(
                    f"{name} in .override(...) on task {func.__name__!r} (set it on "
                    "the @task(...) decorator instead)")
        return _task_factory(func, op_class, {**dec_kwargs, **kwargs}, mappable=mappable)

    maker.function = func
    maker.kwargs = dict(dec_kwargs)
    maker.override = override
    if mappable:
        def _no_dynamic_mapping(*_a, **_k):
            raise NotImplementedError(MAPPING_REFUSAL)

        maker.expand = _no_dynamic_mapping
        maker.expand_kwargs = _no_dynamic_mapping
        maker.partial = _no_dynamic_mapping
    return maker


def task(fn=None, **dec_kwargs):
    """TaskFlow @task: calling the wrapped function builds a python operator and
    returns an XComArg. ``.override(**kwargs)`` returns a copy with different
    operator kwargs (``task_id`` included), as in Airflow."""

    def wrap(func):
        return _task_factory(func, PythonOperator, dec_kwargs, mappable=True)

    return wrap(fn) if callable(fn) else wrap


def _task_branch(fn=None, **dec_kwargs):
    """@task.branch: TaskFlow branching. Captured as a Branch-named operator so
    the compiler refuses it with the clear ADR 0040 Phase D message, rather than
    the wrapped ``task`` raising an opaque AttributeError for the missing
    attribute at import."""

    def wrap(func):
        return _task_factory(func, _TaskBranchOperator, dec_kwargs, mappable=False)

    return wrap(fn) if callable(fn) else wrap


def _task_bash(fn=None, **dec_kwargs):
    """@task.bash: the function returns the bash command to run. The parser
    never calls it; the runtime does, in the pod, and runs the result."""

    def wrap(func):
        return _task_factory(func, _TaskBashOperator, dec_kwargs, mappable=False)

    return wrap(fn) if callable(fn) else wrap


_DEPENDENCIES_ADVICE = (
    "declare the packages in dexaflow.yaml dependencies: and use @task, which runs "
    "in the DAG's image")
_IMAGE_TASK_ADVICE = (
    "run the work as a task of the DAG's own image (a @task, or a BashOperator "
    "calling the tool), with its dependencies declared in dexaflow.yaml")

# TaskFlow decorators Dexaflow cannot run (#2450, #2451), each with why and
# what to do instead. Using one raises at decoration, which the compiler reports
# with the message as given.
_REFUSED_DECORATORS = {
    "docker": ("needs a Docker daemon, which a task pod does not have",
               _IMAGE_TASK_ADVICE),
    "kubernetes": ("starts a pod of its own, outside the admission, pools, secret "
                   "scoping and log capture Dexaflow applies to every task",
                   _IMAGE_TASK_ADVICE),
    "virtualenv": ("builds a virtualenv per task", _DEPENDENCIES_ADVICE),
    "external_python": ("runs another interpreter", _DEPENDENCIES_ADVICE),
    "pyspark": ("needs a Spark session from a Spark connection",
                "create the session inside a @task"),
    "short_circuit": ("skips downstream tasks, which needs skip support Dexaflow "
                      "does not have yet (#787)",
                      "fail fast in a @task, or split the DAG"),
    "sensor": ("needs the sensor poke loop around a function (#2339)",
               "use a provider sensor in reschedule mode, or a @task that polls "
               "with a bounded loop"),
    "run_if": ("skips the task on a condition, which needs skip support Dexaflow "
               "does not have yet (#2347)",
               "check the condition at the start of the @task and return early"),
    "skip_if": ("skips the task on a condition, which needs skip support Dexaflow "
                "does not have yet (#2347)",
                "check the condition at the start of the @task and return early"),
}


def _refused_decorator(name):
    why, advice = _REFUSED_DECORATORS[name]

    def refuse(*_args, **_kwargs):
        raise NotImplementedError(
            f"@task.{name} {why}, so it is not supported by Dexaflow: {advice}")

    return refuse


# @task.branch and @task.bash are attributes of the @task decorator in the
# TaskFlow API; so are the decorators refused above.
task.branch = _task_branch
task.bash = _task_bash
for _name in _REFUSED_DECORATORS:
    setattr(task, _name, _refused_decorator(_name))


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
