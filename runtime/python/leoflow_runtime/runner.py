"""Run a user task callable and capture its return value."""

from __future__ import annotations

import importlib
import inspect
import json
import os
import shlex
import sys
from datetime import datetime

from leoflow_runtime.xcom import XCOM_ENV_PREFIX

# Lifecycle messages are prefixed so the user can distinguish runtime-emitted
# lines from their own print(). They go to stdout (not stderr) on purpose:
# stderr maps to log level=error in the UI, which would visually scream on
# every successful run.
_LIFECYCLE_PREFIX = "[leoflow]"


def _lifecycle(msg: str) -> None:
    """Emit a one-line lifecycle event the user sees in the UI log panel."""
    print(f"{_LIFECYCLE_PREFIX} {msg}", flush=True)


# NOTE: an earlier iteration wrapped the runtime lifecycle in
# ``::group::Pre task execution`` / ``::group::Post task execution`` markers
# the Airflow 3.2 SPA's log viewer recognizes as drill-down sections. In
# practice the SPA renders our markers as <details open={hash-only}> elements
# that default to COLLAPSED (no hash → open=false) and the controlled-component
# toggle does not respond to clicks, so users could not expand the groups to
# see their print() / lifecycle output. The net effect was the OPPOSITE of the
# intent: lifecycle info was hidden by default with no reliable way to reveal
# it. Until the SPA-side toggle bug is addressed, we emit lifecycle lines flat
# — Airflow's standard "no groups → everything visible" layout. The Pre/Post
# drill-down can be reintroduced once the SPA renders them correctly.

DEFAULT_RETURN_VALUE_PATH = "/tmp/leoflow_return_value.json"  # noqa: S108


def _load_call_args() -> dict:
    """Decode LEOFLOW_CALL_ARGS_JSON, the compile-time TaskFlow literals (#115).

    The parser captures literal call args of a ``@task`` invocation
    (``shard(n=0)`` → ``{"n": 0}``) and the agent stamps the result as the
    LEOFLOW_CALL_ARGS_JSON env var. Malformed JSON is silently dropped: the
    parser's contract is to emit valid JSON, and dying with a JSON error the
    user did not write would be worse than running with the function's
    defaults. The env name is call_args (not params) to leave Airflow's
    DAG-run params term free for a future feature (#148).
    """
    raw = os.environ.get("LEOFLOW_CALL_ARGS_JSON", "")
    if not raw:
        return {}
    try:
        decoded = json.loads(raw)
    except (TypeError, ValueError):
        return {}
    return decoded if isinstance(decoded, dict) else {}


def _resolve_kwargs(fn, context: dict) -> dict:
    """Resolve each of fn's parameters from compile-time literals, upstream XCom, and
    the run context — so a TaskFlow @task gets the same context a captured operator does.

    Injection paths, by precedence (highest first):

    - **LEOFLOW_XCOM_<PARAM>**: an upstream task's ``return_value`` (the agent fetches it).
    - **LEOFLOW_CALL_ARGS_JSON** (#115): the literal args the user wrote at the @task call
      site (``shard(n=0)``), captured by the parser at compile time.
    - **run context** (ds, ts, data_interval_start/end, params, run_id, ti, var, conn):
      injected for a param whose name is a context key, and for a ``**kwargs`` catch-all —
      mirroring Airflow's TaskFlow context injection. Lowest precedence, so an explicit
      call_arg/XCom binding is never overridden and existing tasks are unchanged.

    Parameters with no binding are left unset so the function's defaults apply (or it
    raises TypeError if it has none — exactly Airflow's semantics).
    """
    call_args = _load_call_args()
    kwargs: dict = {}
    has_var_kw = False
    for name, param in inspect.signature(fn).parameters.items():
        if param.kind is inspect.Parameter.VAR_POSITIONAL:
            continue
        if param.kind is inspect.Parameter.VAR_KEYWORD:
            has_var_kw = True
            continue
        if name in call_args:
            kwargs[name] = call_args[name]
        # Read the raw env var here (rather than via xcom_pull) so we can log
        # the wire size when the pull succeeds — invaluable when debugging a
        # task that "received None" because the upstream payload didn't arrive.
        # XCom takes precedence over the literal call arg of the same name.
        raw = os.environ.get(XCOM_ENV_PREFIX + name.upper())
        if raw is not None:
            kwargs[name] = json.loads(raw)
            _lifecycle(f"pulled {name} ({len(raw)} B)")
        elif name not in kwargs and name in context:
            # Airflow injects context values by matching param name (def f(ds=None)).
            kwargs[name] = context[name]
    if has_var_kw:
        # def f(**context): Airflow passes the whole context. Do not clobber explicit
        # call_arg/XCom bindings already in kwargs.
        for key, value in context.items():
            kwargs.setdefault(key, value)
    return kwargs


