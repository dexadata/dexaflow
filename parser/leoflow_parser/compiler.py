"""Compile an Airflow DAG into the canonical Dexaflow dag.json.

The compiler imports the DAG module through Airflow's DagBag (which never runs
task bodies), inspects ``dag.task_dict``, and maps each operator to a Dexaflow
task. It supports Python (including TaskFlow ``@task``), Bash, and HTTP tasks.
"""
from __future__ import annotations

import inspect
import json
import os
import runpy
import sys
import warnings
from pathlib import Path
from typing import Any

# Per-project config arrives from the Go CLI via this env var. The CLI parses
# dexaflow.yaml with gopkg.in/yaml.v3 and hands the resolved (defaults
# applied) struct here as JSON — so the parser has zero third-party deps and
# the Go side stays the single source of truth for the config schema.
_CONFIG_ENV = "LEOFLOW_PROJECT_CONFIG_JSON"

_SUPPORTED_TRIGGER_RULES = {
    "all_success",
    "all_failed",
    "all_done",
    "one_success",
    "one_failed",
}


def compile_dag(
    source: str,
    config_path: str,
    image: str,
    dag_version: str = "dev",
) -> dict[str, Any]:
    """Compile the DAG in ``source`` into a dag.json dictionary."""
    config = _load_config(config_path)
    dag = _load_dag(source, config.get("dag_id"))

    spec: dict[str, Any] = {
        "schema_version": "1.0",
        "dag_id": dag.dag_id,
        "dag_version": dag_version,
        "image": image,
        "tasks": [_map_task(task, source, dag) for task in _ordered_tasks(dag)],
    }

    schedule = _schedule(dag)
    if schedule is not None:
        spec["schedule"] = schedule
    if config.get("owner"):
        spec["owner"] = config["owner"]
    tags = config.get("tags") or sorted(getattr(dag, "tags", []) or [])
    if tags:
        spec["tags"] = list(tags)
    # Declared secret sets (ADR 0045, ADR 0055): the per-DAG variables/connections
    # a task may receive. Airflow-native keys, distinct from connectors: (pip
    # provider packages, ADR 0038) which is a build concern the parser never emits.
    # Absent or empty declares nothing — omit the key so the compiled shape of a
    # DAG that declares nothing is unchanged (additive/back-compatible).
    connections = config.get("connections")
    if connections:
        spec["connections"] = list(connections)
    variables = config.get("variables")
    if variables:
        spec["variables"] = list(variables)
    default_args = _default_args(config)
    if default_args:
        spec["default_args"] = default_args
    params = _params(dag)
    if params:
        spec["params"] = params
    # Scheduling/metadata attributes the domain + scheduler honor. Emitted only
    # when set, so a DAG that declares none keeps its compiled shape.
    description = getattr(dag, "description", None)
    if isinstance(description, str) and description:
        spec["description"] = description
    start_date = _start_date(dag)
    if start_date:
        spec["start_date"] = start_date
    mar = getattr(dag, "max_active_runs", None)
    if isinstance(mar, int) and not isinstance(mar, bool) and mar > 0:
        spec["max_active_runs"] = mar
    if getattr(dag, "catchup", None) is True:
        spec["catchup"] = True
    return spec


