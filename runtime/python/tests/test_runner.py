"""Tests for the Dexaflow task runner."""

import itertools
import json

import pytest

from leoflow_runtime import runner

_counter = itertools.count()


def _write_module(tmp_path, monkeypatch, body: str) -> str:
    """Write a uniquely-named module so each test imports fresh code."""
    name = f"usermod_{next(_counter)}"
    (tmp_path / f"{name}.py").write_text(body)
    monkeypatch.syspath_prepend(str(tmp_path))
    return name


def test_run_writes_return_value(tmp_path, monkeypatch):
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    mod = _write_module(tmp_path, monkeypatch, "def task():\n    return {'rows': 7}\n")

    runner.run(f"{mod}:task")

    assert json.loads(out.read_text()) == {"rows": 7}


def test_run_injects_context_into_named_params(tmp_path, monkeypatch):
    # A @task gets the run context the same way a captured operator does: params named
    # after context keys are injected (def task(ds=None)), so native tasks are on par.
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    monkeypatch.setenv("LEOFLOW_DS", "2026-06-14")
    monkeypatch.setenv("LEOFLOW_RUN_ID", "run-9")
    body = "def task(ds=None, run_id=None):\n    return {'ds': ds, 'run_id': run_id}\n"
    mod = _write_module(tmp_path, monkeypatch, body)

    runner.run(f"{mod}:task")

    assert json.loads(out.read_text()) == {"ds": "2026-06-14", "run_id": "run-9"}


def test_run_injects_full_context_into_kwargs(tmp_path, monkeypatch):
    # def task(**context): gets the whole context (the documented "old style").
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    monkeypatch.setenv("LEOFLOW_DS", "2026-06-14")
    monkeypatch.setenv("LEOFLOW_PARAMS", '{"region": "us"}')
    body = ("def task(**context):\n"
            "    return {'ds': context['ds'], 'region': context['params']['region']}\n")
    mod = _write_module(tmp_path, monkeypatch, body)

    runner.run(f"{mod}:task")

    assert json.loads(out.read_text()) == {"ds": "2026-06-14", "region": "us"}


def test_run_explicit_binding_overrides_context(tmp_path, monkeypatch):
    # call_args / XCom win over context — existing behavior preserved, nothing broken.
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    monkeypatch.setenv("LEOFLOW_DS", "ctx-ds")
    monkeypatch.setenv("LEOFLOW_CALL_ARGS_JSON", '{"ds": "explicit-ds"}')
    mod = _write_module(tmp_path, monkeypatch, "def task(ds=None):\n    return {'ds': ds}\n")

    runner.run(f"{mod}:task")

    assert json.loads(out.read_text()) == {"ds": "explicit-ds"}


def test_run_task_ships_custom_xcom_pushes(tmp_path, monkeypatch):
    # A @task that does ti.xcom_push(key=...) (via **context) ships the custom keys
    # the same way operators do — multi-key XCom parity for native tasks (ADR 0040).
    pushes = tmp_path / "pushes.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(tmp_path / "rv.json"))
    monkeypatch.setenv("LEOFLOW_PUSHES_PATH", str(pushes))
    body = ("def task(**context):\n"
            "    context['ti'].xcom_push(key='row_count', value=7)\n"
            "    return {'ok': True}\n")
    mod = _write_module(tmp_path, monkeypatch, body)

    runner.run(f"{mod}:task")

    assert json.loads(pushes.read_text()) == {"row_count": 7}


def test_render_bash_renders_context_macros():
    # A bash command is Jinja-rendered with the run context, so {{ ds }} / {{ params.X }}
    # work like Airflow (ADR 0040 native parity), not just env vars.
    out = runner._render_bash("echo {{ ds }} {{ params.region }}",
                              {"ds": "2026-06-14", "params": {"region": "us"}})
    assert out == "echo 2026-06-14 us"


def test_render_bash_no_template_is_unchanged():
    assert runner._render_bash("echo hello", {"ds": "x"}) == "echo hello"


