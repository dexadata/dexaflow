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


# SETUP_TEARDOWN_REFUSAL is the message for Airflow's setup and teardown tasks
# (#2336). Without it the shim raised an opaque AttributeError on as_setup().
SETUP_TEARDOWN_REFUSAL = (
    "setup and teardown tasks (as_setup(), as_teardown(), @setup, @teardown) are "
    "not supported by Dexaflow: express the cleanup as a downstream task with "
    "trigger_rule='all_done'"
)


# MAPPING_REFUSAL is the message for dynamic task mapping in any of its forms
# (.expand, .partial, .expand_kwargs, .map, .zip). The compiler adds its
# not-supported suffix.
MAPPING_REFUSAL = "dynamic task mapping (.expand/.partial/.expand_kwargs/.map/.zip)"

# XCOM_INDEX_REFUSAL is the message for result["key"] on a task's output, the
# multiple_outputs pattern (#2451).
XCOM_INDEX_REFUSAL = (
    "indexing a task's output (result['key'], as with multiple_outputs=True) is not "
    "supported by Dexaflow yet: pass the whole result to the downstream task and read "
    "the key there, or return each value from its own task"
)


def _refuse_setup_teardown(*_args, **_kwargs):
    raise NotImplementedError(SETUP_TEARDOWN_REFUSAL)


# VARIABLE_AT_PARSE is the message for a Variable read while the DAG file is
# parsed (#2337). The parser has no Variable store, and a value read there would
# be frozen into dag.json instead of read on each run.
VARIABLE_AT_PARSE = (
    "Variable.{method}() called while the DAG file is parsed is not supported by "
    "Dexaflow: the compiler has no Variable store. Call Variable.get inside the "
    "task body, or use {{{{ var.value.<key> }}}} in a templated field"
)


def _variable_refusal(method):
    def refuse(*_args, **_kwargs):
        raise NotImplementedError(VARIABLE_AT_PARSE.format(method=method))

    return staticmethod(refuse)


class Variable:
    """Stand-in for Airflow's ``Variable`` so ``from airflow.sdk import Variable``
    at the top of a DAG file compiles (#2337). Task bodies never run here, so a
    call inside a task is never reached; a call at module level is refused."""

    get = _variable_refusal("get")
    set = _variable_refusal("set")
    update = _variable_refusal("update")
    delete = _variable_refusal("delete")


class _DependencyMixin:
    """Edge operators shared by operators and XComArgs: ``>>`` and ``<<`` with a
    task, an XComArg, a Label or a list of them on either side, and Airflow's
    ``set_downstream`` and ``set_upstream``."""

    def __rshift__(self, other):
        return _as_operator(self)._link(other, downstream=True)

    def __lshift__(self, other):
        return _as_operator(self)._link(other, downstream=False)

    # `[a, b] >> c` calls c.__rrshift__([a, b]): the list is upstream of c.
    def __rrshift__(self, other):
        _as_operator(self)._link(other, downstream=False)
        return self

    def __rlshift__(self, other):
        _as_operator(self)._link(other, downstream=True)
        return self

    def set_downstream(self, other):
        _as_operator(self)._link(other, downstream=True)

    def set_upstream(self, other):
        _as_operator(self)._link(other, downstream=False)


class XComArg(_DependencyMixin):
    """Duck-typed stand-in for Airflow's XComArg: carries the producing operator
    and proxies dependency operators (``>>`` / ``<<``) to it, so TaskFlow chains
    like ``a() >> b()`` and ``x >> [y(), z()]`` wire edges correctly."""

    as_setup = _refuse_setup_teardown
    as_teardown = _refuse_setup_teardown

    def __init__(self, operator):
        self.operator = operator

    def __getitem__(self, key):
        raise NotImplementedError(XCOM_INDEX_REFUSAL)

    def map(self, *_args, **_kwargs):
        raise NotImplementedError(MAPPING_REFUSAL)

    def zip(self, *_args, **_kwargs):
        raise NotImplementedError(MAPPING_REFUSAL)

    def resolve(self, *_args, **_kwargs):
        raise NotImplementedError(
            "XComArg.resolve() while the DAG file is parsed: the value only exists "
            "when the task runs")


def _as_operator(node):
    """Unwrap an XComArg to its operator; pass operators through unchanged."""
    return node.operator if isinstance(node, XComArg) else node


def _as_list(nodes):
    return list(nodes) if isinstance(nodes, (list, tuple, set)) else [nodes]