def _load_config(path: str) -> dict[str, Any]:
    """Resolve the project config, preferring the Go-marshalled JSON in
    ``LEOFLOW_PROJECT_CONFIG_JSON`` (production path: the CLI parses the
    YAML and hands us the result). Falls back to reading ``path`` as JSON
    when the env var is unset — kept for direct test invocation. We do NOT
    fall back to YAML on disk: PyYAML was vendored solely to bridge this
    handshake and was removed at the alpha cut; pointing at a ``.yaml``
    path without the env var yields a clear actionable error rather than
    a silent ImportError."""
    raw = os.environ.get(_CONFIG_ENV)
    if raw is not None:
        if raw.strip() == "":
            return {}
        try:
            data = json.loads(raw)
        except json.JSONDecodeError as exc:  # pragma: no cover - operator error
            raise ValueError(
                f"{_CONFIG_ENV} is set but does not contain valid JSON: {exc}"
            ) from exc
        return data or {}
    if not path:
        raise ValueError(
            f"no project config available: set {_CONFIG_ENV} (the Dexaflow CLI "
            "does this automatically) or pass a JSON config file path"
        )
    if not os.path.isfile(path):
        raise ValueError(
            f"no project config available: {_CONFIG_ENV} is unset and "
            f"{path!r} does not exist. The Dexaflow CLI normally sets the "
            "env var; if you are invoking the parser directly, set it or "
            "pass a path to a JSON config file."
        )
    suffix = os.path.splitext(path)[1].lower()
    if suffix in (".yaml", ".yml"):
        raise ValueError(
            f"YAML config files are no longer parsed in-process (PyYAML was "
            f"removed at the alpha cut). Set {_CONFIG_ENV} to the JSON "
            f"output of `dexaflow compile` config resolution, or convert "
            f"{path!r} to JSON."
        )
    with open(path, encoding="utf-8") as handle:
        return json.loads(handle.read() or "{}") or {}


def _load_dag(source: str, dag_id: str | None):
    """Load the DAG(s) from ``source`` and select one. The default backend is the
    dependency-free structural shim (ADR 0024); set LEOFLOW_PARSER_BACKEND=airflow
    to use the real Airflow DagBag (requires apache-airflow installed)."""
    if os.environ.get("LEOFLOW_PARSER_BACKEND") == "airflow":
        dags, error = _load_dags_airflow(source)
    else:
        dags, error = _load_dags_shim(source)

    if error:
        raise ValueError(f"failed to import {source}: {error}")
    if not dags:
        raise ValueError(f"no DAG found in {source}")
    if dag_id and dag_id in dags:
        return dags[dag_id]
    if dag_id:
        raise ValueError(f"DAG {dag_id!r} not found in {source}; found {sorted(dags)}")
    if len(dags) > 1:
        raise ValueError(f"multiple DAGs in {source}; set dag_id in dexaflow.yaml")
    return next(iter(dags.values()))


def _ensure_shim_on_path() -> None:
    """Put the bundled `airflow` shim first on sys.path so the user's DAG imports
    resolve to it, dropping any real-airflow modules already imported in-process."""
    shim_dir = os.path.join(os.path.dirname(os.path.abspath(__file__)), "_shim")
    try:
        sys.path.remove(shim_dir)
    except ValueError:
        pass
    sys.path.insert(0, shim_dir)
    for name in list(sys.modules):
        if (name == "airflow" or name.startswith("airflow.")) and \
                shim_dir not in (getattr(sys.modules[name], "__file__", "") or ""):
            del sys.modules[name]


def _load_dags_shim(source: str):
    """Exec the DAG file against the structural shim; return ({dag_id: DAG}, error).
    An unsupported operator surfaces as a clear import error (ADR 0024)."""
    _ensure_shim_on_path()
    import airflow._core as core  # the shim

    core.reset()
    # Put the DAG's own directory on sys.path so sibling-module imports
    # (`from helpers import ...` next to dag.py) resolve, as Airflow's DagBag does.
    source_dir = os.path.dirname(os.path.abspath(source))
    added_dir = source_dir not in sys.path
    if added_dir:
        sys.path.insert(0, source_dir)
    before = set(sys.modules)
    try:
        runpy.run_path(source, run_name="__leoflow_dag__")
    except ModuleNotFoundError as exc:
        if _is_removed_core_operator_module(exc.name):
            return {}, _removed_core_operator_hint(exc.name)
        if _is_provider_module(exc.name):
            return {}, _provider_import_hint(exc.name)
        return {}, _unsupported(f"module {exc.name!r}")
    except ImportError as exc:
        # A missing name the shim does not provide (e.g. `chain`, a Branch operator).
        return {}, _unsupported(str(exc))
    except NotImplementedError as exc:
        # A construct the shim deliberately rejects (e.g. dynamic task mapping).
        return {}, _unsupported(str(exc))
    finally:
        # Isolate compiles: drop modules the DAG imported (incl. sibling helpers)
        # and remove its directory, so repeated compiles never serve stale code.
        for name in set(sys.modules) - before:
            del sys.modules[name]
        if added_dir:
            try:
                sys.path.remove(source_dir)
            except ValueError:
                pass
    return dict(core.COLLECTED), None