def test_render_bash_bad_template_falls_back_to_raw():
    # A broken/undefined template must never fail the task — fall back to the raw command
    # (the env vars $LEOFLOW_DS/$AIRFLOW_VAR_* still reach it).
    raw = "echo {{ nope.bad }}"
    assert runner._render_bash(raw, {}) == raw


def test_render_bash_sandbox_blocks_template_injection():
    # The bash command is rendered with a SandboxedEnvironment (Airflow parity), so a
    # server-side template-injection payload that reaches for Python internals is refused
    # at render time. The render fails closed → fall back to the raw command, never
    # executing the injection nor leaking the resolved attribute.
    raw = "echo {{ ''.__class__.__mro__[1].__subclasses__() }}"
    assert runner._render_bash(raw, {}) == raw


def test_run_without_return_writes_no_file(tmp_path, monkeypatch):
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    mod = _write_module(tmp_path, monkeypatch, "def task():\n    return None\n")

    runner.run(f"{mod}:task")

    assert not out.exists()


def test_run_rejects_bad_entrypoint():
    with pytest.raises(ValueError):
        runner.run("no_callable_here")


def test_run_lifecycle_never_logs_kwargs_values(tmp_path, monkeypatch, capsys):
    """ADR 0032: the runtime MUST NOT dump kwargs values to stdout — only the
    keys. Values can carry XCom-pulled secrets or any user payload; they
    belong in the XCom tab, not in the log file. Regression guard for the Lima
    leak found 2026-06-01 where `resolved kwargs: {'raw': 'SECRET'}` was
    bleeding the pulled XCom value straight into the log.
    """
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    secret = "UPSTREAM_SECRET_VALUE_42"  # noqa: S105 — synthetic test marker, not a real secret
    monkeypatch.setenv("LEOFLOW_XCOM_RAW", f'"{secret}"')
    mod = _write_module(tmp_path, monkeypatch, (
        "def consumer(raw):\n"
        "    return len(raw)\n"
    ))

    runner.run(f"{mod}:consumer")
    stdout = capsys.readouterr().out

    # The kwargs lifecycle line MUST list keys but NEVER include the value.
    assert secret not in stdout, (
        f"ADR 0032 violation: pulled XCom value leaked into stdout:\n{stdout}"
    )
    assert "resolved kwargs: ['raw']" in stdout, (
        f"expected the keys-only kwargs line in:\n{stdout}"
    )
    # The pulled metadata line is still expected (size only).
    assert "[leoflow] pulled raw" in stdout
    assert "(26 B)" in stdout  # the JSON-encoded secret has 26 bytes


def test_run_logs_xcom_pulls_with_size(tmp_path, monkeypatch, capsys):
    """When an upstream XCom is consumed, the runtime MUST log it with the wire
    size — invaluable when debugging "received None" in a downstream task
    (was the upstream silent? did the name match? how big is the payload?).
    The line goes inside the Pre task execution group with the [leoflow]
    prefix.
    """
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    # Two upstreams the function accepts as kwargs; agent would inject these.
    monkeypatch.setenv("LEOFLOW_XCOM_RAW", '{"rows":3}')
    monkeypatch.setenv("LEOFLOW_XCOM_SUMMARY", '"ok"')
    mod = _write_module(tmp_path, monkeypatch, (
        "def task(raw, summary):\n"
        "    return {'r': raw, 's': summary}\n"
    ))

    runner.run(f"{mod}:task")

    stdout = capsys.readouterr().out
    # Each pulled XCom gets its own [leoflow] pulled line with byte size.
    assert "[leoflow] pulled raw (10 B)" in stdout, f"missing raw pull line:\n{stdout}"
    assert "[leoflow] pulled summary (4 B)" in stdout, f"missing summary pull line:\n{stdout}"