def return_value_path() -> str:
    """Return the path the task's return value is written to.

    Overridable via ``LEOFLOW_RETURN_VALUE_PATH`` (primarily for tests).
    """
    return os.environ.get("LEOFLOW_RETURN_VALUE_PATH", DEFAULT_RETURN_VALUE_PATH)


def run(entrypoint: str) -> None:
    """Import and call ``module:callable``, writing a non-None return as JSON.

    The agent reads the file and pushes it as the task's ``return_value`` XCom.
    A None return writes nothing, so downstream tasks see no XCom.

    Emits three lifecycle lines so the UI's log panel is informative even when
    the user function is silent: ``loading <entrypoint>``, ``resolved kwargs:
    {...}`` (when any), and ``returned <repr>`` (success) or no extra line on
    raise (the agent wraps the traceback). All three use ``[leoflow]`` prefix
    via :func:`_lifecycle` so they are distinguishable from user ``print()``.
    """
    module_name, sep, fn_name = entrypoint.partition(":")
    if not sep or not module_name or not fn_name:
        raise ValueError(f"entrypoint must be 'module:callable', got {entrypoint!r}")

    _lifecycle(f"loading {entrypoint}")
    module = importlib.import_module(module_name)
    fn = getattr(module, fn_name)
    # Read the task's on_failure_callback off the loaded @task decorator (it keeps
    # it in .kwargs) BEFORE unwrapping to the raw function — resolving from the
    # object we already hold works even for an unbound `with DAG():` (#424 inc 4b).
    on_failure_callback = _on_failure_from_task_object(fn)
    # Airflow TaskFlow @task decorators are not executed when called directly —
    # calling them returns an XComArg (a task reference), not the function's
    # result. Unwrap to the underlying Python function so we run the user's code
    # and capture its real return value.
    if hasattr(fn, "function"):
        fn = fn.function
    context = _operator_context()
    kwargs = _resolve_kwargs(fn, context)
    if kwargs:
        # Log keys only — the values can carry XCom-pulled secrets or any
        # user payload; per ADR 0032 they belong in the XCom tab, not in the
        # log file. The pulled lines above already report each XCom source
        # and its wire size so the operator can correlate.
        _lifecycle(f"resolved kwargs: {sorted(kwargs.keys())}")

    try:
        result = fn(**kwargs)
    except Exception as exc:  # noqa: BLE001 — surface user errors to the log
        _lifecycle(f"user function {fn_name} raised {type(exc).__name__}: {exc}")
        # Stdout is line-buffered or `-u` unbuffered; flush stderr too so the
        # ordering in the log panel matches the wall-clock order.
        sys.stdout.flush()
        sys.stderr.flush()
        # Run the @task's on_failure_callback on its terminal attempt, before the
        # re-raise, so the task's own failed outcome is unchanged (#424 inc 4b).
        _maybe_fire_on_failure_callback(context, on_failure_callback)
        # Re-raise so Python's default handler emits the traceback to stderr —
        # the agent captures it.
        raise

    _write_return(result)
    # A @task can push custom-keyed XComs via context["ti"].xcom_push — ship them the
    # same way operators do (multi-key XCom parity for native tasks, ADR 0040).
    _write_xcom_pushes(context["ti"].pushed)