def _unsupported(detail: str) -> str:
    return (f"{detail}: not supported by Dexaflow "
            f"(supported: Bash, Http, Python/@task; no dynamic task mapping or task groups)")


_REMOVED_CORE_OPERATORS_PREFIX = "airflow.operators."


def _is_removed_core_operator_module(name: str | None) -> bool:
    """True for a core ``airflow.operators.<x>`` module. These operators were
    removed from Airflow core in 3.0 and relocated to
    apache-airflow-providers-standard, so they are never aliasable: aliasing the
    2.x-only spelling would accept a DAG that no Airflow-3 install can run."""
    return bool(name) and name.startswith(_REMOVED_CORE_OPERATORS_PREFIX)


def _removed_core_operator_hint(name: str) -> str:
    """Actionable message for a removed core-operator import. It names the
    canonical ``airflow.providers.standard.operators.<x>`` replacement (which the
    shim supports) instead of falling through to the generic operator-unsupported
    wording, so a migrating author sees the exact spelling to switch to.

    ``name`` is the failed module, e.g. 'airflow.operators.bash'; the submodule
    tail (``bash``) maps 1:1 onto the standard provider's operators package."""
    submodule = name[len(_REMOVED_CORE_OPERATORS_PREFIX):]
    canonical = "airflow.providers.standard.operators"
    if submodule:
        canonical = f"{canonical}.{submodule}"
    return (
        f"{name!r} was removed from Airflow core in 3.0 and relocated to the "
        f"standard provider. Import from {canonical!r} instead "
        f"(the apache-airflow-providers-standard package). Dexaflow targets "
        f"Airflow 3, so the pre-3 core-operator spelling is not accepted."
    )


def _is_provider_module(name: str | None) -> bool:
    """True for an Airflow provider module (`airflow.providers.<x>`), except the
    bundled `standard` provider — which the shim supplies, so a failure there is
    a real bug, not a missing-dependency the connectors: hint would address."""
    if not name:
        return False
    return name.startswith("airflow.providers.") and not name.startswith(
        "airflow.providers.standard"
    )


def _provider_import_hint(name: str) -> str:
    """Actionable message for a provider hook/operator imported at the DAG module
    top level. Dexaflow parses DAGs without providers installed, so the import
    fails here even when the provider IS declared — the fix is to import it inside
    the @task body (which the parser never executes) and declare it so it lands in
    the task runtime. ADR 0038 #2.

    `name` is the failed module, e.g. 'airflow.providers.postgres'. We do not
    translate it to a connector short name here (that mapping is the Go catalog's
    job); we point at the mechanism so the message stays correct for every
    provider, curated or not."""
    return (
        f"{name!r} is an Airflow provider imported at the DAG module top level, "
        f"which Dexaflow cannot resolve while parsing (providers are not installed "
        f"in the parser). Import the hook/operator INSIDE your @task function, and "
        f"declare the provider in dexaflow.yaml via `connectors:` (short names like "
        f"postgres, http) or `dependencies:` (an explicit pip package) so it is "
        f"installed in the task runtime."
    )


def _load_dags_airflow(source: str):
    """Fallback loader using the real Airflow DagBag (opt-in)."""
    from airflow.dag_processing.dagbag import DagBag

    with warnings.catch_warnings():
        warnings.simplefilter("ignore")
        bag = DagBag(dag_folder=source, include_examples=False)
    error = f"{bag.import_errors}" if bag.import_errors else None
    return dict(bag.dags), error


def _ordered_tasks(dag) -> list[Any]:
    return [dag.task_dict[task_id] for task_id in sorted(dag.task_dict)]


# A task's literal args (call_args / operator_args) ride as a SINGLE environment
# variable at dispatch (LEOFLOW_CALL_ARGS_JSON / LEOFLOW_OPERATOR_ARGS). POSIX caps
# a single env var at ~128 KiB on Linux; 100 KiB leaves headroom for the rest of
# the process environment. Over that, dispatch fails with a cryptic OS error — so
# gate it at compile with a clear pointer to the right pattern (#149).
_MAX_LITERAL_ARG_BYTES = 100 * 1024