class Label:
    """Airflow's edge label (``a >> Label("x") >> b``), accepted and dropped
    (#2337): the edge it sits on is wired, the label text is not kept. It
    records the tasks on each side and links every upstream to every downstream,
    whichever side is attached first."""

    def __init__(self, label):
        self.label = label
        self._upstream: list = []
        self._downstream: list = []

    def _add_upstream(self, node):
        op = _as_operator(node)
        self._upstream.append(op)
        for down in self._downstream:
            op._link(down, downstream=True)

    def _add_downstream(self, node):
        op = _as_operator(node)
        self._downstream.append(op)
        for up in self._upstream:
            up._link(op, downstream=True)

    def set_downstream(self, other):
        for node in _as_list(other):
            self._add_downstream(node)

    def set_upstream(self, other):
        for node in _as_list(other):
            self._add_upstream(node)

    def __rshift__(self, other):
        self.set_downstream(other)
        return other

    def __lshift__(self, other):
        self.set_upstream(other)
        return other

    def __rrshift__(self, other):
        self.set_upstream(other)
        return self

    def __rlshift__(self, other):
        self.set_downstream(other)
        return self


def chain(*tasks):
    """Airflow's ``chain``: link each element to the next. A task (or Label)
    next to a list links to every task in it; two adjacent lists must have the
    same length and are linked pairwise, as in Airflow."""
    for up, down in zip(tasks, tasks[1:], strict=False):
        if not isinstance(up, (list, tuple)):
            up.set_downstream(down)
            continue
        if not isinstance(down, (list, tuple)):
            down.set_upstream(up)
            continue
        if len(up) != len(down):
            raise ValueError(
                f"chain() got adjacent lists of different lengths ({len(up)} and "
                f"{len(down)}); Airflow links two lists pairwise, so they must match")
        for up_node, down_node in zip(up, down, strict=True):
            up_node.set_downstream(down_node)


def cross_downstream(from_tasks, to_tasks):
    """Airflow's ``cross_downstream``: every task in ``from_tasks`` becomes
    upstream of every task in ``to_tasks``."""
    for node in from_tasks:
        node.set_downstream(to_tasks)


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


class BaseOperator(_DependencyMixin):
    """Minimal operator base: registers into the active DAG and tracks edges."""

    as_setup = _refuse_setup_teardown
    as_teardown = _refuse_setup_teardown

    def __init__(self, task_id, **kwargs):
        self.upstream_task_ids: set[str] = set()
        self.downstream_task_ids: set[str] = set()
        # The kwargs the task passed itself, so the compiler can tell them from
        # the DAG's default_args (Airflow fills only what the task leaves out).
        self.__leoflow_own__ = frozenset(kwargs)
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

    @property
    def output(self):
        """The task's return value as an XComArg, as on Airflow
        (``process(extract_op.output)``)."""
        return XComArg(self)

    @classmethod
    def partial(cls, *_args, **_kwargs):
        raise NotImplementedError(MAPPING_REFUSAL)

    def _link(self, others, downstream: bool):
        for other in _as_list(others):
            if isinstance(other, Label):
                if downstream:
                    other._add_upstream(self)
                else:
                    other._add_downstream(self)
                continue
            other_op = _as_operator(other)
            ups, downs = (self, other_op) if downstream else (other_op, self)
            downs.upstream_task_ids.add(ups.task_id)
            ups.downstream_task_ids.add(downs.task_id)
        return others


# DAG arguments the shim records only so the compiler can refuse a value other
# than Airflow's default (#2449), with that default.
_REFUSED_DAG_KWARGS = {
    "end_date": None,
    "on_failure_callback": None,
    "on_success_callback": None,
    "sla_miss_callback": None,
    "fail_fast": False,
    "fail_stop": False,
    "render_template_as_native_obj": False,
    "user_defined_macros": None,
    "user_defined_filters": None,
    "template_searchpath": None,
    "template_undefined": None,
    "jinja_environment_kwargs": None,
    "deadline": None,
    "access_control": None,
    "allowed_run_types": None,
}


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
        # max_active_tasks caps the task instances active at once in each run
        # of the DAG, as in Airflow 3 (#2427). The removed Airflow 2 alias
        # concurrency is kept only so the compiler can refuse it by name.
        self.max_active_tasks = kwargs.get("max_active_tasks")
        if "concurrency" in kwargs:
            self.concurrency = kwargs["concurrency"]
        self.catchup = kwargs.get("catchup")
        # Airflow's is_paused_upon_creation: whether the DAG starts paused when
        # it is first registered, overriding the server's
        # dags_are_paused_at_creation. None (unset) leaves it to the server.
        self.is_paused_upon_creation = kwargs.get("is_paused_upon_creation")
        # Arguments the scheduler does not honor yet: kept only so the
        # compiler can refuse a non-default value instead of dropping it
        # (#2336). Airflow's defaults when unset.
        self.dagrun_timeout = kwargs.get("dagrun_timeout")
        self.max_consecutive_failed_dag_runs = kwargs.get("max_consecutive_failed_dag_runs", 0)
        # More arguments that were dropped without a word (#2449), kept for the
        # same refusal. Airflow's defaults when unset.
        for name, default in _REFUSED_DAG_KWARGS.items():
            setattr(self, name, kwargs.get(name, default))
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