def test_run_emits_flat_lifecycle_lines(tmp_path, monkeypatch, capsys):
    """The runtime MUST emit lifecycle lines as plain log lines, NOT wrapped in
    ``::group::``/``::endgroup::`` markers.

    The Airflow 3.2 SPA recognizes the markers but renders them as
    <details open={hash-only}> elements whose controlled-component toggle does
    not respond to clicks — so wrapping our lifecycle in groups made them
    invisible by default with no reliable way to expand. This test pins the
    flat layout (loading / kwargs / returned all visible by default) until the
    SPA-side toggle bug is addressed.
    """
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    mod = _write_module(tmp_path, monkeypatch, (
        "def task():\n"
        "    print('USER_LINE_INSIDE_TASK')\n"
        "    return {'rows': 7}\n"
    ))

    runner.run(f"{mod}:task")

    stdout = capsys.readouterr().out
    # No drill-down markers should be emitted.
    assert "::group::" not in stdout, f"::group:: marker leaked into stdout:\n{stdout}"
    assert "::endgroup::" not in stdout, f"::endgroup:: marker leaked into stdout:\n{stdout}"
    # All three lifecycle lines and the user print MUST be present, in order.
    loading = stdout.find("[leoflow] loading")
    user_line = stdout.find("USER_LINE_INSIDE_TASK")
    returned = stdout.find("[leoflow] returned")
    assert loading != -1, f"missing loading line in:\n{stdout}"
    assert user_line != -1, f"missing user print in:\n{stdout}"
    assert returned != -1, f"missing returned line in:\n{stdout}"
    assert loading < user_line < returned, (
        f"ordering wrong: loading at {loading}, user at {user_line}, "
        f"returned at {returned} in:\n{stdout}"
    )


def test_run_propagates_user_exception(tmp_path, monkeypatch):
    mod = _write_module(tmp_path, monkeypatch, "def task():\n    raise RuntimeError('boom')\n")
    with pytest.raises(RuntimeError, match="boom"):
        runner.run(f"{mod}:task")


def test_run_unwraps_taskflow_decorator(tmp_path, monkeypatch):
    # Airflow TaskFlow @task objects return an XComArg (not the result) when
    # called bare; the runner must unwrap to .function and run the real code.
    # Regression guard for the pod-path bug where the task wrote a non-JSON
    # XComArg and exited 1.
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    body = (
        "class _XComArg:\n"
        "    pass\n"
        "class _TaskDecorator:\n"
        "    def __call__(self):\n"
        "        return _XComArg()  # not JSON serializable, mimics TaskFlow\n"
        "    def function(self):\n"
        "        return {'ran': True}\n"
        "task = _TaskDecorator()\n"
    )
    mod = _write_module(tmp_path, monkeypatch, body)

    runner.run(f"{mod}:task")

    assert json.loads(out.read_text()) == {"ran": True}


def test_run_resolves_xcom_input_arguments(tmp_path, monkeypatch):
    """A task consuming an upstream output receives it via LEOFLOW_XCOM_<param>."""
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    # the agent injects extract's return_value as the 'n' input
    monkeypatch.setenv("LEOFLOW_XCOM_N", "21")
    mod = _write_module(tmp_path, monkeypatch, "def transform(n):\n    return n * 2\n")

    runner.run(f"{mod}:transform")

    assert json.loads(out.read_text()) == 42


def test_run_leaves_unbound_params_to_defaults(tmp_path, monkeypatch):
    """A parameter with no injected XCom falls back to its default (no crash)."""
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    mod = _write_module(tmp_path, monkeypatch, "def task(x=5):\n    return x + 1\n")

    runner.run(f"{mod}:task")

    assert json.loads(out.read_text()) == 6


def test_run_resolves_literal_call_args_from_json(tmp_path, monkeypatch):
    """TaskFlow literal args (#115): @task f(5) captured at compile, delivered at run.

    The agent stamps LEOFLOW_CALL_ARGS_JSON with the literals captured by the
    parser. The runtime decodes and merges them into kwargs, so a
    ``shard(n=0)`` invocation at DAG-build time delivers ``n=0`` at execution.
    """
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    monkeypatch.setenv("LEOFLOW_CALL_ARGS_JSON", json.dumps({"n": 7}))
    mod = _write_module(tmp_path, monkeypatch, "def shard(n):\n    return n * 3\n")

    runner.run(f"{mod}:shard")

    assert json.loads(out.read_text()) == 21