def _check_literal_payload_size(task_id: str, kind: str, payload: dict[str, Any]) -> None:
    """Reject a literal-arg payload too large to ride in one env var at dispatch
    (#149). ``kind`` is "call_args" or "operator_args"; the error names the task,
    the size, and the fix (small constants only; large data via a Connection or
    external storage fetched inside the task)."""
    size = len(json.dumps(payload).encode("utf-8"))
    if size > _MAX_LITERAL_ARG_BYTES:
        raise ValueError(
            f"{kind} on task {task_id!r} is {size} bytes, over the "
            f"{_MAX_LITERAL_ARG_BYTES}-byte compile limit: it rides as a single "
            f"environment variable at dispatch and would exceed the POSIX env-var "
            f"limit, failing the task with a cryptic OS error. Pass small constants "
            f"only — for large data use a Connection or external storage (e.g. S3/GCS) "
            f"and fetch it inside the task."
        )


def _reject_deferrable(task) -> None:
    """Refuse an operator that sets ``deferrable=True`` at COMPILE time, before the
    image build — rather than letting it fail inside the pod after ``execute()``
    raises TaskDeferred (the runtime keeps its own reject as defense-in-depth).
    Explicit-kwarg only: an operator whose deferrable defaults through Airflow
    config carries no kwarg here, so this never fires on it. Dexaflow has no
    triggerer, so deferral is not supported; the fix is a synchronous poke or a
    reschedule-mode sensor.
    """
    args = getattr(task, "__leoflow_args__", None) or {}
    if getattr(task, "deferrable", None) is True or args.get("deferrable") is True:
        raise ValueError(
            f"task {task.task_id!r} sets deferrable=True, which Dexaflow does not "
            "support (no triggerer). Set deferrable=False so the operator runs "
            "synchronously in the pod (poke-style), or use a reschedule-mode "
            "sensor for a long wait."
        )


def _map_task(task, source: str, dag=None) -> dict[str, Any]:
    task_type = _operator_type(task)
    _reject_deferrable(task)
    entry: dict[str, Any] = {"task_id": task.task_id, "type": task_type}

    upstream = sorted(task.upstream_task_ids)
    if upstream:
        entry["depends_on"] = upstream

    rule = _trigger_rule(task)
    if rule != "all_success":
        entry["trigger_rule"] = rule

    _apply_scheduling_attrs(task, dag, entry)

    if task_type == "python":
        entry["entrypoint"] = _python_entrypoint(task, source)
        xcom_input, call_args = _bind_call_arguments(task)
        if xcom_input:
            entry["xcom_input"] = xcom_input
        if call_args:
            _check_literal_payload_size(task.task_id, "call_args", call_args)
            entry["call_args"] = call_args
    elif task_type == "bash":
        entry["entrypoint"] = _bash_command(task)
    elif task_type == "airflow_operator":
        entry["operator_class"] = type(task).__leoflow_operator_class__
        xcom_input, operator_args = _split_operator_args(task)
        if xcom_input:
            entry["xcom_input"] = xcom_input
        if operator_args:
            _check_literal_payload_size(task.task_id, "operator_args", operator_args)
            entry["operator_args"] = operator_args
    _check_callbacks(task, task_type, entry)
    return entry


# _ON_FAILURE_CALLBACK is the one Airflow callback kwarg Dexaflow runs (#424); the
# others stay a loud reject until wired.
_ON_FAILURE_CALLBACK = "on_failure_callback"

# Airflow scheduling attributes captured by the compiler outside a generic
# operator's constructor kwargs (#434): trigger_rule via _trigger_rule, the rest
# via _apply_scheduling_attrs. _split_operator_args skips them so they are not
# mistaken for the operator's own args (and a timedelta does not trip its reject).
_SCHEDULING_ATTR_NAMES = frozenset(
    {"trigger_rule", "retries", "retry_delay", "execution_timeout", "pool_slots"}
)

# Task types whose runtime path is Python with a real try/except, so an
# on_failure_callback can run in-process on the task's final failure (#424). A
# bash task execs bash in place (os.execvp), leaving no Python to run it.
_CALLBACK_CAPABLE_TYPES = ("airflow_operator", "python")