def _write_return(result) -> None:
    """Write a non-None return as JSON to the return-value file (the agent pushes
    it as the task's return_value XCom). A None return writes nothing."""
    if result is None:
        _lifecycle("returned None (no XCom pushed)")
        return
    try:
        payload = json.dumps(result)
    except (TypeError, ValueError):
        # Airflow operators can return non-JSON objects; stringify so the XCom is
        # at least usable rather than failing the task on serialization.
        payload = json.dumps(repr(result))
    _lifecycle(f"returned {type(result).__name__} ({len(payload)} B XCom)")
    with open(return_value_path(), "w", encoding="utf-8") as fh:
        fh.write(payload)


def _render_bash(command: str, context: dict) -> str:
    """Jinja-render a bash command with the run context (ds/params/var/conn/...), so a
    native bash task can template like Airflow (ADR 0040 parity). No template markers,
    no jinja2, or a render error → the raw command (best-effort; the env vars
    $LEOFLOW_DS/$AIRFLOW_VAR_* still reach it either way)."""
    if "{{" not in command and "{%" not in command:
        return command
    try:
        from jinja2.sandbox import SandboxedEnvironment
    except Exception:  # noqa: BLE001 — no jinja in this env; env vars still work
        return command
    try:
        # A SandboxedEnvironment (what Airflow uses for templating) refuses access to
        # Python internals, so a template-injection payload (e.g. {{ ''.__class__ }})
        # fails closed instead of executing. autoescape stays off on purpose: this
        # renders a shell command, not HTML — HTML-escaping would corrupt it. Secrets
        # should be referenced as $AIRFLOW_VAR_*/$AIRFLOW_CONN_* env vars rather than
        # rendered into the command string (see the env-secrets delivery path).
        #
        # finalize shell-quotes every interpolated VALUE (issue #489). The template
        # text is the DAG author's (write:dag) trusted structure; the values it
        # interpolates — `params` is the run's `conf`, which anyone with execute:dag
        # supplies — are untrusted data. Quoting each rendered value with shlex.quote
        # keeps a metacharacter payload ("x; rm -rf /", "$(...)") a single inert
        # argument instead of shell syntax. Safe values (dates, plain words) contain
        # no metacharacters, so shlex.quote returns them unchanged — the common case
        # is byte-identical. This is the boundary the sandbox does NOT cover: it
        # governs what the template may evaluate, not what the result means to bash.
        env = SandboxedEnvironment(finalize=lambda value: shlex.quote(str(value)))
        return env.from_string(command).render(**context)
    except Exception:  # noqa: BLE001 — a broken/blocked template must not fail the task
        return command


def run_bash(command: str) -> None:
    """Render a bash command with the run context, then exec ``bash -c`` in place so
    bash's stdout/stderr/exit code flow to the agent unchanged (ADR 0040). The agent
    only routes a command here when it contains ``{{`` — plain bash stays a direct
    ``bash -c`` (no Python needed in bash-only images)."""
    rendered = _render_bash(command, _operator_context())
    if rendered != command:
        _lifecycle("rendered bash command from the run context")
    # Intentionally exec bash by PATH (the agent ran the task as `bash -c` before this
    # render hop); replacing the process keeps bash's stdout/stderr/exit code intact.
    os.execvp("bash", ["bash", "-c", rendered])  # noqa: S606,S607 — deliberate bash exec


# Env var carrying the upstream task_id -> return_value map for ti.xcom_pull. It must
# NOT start with XCOM_ENV_PREFIX ("LEOFLOW_XCOM_"): _merge_operator_xcom consumes every
# such var as a param-bound operator kwarg, and would otherwise inject this whole map
# as a bogus `by_task=` kwarg (a real collision the live GCP chain test caught).
UPSTREAM_XCOM_ENV = "LEOFLOW_UPSTREAM_XCOM"

# A reschedule-mode sensor that pokes not-ready writes its requested next-poke time
# to RESCHEDULE_PATH_ENV and exits with RESCHEDULE_EXIT_CODE; the agent reads both and
# reports up_for_reschedule instead of failure (ADR 0040 Phase B, #380). 75 is
# sysexits' EX_TEMPFAIL ("temporary failure, reattempt later") — semantically exact,
# and distinct from the 0/1 success/failure the agent already maps.
RESCHEDULE_PATH_ENV = "LEOFLOW_RESCHEDULE_PATH"
RESCHEDULE_EXIT_CODE = 75