def test_run_literal_call_args_carry_complex_values(tmp_path, monkeypatch):
    """Nested JSON literals (dicts, lists, None) round-trip through CALL_ARGS_JSON."""
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    payload = {"opts": {"shards": [1, 2, 3], "name": "demo"}, "limit": None}
    monkeypatch.setenv("LEOFLOW_CALL_ARGS_JSON", json.dumps(payload))
    mod = _write_module(
        tmp_path, monkeypatch,
        "def task(opts, limit):\n    return [opts['shards'], opts['name'], limit]\n",
    )

    runner.run(f"{mod}:task")

    assert json.loads(out.read_text()) == [[1, 2, 3], "demo", None]


def test_run_xcom_wins_over_literal_call_arg(tmp_path, monkeypatch):
    """XCom precedence: an upstream output supersedes a literal of the same name.

    Matches Airflow semantics — when a parameter is bound to both an upstream
    XComArg and a literal, the XComArg wins at runtime (the literal is only a
    compile-time placeholder, in practice you would never bind both, but the
    contract has to be deterministic).
    """
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    monkeypatch.setenv("LEOFLOW_CALL_ARGS_JSON", json.dumps({"n": 1}))
    monkeypatch.setenv("LEOFLOW_XCOM_N", "100")
    mod = _write_module(tmp_path, monkeypatch, "def task(n):\n    return n\n")

    runner.run(f"{mod}:task")

    assert json.loads(out.read_text()) == 100


def test_run_ignores_malformed_call_args_json(tmp_path, monkeypatch):
    """Malformed CALL_ARGS_JSON does not crash the runtime; silently dropped.

    The parser's contract is to emit valid JSON; if the env is malformed, the
    task is better off running with no literals (it may still find defaults
    or XCom) than dying with a JSON error the user never wrote.
    """
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    monkeypatch.setenv("LEOFLOW_CALL_ARGS_JSON", "{not valid json")
    mod = _write_module(tmp_path, monkeypatch, "def task(x=5):\n    return x\n")

    runner.run(f"{mod}:task")

    assert json.loads(out.read_text()) == 5


def test_run_operator_executes_captured_provider_operator(tmp_path, monkeypatch):
    """ADR 0040 A3: run_operator imports a captured operator and executes it,
    writing its return as the XCom. Skips when Airflow is not installed (the lean
    runtime test env); runs against the real operator otherwise."""
    pytest.importorskip("airflow.providers.standard.operators.bash")
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(tmp_path / "ret.json"))
    monkeypatch.setenv("LEOFLOW_TASK_ID", "t")
    runner.run_operator(
        "airflow.providers.standard.operators.bash.BashOperator",
        {"bash_command": "echo generic-op-ran"},
    )
    assert json.loads((tmp_path / "ret.json").read_text()) == "generic-op-ran"


def test_merge_operator_xcom_injects_upstream_values(monkeypatch):
    """An operator arg bound to an upstream (recorded as xcom_input) is delivered
    as LEOFLOW_XCOM_<PARAM>; _merge_operator_xcom must set it on the kwargs,
    overriding any same-name literal (ADR 0040 A1.1). Pure: no Airflow needed."""
    monkeypatch.setenv("LEOFLOW_XCOM_SQL", '"SELECT 42"')
    merged = runner._merge_operator_xcom({"conn_id": "sf", "sql": "PLACEHOLDER"})
    assert merged["sql"] == "SELECT 42"   # XCom overrides the literal
    assert merged["conn_id"] == "sf"      # untouched literal survives