def _is_callback(value) -> bool:
    """Report whether ``value`` declares at least one callback.

    Airflow 3 normalises a task's callback attributes to a LIST (``[fn]``), while a
    bare ``@task(on_failure_callback=fn)`` keeps whatever was passed. Both forms are
    real, so both count. This mirrors the runtime's ``_normalize_callbacks``
    (#442) — the two must agree, or the compiler marks what the runtime cannot run,
    or drops what it could.

    An empty list declares nothing: marking it would make the runtime re-import
    dag.py on every failure to call no one."""
    if callable(value):
        return True
    if isinstance(value, (list, tuple)):
        return any(callable(item) for item in value)
    return False


def _has_callback(task, name: str) -> bool:
    """Report whether the task declares ``name``. Native tasks store it as an
    attribute (the shim setattrs every kwarg); a generically-captured operator
    carries it in ``__leoflow_args__``. Check both, in either shape.

    Recognising the list shape matters twice over: it is what marks a supported
    callback so the runtime runs it, AND what makes an UNSUPPORTED one hit the loud
    reject below. A predicate that only accepted a bare callable let the list form
    slip past both — the callback silently never ran, and the error promising that
    would never happen never fired (#470)."""
    if _is_callback(getattr(task, name, None)):
        return True
    return _is_callback((getattr(task, "__leoflow_args__", {}) or {}).get(name))


def _check_callbacks(task, task_type: str, entry: dict) -> None:
    """Resolve Airflow lifecycle callbacks (#424). ``on_failure_callback`` is marked
    on a Python-path task (operator or @task) so the runtime runs it; on a bash task
    it is a loud reject (no Python is left to run it). ``on_success_callback`` /
    ``on_retry_callback`` are unsupported everywhere and always a loud reject —
    never a silent drop, which would leave the author thinking their callback runs."""
    for name in ("on_success_callback", "on_retry_callback"):
        if _has_callback(task, name):
            raise ValueError(
                f"{name} on task {task.task_id!r} is not supported by Dexaflow yet — "
                "refusing to silently drop it. Use an alerts: block in dexaflow.yaml, "
                "or a downstream @task with a trigger_rule.")
    if not _has_callback(task, _ON_FAILURE_CALLBACK):
        return
    if task_type in _CALLBACK_CAPABLE_TYPES:
        entry[_ON_FAILURE_CALLBACK] = True
        return
    # bash execs bash in place (no Python left), so it cannot run a Python callback.
    raise ValueError(
        f"on_failure_callback on task {task.task_id!r} (type {task_type}) cannot run: "
        "only a Python-executed task runs it in-process (a provider operator or a "
        "@task). For HTTP use HttpOperator (a provider operator, which runs it); "
        "otherwise use an alerts: block in dexaflow.yaml, or a downstream @task with "
        "trigger_rule='one_failed'.")