def _load_xcom_by_task() -> dict:
    """Decode UPSTREAM_XCOM_ENV — the map of upstream ``task_id`` to its decoded
    ``return_value`` the agent delivers so ``ti.xcom_pull`` can resolve it (chained
    operators). Malformed/absent → empty: a missing upstream pulls as None, never a
    crash."""
    raw = os.environ.get(UPSTREAM_XCOM_ENV)
    if not raw:
        return {}
    try:
        decoded = json.loads(raw)
    except (TypeError, ValueError):
        return {}
    return decoded if isinstance(decoded, dict) else {}


class _StandaloneTaskInstance:
    """A minimal, tolerant TaskInstance stand-in for a standalone operator run (ADR
    0040 Phase A).

    Almost every provider operator calls ``context["ti"].xcom_push(...)`` at the end
    of ``execute()`` to persist its UI operator link (see Airflow's
    ``.../links/base.py``); a few read ``try_number``. Dexaflow propagates only the
    operator's ``execute()`` return value as the downstream XCom (see
    :func:`_write_return`), so ``xcom_push``/``xcom_pull`` are deliberate no-ops —
    they drop the UI link and any extra keyed XCom that Dexaflow does not consume,
    rather than crashing the task *after* its real side effect (which on retry would
    re-run a non-idempotent operator). Identity fields come from the agent's env.
    Attributes beyond these are intentionally absent: an operator that needs the real
    metastore then fails loudly rather than running silently wrong.
    """

    def __init__(self) -> None:
        self.task_id = os.environ.get("LEOFLOW_TASK_ID", "")
        self.dag_id = os.environ.get("LEOFLOW_DAG_ID", "")
        self.run_id = os.environ.get("LEOFLOW_RUN_ID", "")
        self.map_index = -1
        try:
            self.try_number = int(os.environ.get("LEOFLOW_TRY_NUMBER", "1"))
        except (TypeError, ValueError):
            self.try_number = 1
        self._xcom_by_task = _load_xcom_by_task()
        self.pushed: dict = {}

    def xcom_push(self, key=None, value=None, *args, **kwargs) -> None:
        """Capture the push (keyed by ``key``) so the operator's UI deep-link buttons
        (operator_extra_links) can be computed after execute() — see
        :func:`_extra_links` (ADR 0040, #375). Dexaflow still propagates only the
        execute() return value downstream (:func:`_write_return`), not these pushes."""
        if key is not None:
            self.pushed[key] = value

    def xcom_pull(self, task_ids=None, dag_id=None, key="return_value", *args, **kwargs):
        """Resolve an upstream task's ``return_value`` — the Airflow-idiomatic way
        chained operators pass data (``ti.xcom_pull('compile')['name']``). The agent
        delivers each declared upstream's return_value in UPSTREAM_XCOM_ENV; only
        the ``return_value`` key is carried (custom keys → None, matching Dexaflow's
        single-payload XCom model). ``task_ids`` may be a single id (→ one value) or a
        list (→ a list, like Airflow); an unknown id resolves to ``default``
        (Airflow's ``default`` kwarg, itself None unless the caller passes one).
        """
        default = kwargs.get("default")
        if key not in (None, "return_value"):
            return default
        if isinstance(task_ids, (list, tuple)):
            return [self._xcom_by_task.get(t, default) for t in task_ids]
        if task_ids is None:
            return default
        return self._xcom_by_task.get(task_ids, default)

    def get_first_reschedule_date(self, context=None):
        """Return when this task first entered reschedule, delivered by the agent as
        LEOFLOW_FIRST_RESCHEDULE_AT (#380), so a reschedule-mode sensor measures its
        run_duration from the first poke and honors its cumulative ``timeout`` across
        re-dispatches. None on the first attempt (not yet rescheduled) — the sensor's
        started_at then falls back to now(), as Airflow does for the first poke."""
        return _parse_dt(os.environ.get("LEOFLOW_FIRST_RESCHEDULE_AT"))


