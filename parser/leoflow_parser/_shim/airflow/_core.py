"""Structural shim of Apache Airflow for *parsing only* (ADR 0024).

It records DAG/operator structure when a ``dag.py`` is exec'd, without importing
real Airflow. Pure standard library, zero third-party dependencies. It reproduces
exactly the attribute surface ``leoflow_parser.compiler`` reads, and TaskFlow
``@task`` calls only build structure (task bodies never run).

Unsupported operators are simply absent from this package, so importing one
raises ModuleNotFoundError — which the loader turns into a clear "not supported"
error (the behavior ADR 0024 specifies).
"""
from __future__ import annotations

_CURRENT: list = []  # stack of DAGs currently being defined
COLLECTED: dict = {}  # dag_id -> DAG, populated as each DAG context is entered


def reset() -> None:
    """Clear collected state between DAG files (the loader calls this)."""
    _CURRENT.clear()
    COLLECTED.clear()


class XComArg:
    """Duck-typed stand-in for Airflow's XComArg: carries the producing operator
    and proxies dependency operators (``>>`` / ``<<``) to it, so TaskFlow chains
    like ``a() >> b()`` and ``x >> [y(), z()]`` wire edges correctly."""

    def __init__(self, operator):
        self.operator = operator

    def __rshift__(self, other):
        return self.operator.__rshift__(other)

    def __lshift__(self, other):
        return self.operator.__lshift__(other)


def _as_operator(node):
    """Unwrap an XComArg to its operator; pass operators through unchanged."""
    return node.operator if isinstance(node, XComArg) else node


# JSON-Schema validation keywords a Param's kwargs may carry. Airflow's real Param
# collects every non-default/description kwarg into the param's JSON Schema; we
# pass through the recognised keyword set so the compiled schema is a clean,
# validator-ready JSON-Schema object (unknown kwargs are ignored, not emitted).
# Sentinel marking a Param declared with no default (a required parameter),
# distinct from an explicit default of None.
_UNSET = object()


class Param:
    """Structural stand-in for Airflow's ``airflow.sdk.Param``.

    Records the declared default and its JSON Schema. Airflow treats a Param's
    kwargs as the schema, so they are passed through verbatim (with an explicit
    ``schema=`` dict, if given, as the base) rather than filtered against an
    allow-list — that way composite keywords (``anyOf``/``allOf``/``oneOf``/
    ``not``), every string/number facet, ``title``, ``examples``, etc. all reach
    trigger-time validation. The compiler reads ``.default`` and ``.schema`` to
    emit the DAG's ``params`` block. Task bodies never run, so nothing is
    validated here — the control plane validates the run conf against this schema
    at trigger time."""

    def __init__(self, default=_UNSET, description=None, **kwargs):
        # Distinguish "no default given" (a required param) from an explicit
        # None/null default. Airflow uses the same sentinel technique; the
        # compiler omits the `default` key entirely for a required param.
        self.has_default = default is not _UNSET
        self.default = None if default is _UNSET else default
        self.description = description
        schema = dict(kwargs.pop("schema", None) or {})
        schema.update(kwargs)
        self.schema = schema


class BaseOperator:
    """Minimal operator base: registers into the active DAG and tracks edges."""

    def __init__(self, task_id, **kwargs):
        self.upstream_task_ids: set[str] = set()
        self.downstream_task_ids: set[str] = set()
        self.trigger_rule = kwargs.get("trigger_rule", "all_success")
        for key, value in kwargs.items():
            setattr(self, key, value)
        # Attach to a DAG: an explicit dag= kwarg wins (operators built outside a
        # `with DAG()` block), otherwise the active context DAG. Mirrors Airflow.
        target = kwargs.get("dag") or (_CURRENT[-1] if _CURRENT else None)
        if target is not None:
            # Airflow auto-suffixes a duplicate task_id within a DAG (__1, __2, …).
            self.task_id = target.unique_task_id(task_id)
            target.add_task(self)
        else:
            self.task_id = task_id

    def _link(self, others, downstream: bool):
        targets = others if isinstance(others, (list, tuple)) else [others]
        for other in targets:
            other_op = _as_operator(other)
            ups, downs = (self, other_op) if downstream else (other_op, self)
            downs.upstream_task_ids.add(ups.task_id)
            ups.downstream_task_ids.add(downs.task_id)
        return others

    def __rshift__(self, other):
        return self._link(other, downstream=True)

    def __lshift__(self, other):
        return self._link(other, downstream=False)


class DAG:
    """Context-manager DAG that collects the operators defined within it."""

    def __init__(self, dag_id, schedule=None, tags=None, **kwargs):
        self.dag_id = dag_id
        self.schedule = schedule
        self.tags = list(tags or [])
        self.task_dict: dict = {}
        # Airflow applies default_args to every operator in the DAG; the compiler
        # reads it as the fallback for a task's retries/retry_delay/execution_timeout
        # (#434). Kept as a plain dict; empty when not given.
        self.default_args: dict = dict(kwargs.get("default_args") or {})
        # Author-declared DAG-run params (Airflow's params=): each value is a bare
        # default or a Param carrying a JSON Schema. The compiler emits them so the
        # control plane can default + validate a run's conf at trigger time. None
        # when the DAG declares none, keeping the compiled shape unchanged.
        self.params = kwargs.get("params")
        # Scheduling/metadata attributes the domain + scheduler already honor
        # (max_active_runs concurrency, catchup/start_date backfill, description
        # for the UI): captured so they are not silently dropped. Absent leaves
        # them off the compiled spec, keeping a DAG that sets none unchanged.
        self.description = kwargs.get("description")
        self.start_date = kwargs.get("start_date")
        self.max_active_runs = kwargs.get("max_active_runs")
        self.catchup = kwargs.get("catchup")
        # Airflow's is_paused_upon_creation: whether the DAG starts paused when
        # it is first registered, overriding the server's
        # dags_are_paused_at_creation. None (unset) leaves it to the server.
        self.is_paused_upon_creation = kwargs.get("is_paused_upon_creation")
        # Collect on construction too, so DAGs defined without `with` (e.g.
        # module-level `dag = DAG(...)` with operators attached via dag=) are seen.
        COLLECTED[dag_id] = self

    def add_task(self, op):
        self.task_dict[op.task_id] = op

    def unique_task_id(self, task_id: str) -> str:
        if task_id not in self.task_dict:
            return task_id
        i = 1
        while f"{task_id}__{i}" in self.task_dict:
            i += 1
        return f"{task_id}__{i}"

    def __enter__(self):
        _CURRENT.append(self)
        COLLECTED[self.dag_id] = self
        return self

    def __exit__(self, *exc):
        _CURRENT.pop()
        return False