def _split_operator_args(task) -> tuple[dict[str, list[str]], dict[str, Any]]:
    """Split a captured operator's constructor kwargs into XCom inputs and JSON
    literals (ADR 0040 A1.1).

    - An arg bound to an upstream task's output (``MyOperator(sql=extract())``)
      becomes an **xcom_input** — ``arg → [upstream_task_ids…]`` — so the agent
      fetches the upstream's return_value and the runtime injects it. A list of
      outputs is fan-in, mirroring the @task path (:func:`_bind_call_arguments`).
    - A JSON-serialisable literal becomes an **operator_arg**, carried verbatim
      in dag.json and delivered via LEOFLOW_OPERATOR_ARGS.

    An arg that is neither — a callable, a datetime, an arbitrary object — is a
    **loud compile error**, not a silent drop: the generic executor cannot carry
    it across dag.json, and dropping it would run the operator wrong. Move that
    logic into a @task, or pass a JSON-serialisable value / an upstream output.
    """
    raw = getattr(task, "__leoflow_args__", {}) or {}
    xcom: dict[str, list[str]] = {}
    operator_args: dict[str, Any] = {}
    for name, value in raw.items():
        # Airflow scheduling attributes (trigger_rule + retries/retry_delay/
        # execution_timeout) are captured separately — _trigger_rule and
        # _apply_scheduling_attrs (#434) — NOT as operator constructor args. Skip
        # them here: they are not the operator's own kwargs, and a timedelta
        # (retry_delay/execution_timeout) would otherwise trip the non-serialisable
        # reject below and fail an otherwise-valid provider operator at compile.
        if name in _SCHEDULING_ATTR_NAMES:
            continue
        # on_failure_callback cannot be serialised into dag.json, but it is
        # SUPPORTED (#424 inc 4): the runtime re-imports dag.py and calls it in the
        # task process on failure. Accept it here as a marker (see _has_callback)
        # instead of the non-serialisable reject.
        #
        # Both shapes, because Airflow 3 normalises the attribute to a list. Testing
        # only `callable` sent the list form on to the serialiser, which rejected it
        # as "a list Dexaflow cannot carry" — an error naming the wrong cause for a
        # construct that is in fact supported (#470).
        if name == _ON_FAILURE_CALLBACK and _is_callback(value):
            continue
        single_upstream = getattr(getattr(value, "operator", None), "task_id", None)
        if single_upstream:
            xcom[name] = [single_upstream]
            continue
        if isinstance(value, (list, tuple)) and value and all(
                getattr(getattr(item, "operator", None), "task_id", None)
                for item in value):
            xcom[name] = [item.operator.task_id for item in value]
            continue
        if _is_json_literal(value):
            operator_args[name] = value
            continue
        raise ValueError(
            f"operator argument {name!r} on task {task.task_id} is a "
            f"{type(value).__name__}, which Dexaflow cannot carry in dag.json; "
            f"pass a JSON-serialisable value or an upstream task's output, or "
            f"move the logic into a @task")
    return xcom, operator_args


def _operator_type(task) -> str:
    name = type(task).__name__
    # A dbt group placeholder (ADR 0043): the Go compiler expands it into one task
    # per dbt node after parsing. It carries no operator semantics here.
    if getattr(type(task), "__leoflow_dbt_group__", False):
        return "dbt_group"
    # Reject silent-wrong-execution shapes BEFORE the substring catch-alls below
    # (issue #225). BranchPythonOperator / ShortCircuitOperator / @task.branch
    # all carry "Python" in their class name, so the legacy substring match used
    # to translate them to a plain `python` task — and every "skipped" branch
    # would then actually execute. Refusing them at compile is the loud failure
    # the parser owes; remove the gate when real translation lands.
    # Control-flow operators need scheduler branch/skip support we do not have yet
    # (ADR 0040 Phase D); refuse rather than mistranslate. Provider operators and
    # sensors ARE captured generically below.
    for marker, feature in (
        ("Branch", "branching (@task.branch / BranchPythonOperator)"),
        ("ShortCircuit", "short-circuit (@task.short_circuit / ShortCircuitOperator)"),
    ):
        if marker in name:
            raise ValueError(
                f"unsupported operator {name!r} on task {task.task_id!r}: "
                f"{feature} is not supported by Dexaflow yet — refusing to "
                "silently mistranslate. See docs/dag-authoring.md for the "
                "current supported operator list."
            )
    # Any captured provider operator / sensor / transfer runs through the generic
    # executor (ADR 0040 Phase A): import_string(class)(**args).execute(context). Only
    # _generic-captured classes carry __leoflow_operator_class__; the bundled shim
    # operators (Bash/Python/Http) do not. This check MUST precede the substring
    # fast-path below: a long-tail operator whose class name happens to contain
    # Bash/Http/Python (e.g. AcmePythonModelOperator) would otherwise be mistranslated
    # into a native task, silently dropping its operator_class — the same
    # silent-mistranslation class as the HttpSensor regression guarded below.
    if getattr(type(task), "__leoflow_operator_class__", None):
        return "airflow_operator"
    # The native fast path is for the BUNDLED shim operators only (which carry no
    # __leoflow_operator_class__, so they fall through to here). A SENSOR whose class
    # name happens to contain Bash/Http/Python (HttpSensor, BashSensor, PythonSensor)
    # must NOT be mistranslated into a native one-shot task — sensors run via the
    # generic poke executor in their own pod. (An e2e caught HttpSensor silently
    # becoming an inline http_api call.) Airflow sensors conventionally end in "Sensor".
    if not name.endswith("Sensor"):
        if "Bash" in name:
            return "bash"
        # "Http" no longer maps to the native inline http_api (ADR 0047): an
        # HttpOperator is a captured provider operator (handled above) and runs in
        # a pod. The native inline path was the control-plane SSRF surface (H5).
        if "Python" in name:
            return "python"
    raise ValueError(f"unsupported operator {name!r} on task {task.task_id}")