def _operator_context() -> dict:
    """A minimal Airflow execution context for a standalone operator run (ADR 0040
    Phase A). The agent supplies the run's macros via env; many operators read no
    context at all, so sensible defaults are enough for the common case. ``ti`` /
    ``task_instance`` are a tolerant :class:`_StandaloneTaskInstance` (not None) so
    the near-universal operator-link ``ti.xcom_push`` pattern does not crash."""
    params = {}
    raw_params = os.environ.get("LEOFLOW_PARAMS")
    if raw_params:
        try:
            decoded = json.loads(raw_params)
        except (TypeError, ValueError):
            decoded = {}
        # Coerce non-object conf (e.g. "null") to {} so context["params"].get(...) works.
        params = decoded if isinstance(decoded, dict) else {}
    ds = os.environ.get("LEOFLOW_DS", "")
    ti = _StandaloneTaskInstance()
    ctx = {
        "ds": ds,
        "ts": os.environ.get("LEOFLOW_TS", ""),
        "run_id": os.environ.get("LEOFLOW_RUN_ID", ""),
        "params": params,
        "task_instance": ti,
        "ti": ti,
        "dag_run": None,
        "data_interval_start": _parse_dt(os.environ.get("LEOFLOW_DATA_INTERVAL_START")),
        "data_interval_end": _parse_dt(os.environ.get("LEOFLOW_DATA_INTERVAL_END")),
    }
    var, conn = _secrets_accessors()
    if var is not None:
        ctx["var"] = var
        ctx["conn"] = conn
    return ctx


def _secrets_accessors():
    """Airflow's var/conn template accessors, so ``{{ var.value.X }}`` / ``{{ var.json.X }}``
    / ``{{ conn.X }}`` resolve from the AIRFLOW_VAR_*/AIRFLOW_CONN_* env the agent delivers
    (ADR 0040). Reuses Airflow's own accessor classes — they read through the secrets
    backends, which include the environment backend. Returns (None, None) when Airflow is
    absent (the runtime's Airflow-free tests), so the keys are simply not injected."""
    try:
        # The Task SDK module (Airflow 3); airflow.utils.context is deprecated.
        from airflow.sdk.execution_time.context import ConnectionAccessor, VariableAccessor
    except Exception:  # noqa: BLE001 — older Airflow or none in this env
        try:
            from airflow.utils.context import ConnectionAccessor, VariableAccessor
        except Exception:  # noqa: BLE001 — no Airflow in this env
            return None, None
    var = {
        "value": VariableAccessor(deserialize_json=False),
        "json": VariableAccessor(deserialize_json=True),
    }
    return var, ConnectionAccessor()


def _parse_dt(value):
    """Parse an RFC3339 timestamp (the agent's data-interval format) to a datetime, so
    operators can use data_interval_start/end as real datetimes. None when unset or
    unparseable. Tolerates a trailing 'Z' (Python < 3.11 fromisoformat does not)."""
    if not value:
        return None
    try:
        return datetime.fromisoformat(value.replace("Z", "+00:00"))
    except (TypeError, ValueError):
        return None


def _merge_operator_xcom(args: dict) -> dict:
    """Inject upstream XCom values into a captured operator's kwargs (ADR 0040
    A1.1). The parser records an operator arg bound to an upstream task's output
    as an xcom_input; the agent fetches that upstream's return_value and delivers
    it as ``LEOFLOW_XCOM_<PARAM>``. Here we decode each and set it on the matching
    kwarg, so ``MyOperator(sql=extract())`` runs with the real upstream SQL. An
    XCom value overrides any same-name literal, matching the @task precedence.
    """
    for key, raw in os.environ.items():
        if not key.startswith(XCOM_ENV_PREFIX):
            continue
        param = key[len(XCOM_ENV_PREFIX):].lower()
        args[param] = json.loads(raw)
        _lifecycle(f"pulled {param} for operator ({len(raw)} B)")
    return args


def _normalize_callbacks(on_failure_callback) -> list:
    """Return a task's on_failure_callback(s) as a list of callables. Airflow 3
    normalises a task's ``on_failure_callback`` to a list (e.g. ``[fn]``); a raw
    ``@task`` decorator keeps whatever was passed to ``@task`` (a callable or a
    list). Accept a bare callable, a list/tuple, or None, and drop anything
    non-callable — so a real Airflow list is called element-by-element, never as a
    (non-callable) list."""
    if on_failure_callback is None:
        return []
    items = (on_failure_callback if isinstance(on_failure_callback, (list, tuple))
             else [on_failure_callback])
    return [c for c in items if callable(c)]