def test_merge_operator_xcom_noop_without_xcom(monkeypatch):
    """With no LEOFLOW_XCOM_* in the env, the operator kwargs pass through."""
    monkeypatch.delenv("LEOFLOW_XCOM_SQL", raising=False)
    merged = runner._merge_operator_xcom({"conn_id": "sf"})
    assert merged == {"conn_id": "sf"}


def test_is_reschedule_exc_matches_by_name():
    """AirflowRescheduleException is recognized by class name across the MRO, so
    the runtime translates reschedule-mode sensors to a clear error without
    importing Airflow (review polish #5)."""
    class AirflowRescheduleException(Exception):
        pass

    class Subclass(AirflowRescheduleException):
        pass

    assert runner._is_reschedule_exc(AirflowRescheduleException())
    assert runner._is_reschedule_exc(Subclass())
    assert not runner._is_reschedule_exc(ValueError("nope"))


def test_standalone_ti_get_first_reschedule_date(monkeypatch):
    """get_first_reschedule_date returns None on the first attempt (env unset) and the
    delivered first-reschedule time once the agent stamps LEOFLOW_FIRST_RESCHEDULE_AT,
    so a reschedule-mode sensor honors its cumulative timeout (#380). The full
    reschedule signal path (wired/not-wired) is covered with a fake in test_operator."""
    from datetime import datetime, timezone
    monkeypatch.delenv("LEOFLOW_FIRST_RESCHEDULE_AT", raising=False)
    assert runner._StandaloneTaskInstance().get_first_reschedule_date({}) is None
    monkeypatch.setenv("LEOFLOW_FIRST_RESCHEDULE_AT", "2099-01-02T03:04:05+00:00")
    got = runner._StandaloneTaskInstance().get_first_reschedule_date({})
    assert got == datetime(2099, 1, 2, 3, 4, 5, tzinfo=timezone.utc)


# --- on_failure_callback core (#424 inc 4b) -------------------------------------
#
# Reality anchors (regression guards for the two integration bugs the e2e caught):
#   1. Airflow 3 normalises a task's on_failure_callback to a LIST, e.g.
#      ``on_failure_callback=[fn]`` — never a bare callable on the operator. The
#      runtime must call each element, not the list.
#   2. The callback is resolved from the loaded task OBJECT (a @task decorator keeps
#      it in ``.kwargs``; an operator exposes it as an attribute), NOT by scanning
#      module globals for a bound DAG — so an unbound ``with DAG():`` still works.

def test_normalize_callbacks_accepts_bare_list_and_none():
    def a(_ctx):
        pass

    def b(_ctx):
        pass
    assert runner._normalize_callbacks(None) == []
    assert runner._normalize_callbacks(a) == [a]
    assert runner._normalize_callbacks([a, b]) == [a, b]          # Airflow's list shape
    assert runner._normalize_callbacks((a,)) == [a]
    assert runner._normalize_callbacks([a, "not-callable", None]) == [a]  # filters junk


def test_on_failure_from_task_object_reads_decorator_kwargs_and_operator_attr():
    class _Decorator:  # a TaskFlow @task keeps its kwargs here
        kwargs = {"on_failure_callback": ["cb-from-decorator"], "task_id": "t"}

    class _Operator:  # an operator exposes the (list-normalised) attribute
        on_failure_callback = ["cb-from-operator"]

    class _Bare:
        pass
    assert runner._on_failure_from_task_object(_Decorator()) == ["cb-from-decorator"]
    assert runner._on_failure_from_task_object(_Operator()) == ["cb-from-operator"]
    assert runner._on_failure_from_task_object(_Bare()) is None


def test_on_failure_callback_fires_on_final_attempt():
    calls = []
    runner._run_on_failure_callback(
        lambda ctx: calls.append(ctx), {"dag_id": "d"}, try_number=3, max_tries=3)
    assert calls == [{"dag_id": "d"}]