def _trigger_rule(task) -> str:
    value = getattr(task.trigger_rule, "value", str(task.trigger_rule))
    if value not in _SUPPORTED_TRIGGER_RULES:
        raise ValueError(f"unsupported trigger rule {value!r} on task {task.task_id}")
    return value


def _sched_attr(task, dag, name: str):
    """Resolve an Airflow scheduling attribute (retries/retry_delay/execution_timeout)
    the way Airflow does: the value set on the operator wins; otherwise the DAG's
    default_args supplies it (#434). Returns None when neither sets it, so the task
    emits no key and the Go side's dexaflow.yaml defaults still apply."""
    value = getattr(task, name, None)
    if value is None and dag is not None:
        value = (getattr(dag, "default_args", None) or {}).get(name)
    return value


def _seconds(value) -> int | None:
    """Coerce an Airflow duration (a timedelta, or a bare int/float of seconds) to a
    whole number of seconds. None stays None."""
    if value is None:
        return None
    total = value.total_seconds() if hasattr(value, "total_seconds") else value
    return int(total)


def _apply_scheduling_attrs(task, dag, entry: dict[str, Any]) -> None:
    """Capture retries / retry_delay / execution_timeout / pool_slots (operator or
    default_args) onto the task entry, converting durations to seconds. Silently dropping these —
    the pre-#434 behavior — made a migrated DAG's retries=3 run once with no retry."""
    retries = _sched_attr(task, dag, "retries")
    if retries is not None:
        entry["retries"] = int(retries)
    retry_delay = _seconds(_sched_attr(task, dag, "retry_delay"))
    if retry_delay is not None:
        entry["retry_delay_seconds"] = retry_delay
    execution_timeout = _seconds(_sched_attr(task, dag, "execution_timeout"))
    if execution_timeout is not None:
        entry["execution_timeout_seconds"] = execution_timeout
    # pool_slots (ADR 0066): how many slots of its pool the task takes. 1 is
    # both Airflow's and the engine's default, so only a larger value is emitted
    # and a dag.json that does not use it stays byte-identical.
    pool_slots = _sched_attr(task, dag, "pool_slots")
    if pool_slots is not None and int(pool_slots) != 1:
        entry["pool_slots"] = int(pool_slots)


def _python_entrypoint(task, source: str) -> str:
    callable_obj = getattr(task, "python_callable", None)
    name = getattr(callable_obj, "__name__", task.task_id)
    return f"{Path(source).stem}:{name}"