def _on_failure_from_task_object(obj):
    """Pull ``on_failure_callback`` off the loaded task object. A TaskFlow ``@task``
    decorator keeps it in ``.kwargs``; an operator instance exposes it as an
    attribute (Airflow-normalised to a list). Returns the raw value (callable /
    list / None). Resolving from the object the runtime already holds — instead of
    scanning module globals for a bound DAG — is what makes an unbound
    ``with DAG():`` (no module-level DAG variable) work."""
    kwargs = getattr(obj, "kwargs", None)
    if isinstance(kwargs, dict) and "on_failure_callback" in kwargs:
        return kwargs["on_failure_callback"]
    return getattr(obj, "on_failure_callback", None)


def _run_on_failure_callback(on_failure_callback, context, try_number, max_tries) -> None:
    """Run a task's Airflow ``on_failure_callback`` on its FINAL failed attempt
    (#424 inc 4b).

    Airflow calls the callback only when the task instance reaches terminal
    ``failed`` (retries exhausted), never on an intermediate ``up_for_retry``
    attempt. The runtime runs per attempt, so gate on ``try_number >= max_tries``:
    for ``retries: 3``, a fail→fail→success run fires it zero times, and a
    fail×3 run fires it once (on the last).

    ``on_failure_callback`` is whatever the task carries — a callable, or (as
    Airflow normalises it) a list of them. Every callable runs, best-effort and
    anti-loop: one that raises is logged and swallowed so it can neither stop the
    others nor change the task's own failed outcome.
    """
    if try_number < max_tries:
        return
    callbacks = _normalize_callbacks(on_failure_callback)
    if not callbacks:
        return
    _lifecycle("running on_failure_callback")
    for callback in callbacks:
        try:
            callback(context)
        except Exception as exc:  # noqa: BLE001 — best-effort; never fail the task on its callback
            _lifecycle(f"on_failure_callback raised {type(exc).__name__}: {exc} (ignored)")


def _maybe_fire_on_failure_callback(context, on_failure_callback) -> None:
    """Fire the task's on_failure_callback if the compiler marked one
    (LEOFLOW_ON_FAILURE_CALLBACK, set by the agent) and this attempt is terminal.
    ``on_failure_callback`` is the raw value read off the loaded task object (a
    callable, a list, or None). Reads try_number/max_tries from the env; a missing
    max_tries defaults to try_number (treat as final). Fully guarded — never raises."""
    if os.environ.get("LEOFLOW_ON_FAILURE_CALLBACK") != "1":
        return
    try:
        try_number = int(os.environ.get("LEOFLOW_TRY_NUMBER", "1"))
    except ValueError:
        try_number = 1
    try:
        max_tries = int(os.environ.get("LEOFLOW_MAX_TRIES", str(try_number)))
    except ValueError:
        max_tries = try_number
    _run_on_failure_callback(on_failure_callback, context, try_number, max_tries)