def test_on_failure_callback_list_all_fire():
    # Bug #2 guard: Airflow hands a LIST; every callback in it must run.
    calls = []
    runner._run_on_failure_callback(
        [lambda ctx: calls.append("a"), lambda ctx: calls.append("b")],
        {}, try_number=1, max_tries=1)
    assert calls == ["a", "b"]


def test_on_failure_callback_skipped_when_retries_remain():
    calls = []
    runner._run_on_failure_callback(
        lambda ctx: calls.append(ctx), {}, try_number=1, max_tries=3)
    assert calls == []  # a retry will follow — not a terminal failure


def test_on_failure_callback_swallows_error_and_still_runs_the_rest():
    calls = []

    def boom(_ctx):
        raise RuntimeError("nope")
    # best-effort: a raising callback must NOT stop the others, nor fail the task.
    runner._run_on_failure_callback(
        [boom, lambda ctx: calls.append("ran")], {}, try_number=1, max_tries=1)
    assert calls == ["ran"]


def test_on_failure_callback_noop_when_absent():
    # None (no callback declared) → no-op, no error
    runner._run_on_failure_callback(None, {}, try_number=1, max_tries=1)


def test_maybe_fire_noop_without_marker(monkeypatch):
    monkeypatch.delenv("LEOFLOW_ON_FAILURE_CALLBACK", raising=False)
    calls = []
    runner._maybe_fire_on_failure_callback({}, [lambda ctx: calls.append(ctx)])
    assert calls == []  # marker absent → never fires


def test_maybe_fire_runs_on_final_attempt(monkeypatch):
    calls = []
    monkeypatch.setenv("LEOFLOW_ON_FAILURE_CALLBACK", "1")
    monkeypatch.setenv("LEOFLOW_TRY_NUMBER", "2")
    monkeypatch.setenv("LEOFLOW_MAX_TRIES", "2")
    # Pass the callback the way real Airflow carries it: a list.
    runner._maybe_fire_on_failure_callback({"dag_id": "d"}, [lambda ctx: calls.append(ctx)])
    assert calls == [{"dag_id": "d"}]


def test_maybe_fire_skips_when_retries_remain(monkeypatch):
    calls = []
    monkeypatch.setenv("LEOFLOW_ON_FAILURE_CALLBACK", "1")
    monkeypatch.setenv("LEOFLOW_TRY_NUMBER", "1")
    monkeypatch.setenv("LEOFLOW_MAX_TRIES", "3")
    runner._maybe_fire_on_failure_callback({}, [lambda ctx: calls.append(ctx)])
    assert calls == []


def test_render_bash_quotes_conf_value_blocks_shell_injection(tmp_path):
    # `params` comes from the DAG-run conf, which anyone with execute:dag supplies
    # (a lower bar than write:dag). A value carrying shell metacharacters must be
    # neutralized: only the trusted template text is shell syntax; interpolated
    # values are quoted. Realistic payload — a command substitution that would
    # create a file — run through the real bash the task uses (issue #489).
    import subprocess

    sentinel = tmp_path / "pwned"
    payload = f"$(touch {sentinel})"
    rendered = runner._render_bash("true {{ params.name }}", {"params": {"name": payload}})
    subprocess.run(["bash", "-c", rendered], check=False)  # noqa: S603,S607 — deliberately exec bash to prove the payload is inert
    assert not sentinel.exists(), (
        f"shell injection executed via conf: rendered={rendered!r} created {sentinel}")


def test_render_bash_quotes_metachars_as_single_token(tmp_path):
    # A ';'-based payload must stay a single argument to the trusted command, not
    # become a second command. Proven by executing: the injected `touch` must not run.
    import subprocess

    sentinel = tmp_path / "pwned2"
    payload = f"x; touch {sentinel}"
    rendered = runner._render_bash("echo {{ params.name }}", {"params": {"name": payload}})
    subprocess.run(["bash", "-c", rendered], check=False)  # noqa: S603,S607 — deliberately exec bash to prove the payload is inert
    assert not sentinel.exists(), (
        f"';' in conf started a new command: rendered={rendered!r} created {sentinel}")