def _bind_call_arguments(task) -> tuple[dict[str, list[str]], dict[str, Any]]:
    """Split a TaskFlow task's bound arguments into XCom inputs and literal call args.

    For ``transform(extract())`` the operator stores ``extract``'s XComArg as
    one argument; for ``shard(0)`` it stores the literal ``0``; for
    ``combine([a(), b(), c()])`` it stores a list of XComArgs (fan-in).
    Binding all of them against the callable's signature lets us split by type:

    - **xcom_input**: ``param_name → [upstream_task_ids…]``. ALWAYS a list,
      even for a single upstream (uniform schema). The agent fetches every
      upstream's ``return_value`` at dispatch time; for fan-in (len > 1)
      the runtime receives a JSON array, so the function parameter is the
      list of upstream values, in declaration order.
    - **call_args** (#115): ``param_name → JSON-serialisable literal``. The
      agent stamps the whole map as LEOFLOW_CALL_ARGS_JSON; the runtime
      decodes and delivers it to the function. Named call_args (not params)
      to keep Airflow's DAG-run params term free (#148).

    Arguments that are neither (e.g. a non-serialisable Python object) are
    silently dropped so the function falls back to its default — matching the
    pre-existing tolerance of the parser. An XComArg always wins precedence
    via the runtime layer, where the merge happens deterministically.
    """
    callable_obj = getattr(task, "python_callable", None)
    if callable_obj is None:
        return {}, {}
    op_args = getattr(task, "op_args", ()) or ()
    op_kwargs = getattr(task, "op_kwargs", {}) or {}
    try:
        bound = inspect.signature(callable_obj).bind_partial(*op_args, **op_kwargs)
    except (TypeError, ValueError):
        return {}, {}
    xcom: dict[str, list[str]] = {}
    call_args: dict[str, Any] = {}
    for name, value in bound.arguments.items():
        operator = getattr(value, "operator", None)
        single_upstream = getattr(operator, "task_id", None)
        if single_upstream:
            xcom[name] = [single_upstream]
            continue
        # Fan-in: `combine([a(), b(), c()])` binds a list of XComArgs to one
        # parameter. The agent fetches each upstream and the runtime receives
        # them as a JSON array (in declaration order). A mixed list with any
        # non-XComArg item is ambiguous (literal vs. upstream) — fall through
        # to the literal path so json.dumps rejects it cleanly.
        if isinstance(value, (list, tuple)) and value and all(
                getattr(getattr(item, "operator", None), "task_id", None)
                for item in value):
            xcom[name] = [item.operator.task_id for item in value]
            continue
        if _is_json_literal(value):
            call_args[name] = value
    return xcom, call_args


def _is_json_literal(value: Any) -> bool:
    """Reports whether value is safely round-trippable through JSON.

    The runtime delivers params via LEOFLOW_PARAMS, so a value that
    survives ``json.dumps`` cleanly is the safe-to-capture set. Anything
    else (a class instance, a tuple of objects, a function) is dropped so
    we never emit invalid JSON into dag.json.
    """
    try:
        json.dumps(value)
    except (TypeError, ValueError):
        return False
    return True


def _bash_command(task) -> str:
    return getattr(task, "bash_command", "") or ""


def _schedule(dag) -> str | None:
    timetable = getattr(dag, "timetable", None)
    summary = getattr(timetable, "summary", None)
    if isinstance(summary, str) and summary:
        return summary
    schedule = getattr(dag, "schedule", None)
    return schedule if isinstance(schedule, str) else None


def _start_date(dag) -> str | None:
    """Serialize the DAG's start_date to an ISO-8601 string (the domain field is
    a string; the schema wants date-time). Airflow's start_date is a datetime;
    accept anything with isoformat() and fall back to str()."""
    value = getattr(dag, "start_date", None)
    if value is None:
        return None
    iso = getattr(value, "isoformat", None)
    if callable(iso):
        return iso()
    text = str(value)
    return text or None


def _params(dag) -> dict[str, Any]:
    """Emit the DAG's author-declared params as ``{name: {default, schema}}``.

    A bare default (``params={"limit": 5}``) becomes ``{"default": 5, "schema": {}}``;
    a ``Param`` contributes its recorded default plus the JSON Schema it built from
    its kwargs. Returns an empty dict when the DAG declares no params, so the
    compiler omits the key and the compiled shape of a param-free DAG is unchanged.
    """
    declared = getattr(dag, "params", None)
    if not declared:
        return {}
    out: dict[str, Any] = {}
    for name, value in declared.items():
        schema = getattr(value, "schema", None)
        entry: dict[str, Any] = {"schema": dict(schema) if schema else {}}
        # Omit `default` for a required Param (one declared with no default), so
        # the trigger-time check can distinguish required from a null default. A
        # bare value (params={"limit": 5}) has no has_default attr → it IS its
        # own default.
        if getattr(value, "has_default", True):
            entry["default"] = getattr(value, "default", value)
        out[name] = entry
    return out


def _default_args(config: dict[str, Any]) -> dict[str, Any]:
    defaults = config.get("defaults") or {}
    out: dict[str, Any] = {}
    for key in ("retries", "retry_delay_seconds", "execution_timeout_seconds"):
        if key in defaults:
            out[key] = defaults[key]
    return out