def run_operator(operator_class: str, args: dict) -> None:
    """Instantiate and execute a captured Airflow operator/sensor (ADR 0040 Phase
    A): ``import_string(class)(task_id, **args) → render_template_fields → execute``.
    The provider is installed in the task image (declared via connectors:/
    dependencies:); connections resolve from AIRFLOW_CONN_* exactly as a hook's do.
    """
    _lifecycle(f"loading operator {operator_class}")
    module_name, _, class_name = operator_class.rpartition(".")
    if not module_name:
        raise ValueError(f"operator class must be dotted, got {operator_class!r}")
    op_cls = getattr(importlib.import_module(module_name), class_name)
    task_id = os.environ.get("LEOFLOW_TASK_ID", class_name)
    op = op_cls(task_id=task_id, **_merge_operator_xcom(dict(args)))

    context = _operator_context()
    try:
        op.render_template_fields(context)
    except Exception as exc:  # noqa: BLE001 — templating is best-effort in Phase A
        _lifecycle(f"render_template_fields skipped ({type(exc).__name__}: {exc})")

    _lifecycle(f"executing {class_name}.execute()")
    try:
        result = op.execute(context)
    except Exception as exc:  # noqa: BLE001 — translate reschedule/deferral; re-raise the rest
        if _is_reschedule_exc(exc):
            _signal_reschedule(exc, class_name)  # writes the file + SystemExit, or raises
        if _is_deferral_exc(exc):
            raise RuntimeError(
                f"{class_name} asked to defer (deferrable=True), which Dexaflow does not "
                f"support yet (ADR 0040 Phase C — no triggerer). Pass deferrable=False — "
                f"the operator runs synchronously in the pod (poke-style).") from exc
        # A genuine failure (not a reschedule/deferral): run the on_failure_callback
        # on the terminal attempt before re-raising, so the task's failed state is
        # unchanged (#424 inc 4b). The operator instance carries the (list-normalised)
        # callback as an attribute.
        _maybe_fire_on_failure_callback(context, _on_failure_from_task_object(op))
        raise
    _write_return(result)
    _write_extra_links(op, context["ti"].pushed)
    _write_xcom_pushes(context["ti"].pushed)


def _ti_key():
    """Build the TaskInstanceKey a link's get_link expects, from the run-context env.
    Returns None when Airflow is not importable (e.g. the runtime's Airflow-free unit
    tests), so callers fall back to the format_str path."""
    try:
        from airflow.models.taskinstancekey import TaskInstanceKey
    except Exception:  # noqa: BLE001 — no Airflow in this env
        return None
    try:
        try_number = int(os.environ.get("LEOFLOW_TRY_NUMBER", "1"))
    except (TypeError, ValueError):
        try_number = 1
    return TaskInstanceKey(
        dag_id=os.environ.get("LEOFLOW_DAG_ID", ""),
        task_id=os.environ.get("LEOFLOW_TASK_ID", ""),
        run_id=os.environ.get("LEOFLOW_RUN_ID", ""),
        try_number=try_number,
        map_index=-1,
    )


def _generic_link_url(link, op, ti_key, pushed: dict) -> str:
    """Resolve a link's URL the way Airflow's UI does — call ``link.get_link(op,
    ti_key)`` — but bridge ``BaseXCom.get_value`` to the params the operator persisted
    via ti.xcom_push (captured in ``pushed``) instead of the metastore we don't have.
    This is provider-agnostic: Google, Amazon, Azure, … all resolve through their own
    get_link (#375 / #379). Best-effort: any failure yields ""."""
    try:
        from airflow.sdk.bases.xcom import BaseXCom
    except Exception:  # noqa: BLE001 — no Airflow in this env
        return ""
    orig = BaseXCom.get_value
    BaseXCom.get_value = staticmethod(lambda *, ti_key, key: pushed.get(key))
    try:
        return link.get_link(op, ti_key=ti_key) or ""
    except Exception:  # noqa: BLE001 — a malformed link must not fail the task
        return ""
    finally:
        BaseXCom.get_value = orig


def _format_link_url(link, op, pushed: dict) -> str:
    """Fallback for Google-style links (BaseGoogleLink._format_link) when the generic
    get_link path is unavailable (no Airflow) — fills the link's format_str from the
    captured params."""
    key = getattr(link, "key", None)
    value = pushed.get(key) if key else None
    if isinstance(value, str) and value.startswith("http"):
        return value
    if not isinstance(value, dict):
        return ""
    fmt = getattr(link, "_format_link", None)
    if not callable(fmt):
        return ""
    conf = {**(getattr(op, "extra_links_params", {}) or {}), **value}
    conf.setdefault("namespace", "default")  # mirrors BaseGoogleLink.get_config
    try:
        return fmt(**conf) or ""
    except Exception:  # noqa: BLE001 — best-effort
        return ""


def _extra_links(op, pushed: dict) -> dict:
    """Compute the operator's UI deep-link buttons (operator_extra_links) from the
    params it persisted via ti.xcom_push during execute() (ADR 0040, #375). Primary
    path copies Airflow's own generic resolution (:func:`_generic_link_url` —
    ``link.get_link`` with a bridged XCom), which works for every provider; the
    format_str path (:func:`_format_link_url`) is the fallback when Airflow is absent.
    Best-effort: a link that cannot be computed is skipped, never raised.
    """
    out: dict = {}
    ti_key = _ti_key()
    for link in getattr(op, "operator_extra_links", ()) or ():
        name = getattr(link, "name", None)
        if not name:
            continue
        url = _generic_link_url(link, op, ti_key, pushed) if ti_key is not None else ""
        if not url:
            url = _format_link_url(link, op, pushed)
        if url:
            out[name] = url
    return out


def _write_extra_links(op, pushed: dict) -> None:
    """Write the computed operator_extra_links to LEOFLOW_EXTRA_LINKS_PATH as a
    ``{name: url}`` JSON map for the agent to ship back (ADR 0040, #375). Nothing is
    written when the operator exposes no resolvable link."""
    links = _extra_links(op, pushed)
    if not links:
        return
    path = os.environ.get("LEOFLOW_EXTRA_LINKS_PATH")
    if not path:
        return
    with open(path, "w", encoding="utf-8") as fh:
        json.dump(links, fh)
    _lifecycle(f"extra links: {sorted(links)}")


def _write_xcom_pushes(pushed: dict) -> None:
    """Write the operator's custom-keyed ti.xcom_push values to LEOFLOW_PUSHES_PATH
    as a ``{key: value}`` JSON map for the agent to store as XComs — multi-key XCom, so
    operator pushes show in the XCom tab like Airflow. ``return_value`` is excluded (it
    travels via :func:`_write_return`); nothing is written when there are no custom keys."""
    pushes = {k: v for k, v in pushed.items() if k != "return_value"}
    if not pushes:
        return
    path = os.environ.get("LEOFLOW_PUSHES_PATH")
    if not path:
        return
    with open(path, "w", encoding="utf-8") as fh:
        json.dump(pushes, fh)
    _lifecycle(f"xcom pushes: {sorted(pushes)}")


def _is_reschedule_exc(exc: BaseException) -> bool:
    """True if exc is Airflow's AirflowRescheduleException (matched by name across
    the MRO, so the runtime needn't import Airflow to recognize it). Reschedule
    mode is ADR 0040 Phase B; until then we translate it to a clear error instead
    of leaking a raw traceback (review polish #5)."""
    return any(base.__name__ == "AirflowRescheduleException" for base in type(exc).__mro__)


def _signal_reschedule(exc: BaseException, class_name: str) -> None:
    """A reschedule-mode sensor poked not-ready: if the agent wired
    RESCHEDULE_PATH_ENV, write the requested reschedule time there and exit with the
    reschedule sentinel so the agent reports up_for_reschedule — releasing the pod,
    consuming no retry budget (ADR 0040 Phase B, #380). If unwired (an older agent),
    fall back to a clear error rather than a silent sentinel exit. Never returns."""
    path = os.environ.get(RESCHEDULE_PATH_ENV)
    when = getattr(exc, "reschedule_date", None)
    if not path or when is None:
        raise RuntimeError(
            f"{class_name} asked to reschedule (sensor mode='reschedule'); this build "
            f"is not wired to deliver it (ADR 0040 Phase B, #380). Use mode='poke' — "
            f"the sensor holds the worker until its condition is met.") from exc
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(when.isoformat() if hasattr(when, "isoformat") else str(when))
    _lifecycle(f"{class_name} rescheduled until {when}")
    raise SystemExit(RESCHEDULE_EXIT_CODE)


def _is_deferral_exc(exc: BaseException) -> bool:
    """True if exc is Airflow's TaskDeferred (matched by name across the MRO, so the
    runtime needn't import Airflow). A deferrable operator (deferrable=True) raises
    it to suspend onto a trigger; Dexaflow has no triggerer yet (ADR 0040 Phase C),
    so we translate it into a clear "set deferrable=False" error rather than leaking
    a raw TaskDeferred traceback."""
    return any(base.__name__ == "TaskDeferred" for base in type(exc).__mro__)
