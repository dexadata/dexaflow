"""Tests for the Dexaflow task runner."""

import itertools
import json
import sys
import types
from datetime import datetime, timezone

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


def test_render_bash_undefined_template_fails_with_field_and_error():
    # A template that cannot render must fail the attempt, never run the raw
    # "{{ ... }}" text (#1845). The error names the field and keeps Jinja's message.
    with pytest.raises(runner.TemplateRenderError) as info:
        runner._render_bash("echo {{ nope.bad }}", {})
    assert info.value.field == "bash_command"
    assert "'nope' is undefined" in str(info.value)
    assert "'bash_command'" in str(info.value)


def test_render_bash_syntax_error_fails():
    with pytest.raises(runner.TemplateRenderError) as info:
        runner._render_bash("echo {{ ds", {"ds": "2026-06-14"})
    assert info.value.field == "bash_command"


def test_render_bash_literal_braces_need_a_raw_block():
    # A command that carries literal braces for another tool (docker --format, a Go
    # template) is a Jinja syntax error now that render errors fail; a raw block,
    # as on Airflow, passes the braces through untouched and unquoted.
    with pytest.raises(runner.TemplateRenderError):
        runner._render_bash("docker ps --format '{{.Names}}'", {})
    out = runner._render_bash(
        "docker ps --format '{% raw %}{{.Names}}{% endraw %}' {{ ds }}", {"ds": "2026-06-14"})
    assert out == "docker ps --format '{{.Names}}' 2026-06-14"


def test_render_bash_sandbox_blocks_template_injection():
    # The bash command is rendered with a SandboxedEnvironment (Airflow parity), so a
    # server-side template-injection payload that reaches for Python internals is refused
    # at render time. The render fails closed: the attempt fails, nothing is executed
    # and the resolved attribute never reaches the shell.
    raw = "echo {{ ''.__class__.__mro__[1].__subclasses__() }}"
    with pytest.raises(runner.TemplateRenderError) as info:
        runner._render_bash(raw, {})
    assert "SecurityError" in str(info.value)


def test_render_bash_without_jinja_fails_instead_of_running_raw(monkeypatch):
    # No jinja2 in the image means the template cannot render: fail, do not run the
    # raw template text (#1845). The Task SDK in the base image brings jinja2.
    monkeypatch.setitem(sys.modules, "jinja2.sandbox", None)
    with pytest.raises(runner.TemplateRenderError) as info:
        runner._render_bash("echo {{ ds }}", {"ds": "2026-06-14"})
    assert "jinja2" in str(info.value)


def test_run_bash_render_error_never_execs(monkeypatch, capsys):
    execs = []
    monkeypatch.setattr(runner.os, "execvp", lambda *a: execs.append(a))
    with pytest.raises(runner.TemplateRenderError):
        runner.run_bash("echo {{ ds }} {{ nope.x }}")
    assert execs == []
    out = capsys.readouterr().out
    assert "'bash_command'" in out
    assert "'nope' is undefined" in out


def test_run_bash_renders_macros_and_logical_date(monkeypatch):
    # The two keys common templates use that the context lacked (#1843):
    # macros.ds_add raised UndefinedError and logical_date rendered empty.
    execs = []
    monkeypatch.setattr(runner.os, "execvp", lambda *a: execs.append(a))
    monkeypatch.setenv("LEOFLOW_DS", "2026-06-14")
    monkeypatch.setenv("LEOFLOW_TS", "2026-06-14T05:30:00Z")
    runner.run_bash("echo {{ macros.ds_add(ds, 1) }} {{ logical_date.strftime('%Y%m%dT%H%M') }}")
    assert execs == [("bash", ["bash", "-c", "echo 2026-06-15 20260614T0530"])]


def test_operator_context_logical_date(monkeypatch):
    monkeypatch.setenv("LEOFLOW_TS", "2026-06-14T05:30:00Z")
    assert runner._operator_context()["logical_date"] == datetime(
        2026, 6, 14, 5, 30, tzinfo=timezone.utc)
    monkeypatch.delenv("LEOFLOW_TS")
    ctx = runner._operator_context()
    assert "logical_date" in ctx
    assert ctx["logical_date"] is None


def test_operator_context_date_keys(monkeypatch):
    # The keys the Task SDK derives from the logical date, with its own formats (#1843).
    monkeypatch.setenv("LEOFLOW_DAG_ID", "report")
    monkeypatch.setenv("LEOFLOW_TASK_ID", "build")
    monkeypatch.setenv("LEOFLOW_TS", "2026-06-14T05:30:00Z")
    monkeypatch.setenv("LEOFLOW_DS", "2026-06-14")
    ctx = runner._operator_context()
    assert ctx["ds_nodash"] == "20260614"
    assert ctx["ts_nodash"] == "20260614T053000"
    assert ctx["ts_nodash_with_tz"] == "20260614T053000+0000"
    assert ctx["task_instance_key_str"] == "report__build__20260614"


def test_operator_context_date_keys_absent_without_logical_date(monkeypatch):
    # The Task SDK sets them only for a run with a logical date; a run without one
    # keys task_instance_key_str by run_id instead.
    monkeypatch.delenv("LEOFLOW_TS", raising=False)
    monkeypatch.setenv("LEOFLOW_DAG_ID", "report")
    monkeypatch.setenv("LEOFLOW_TASK_ID", "build")
    monkeypatch.setenv("LEOFLOW_RUN_ID", "manual__1")
    ctx = runner._operator_context()
    for key in ("ds_nodash", "ts_nodash", "ts_nodash_with_tz"):
        assert key not in ctx
    assert ctx["task_instance_key_str"] == "report__build__manual__1"


def test_operator_context_try_number(monkeypatch):
    monkeypatch.setenv("LEOFLOW_TRY_NUMBER", "3")
    assert runner._operator_context()["try_number"] == 3


def test_operator_context_dag_run(monkeypatch):
    # dag_run was None, so operators and callbacks reading dag_run.conf crashed (#1843).
    monkeypatch.setenv("LEOFLOW_DAG_ID", "report")
    monkeypatch.setenv("LEOFLOW_RUN_ID", "manual__2026-06-14T05:30:00+00:00")
    monkeypatch.setenv("LEOFLOW_TS", "2026-06-14T05:30:00Z")
    monkeypatch.setenv("LEOFLOW_DATA_INTERVAL_START", "2026-06-13T05:30:00Z")
    monkeypatch.setenv("LEOFLOW_DATA_INTERVAL_END", "2026-06-14T05:30:00Z")
    monkeypatch.setenv("LEOFLOW_RUN_CONF", '{"table": "orders"}')
    monkeypatch.setenv("LEOFLOW_RUN_TYPE", "scheduled")
    dag_run = runner._operator_context()["dag_run"]
    assert dag_run.dag_id == "report"
    assert dag_run.run_id == "manual__2026-06-14T05:30:00+00:00"
    assert dag_run.logical_date == datetime(2026, 6, 14, 5, 30, tzinfo=timezone.utc)
    assert dag_run.data_interval_start == datetime(2026, 6, 13, 5, 30, tzinfo=timezone.utc)
    assert dag_run.data_interval_end == datetime(2026, 6, 14, 5, 30, tzinfo=timezone.utc)
    assert dag_run.conf == {"table": "orders"}
    assert dag_run.run_type == "scheduled"


def test_operator_context_dag_run_conf_is_the_conf_not_params(monkeypatch):
    # params layers DAG and task defaults under the conf; dag_run.conf is the
    # conf alone, as on Airflow.
    monkeypatch.setenv("LEOFLOW_PARAMS", '{"table": "orders", "limit": 5}')
    monkeypatch.setenv("LEOFLOW_RUN_CONF", '{"table": "orders"}')
    assert runner._operator_context()["dag_run"].conf == {"table": "orders"}


def test_operator_context_dag_run_bad_conf_is_empty(monkeypatch):
    monkeypatch.setenv("LEOFLOW_RUN_CONF", "null")
    assert runner._operator_context()["dag_run"].conf == {}
    monkeypatch.setenv("LEOFLOW_RUN_CONF", "{oops")
    assert runner._operator_context()["dag_run"].conf == {}


def test_operator_context_dag_run_without_delivered_conf_says_so(monkeypatch):
    # A control plane older than the agent delivers no conf and no run type: the
    # attribute is missing with a message naming it, not a made-up empty conf.
    monkeypatch.delenv("LEOFLOW_RUN_CONF", raising=False)
    monkeypatch.delenv("LEOFLOW_RUN_TYPE", raising=False)
    dag_run = runner._operator_context()["dag_run"]
    with pytest.raises(AttributeError, match="'conf'.*Dexaflow"):
        _ = dag_run.conf
    assert not hasattr(dag_run, "run_type")


def test_operator_context_dag_and_task_identity(monkeypatch):
    # Outside an operator run the runtime has no DAG or task object; the stand-ins
    # carry their ids, and ti.task is the same task (#1843).
    monkeypatch.setenv("LEOFLOW_DAG_ID", "report")
    monkeypatch.setenv("LEOFLOW_TASK_ID", "build")
    ctx = runner._operator_context()
    assert ctx["dag"].dag_id == "report"
    assert ctx["task"].task_id == "build"
    assert ctx["task"].dag_id == "report"
    assert ctx["ti"].task is ctx["task"]


@pytest.mark.parametrize("key,name", [
    ("dag", "get_task"), ("task", "execute"), ("dag_run", "start_date"), ("ti", "hostname")])
def test_operator_context_missing_attribute_names_it(key, name):
    # A provider that reaches for an attribute the runtime lacks gets an error that
    # names it and says Dexaflow does not provide it, not a bare AttributeError.
    obj = runner._operator_context()[key]
    with pytest.raises(AttributeError, match=f"'{name}'.*Dexaflow does not provide"):
        getattr(obj, name)
    assert getattr(obj, name, "fallback") == "fallback"


def test_standalone_ti_max_tries(monkeypatch):
    monkeypatch.setenv("LEOFLOW_TRY_NUMBER", "2")
    monkeypatch.setenv("LEOFLOW_MAX_TRIES", "4")
    assert runner._operator_context()["ti"].max_tries == 4
    monkeypatch.delenv("LEOFLOW_MAX_TRIES")
    assert runner._operator_context()["ti"].max_tries == 2


def test_run_bash_renders_the_new_context_keys(monkeypatch):
    execs = []
    monkeypatch.setattr(runner.os, "execvp", lambda *a: execs.append(a))
    monkeypatch.setenv("LEOFLOW_DAG_ID", "report")
    monkeypatch.setenv("LEOFLOW_TASK_ID", "build")
    monkeypatch.setenv("LEOFLOW_TS", "2026-06-14T05:30:00Z")
    monkeypatch.setenv("LEOFLOW_RUN_CONF", '{"table": "orders"}')
    runner.run_bash("echo {{ ds_nodash }} {{ dag.dag_id }}.{{ task.task_id }} "
                    "{{ dag_run.conf.table }}")
    assert execs == [("bash", ["bash", "-c", "echo 20260614 report.build orders"])]


def test_run_task_failure_callback_gets_the_exception(tmp_path, monkeypatch):
    # Airflow hands on_failure_callback the error as context["exception"] (#1843).
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(tmp_path / "rv.json"))
    monkeypatch.setenv("LEOFLOW_ON_FAILURE_CALLBACK", "1")
    body = ("SEEN = []\n"
            "def _cb(context):\n"
            "    SEEN.append(repr(context.get('exception')))\n"
            "def task():\n"
            "    raise ValueError('bad row')\n"
            "task.on_failure_callback = _cb\n")
    mod = _write_module(tmp_path, monkeypatch, body)

    with pytest.raises(ValueError):
        runner.run(f"{mod}:task")

    assert sys.modules[mod].SEEN == ["ValueError('bad row')"]


def test_operator_context_macros_prefer_the_task_sdk(monkeypatch):
    # When the Task SDK is installed (the base image), macros are its own module, so
    # templates behave as on Airflow; the built-in subset is only the fallback.
    fake = types.ModuleType("airflow.sdk.execution_time.macros")
    fake.ds_add = lambda ds, days: f"sdk:{ds}:{days}"
    monkeypatch.setitem(sys.modules, "airflow.sdk.execution_time.macros", fake)
    assert runner._operator_context()["macros"].ds_add("2026-06-14", 1) == "sdk:2026-06-14:1"


def test_operator_context_macros_fall_back_without_the_sdk(monkeypatch):
    monkeypatch.setitem(sys.modules, "airflow.sdk.execution_time.macros", None)
    macros = runner._operator_context()["macros"]
    assert macros.ds_add("2026-06-14", -14) == "2026-05-31"
    assert macros.ds_format("2026-06-14", "%Y-%m-%d", "%d/%m/%Y") == "14/06/2026"


def test_fallback_macros_export_the_task_sdk_modules():
    # Templates reach the standard modules through macros, as with the Task SDK,
    # so the fallback lists them as its public names, not as stray imports.
    from leoflow_runtime import macros

    for name in ("datetime", "json", "random", "time", "timedelta", "uuid"):
        assert name in macros.__all__
        assert getattr(macros, name) is not None


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
    assert "[dexaflow] pulled raw" in stdout
    assert "(26 B)" in stdout  # the JSON-encoded secret has 26 bytes


def test_run_logs_xcom_pulls_with_size(tmp_path, monkeypatch, capsys):
    """When an upstream XCom is consumed, the runtime MUST log it with the wire
    size — invaluable when debugging "received None" in a downstream task
    (was the upstream silent? did the name match? how big is the payload?).
    The line goes inside the Pre task execution group with the [dexaflow]
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
    # Each pulled XCom gets its own [dexaflow] pulled line with byte size.
    assert "[dexaflow] pulled raw (10 B)" in stdout, f"missing raw pull line:\n{stdout}"
    assert "[dexaflow] pulled summary (4 B)" in stdout, f"missing summary pull line:\n{stdout}"


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
    loading = stdout.find("[dexaflow] loading")
    user_line = stdout.find("USER_LINE_INSIDE_TASK")
    returned = stdout.find("[dexaflow] returned")
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


def test_run_overridden_task_runs_the_function_under_its_own_task_id(tmp_path, monkeypatch):
    """``estimate.override(task_id="estimate_03")(3)`` (#1681): the parser emits the
    entrypoint ``module:estimate`` and the task_id ``estimate_03``. Importing the
    module runs the overrides again (as Airflow does), and the runner still
    resolves the module-level decorator, unwraps it and runs the function under
    the task_id the agent sets, with the literal captured for that task."""
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    monkeypatch.setenv("LEOFLOW_TASK_ID", "estimate_03")
    monkeypatch.setenv("LEOFLOW_CALL_ARGS_JSON", json.dumps({"worker": 3}))
    body = (
        "class _TaskDecorator:\n"
        "    def __init__(self, fn, **kwargs):\n"
        "        self.function, self.kwargs = fn, kwargs\n"
        "    def override(self, **kwargs):\n"
        "        return _TaskDecorator(self.function, **{**self.kwargs, **kwargs})\n"
        "    def __call__(self, *a, **k):\n"
        "        return object()  # an XComArg, never the result\n"
        "def _estimate(worker, ti=None):\n"
        "    return {'worker': worker, 'task_id': ti.task_id}\n"
        "estimate = _TaskDecorator(_estimate)\n"
        "_ = [estimate.override(task_id=f'estimate_{n:02d}')(n) for n in range(4)]\n"
    )
    mod = _write_module(tmp_path, monkeypatch, body)

    runner.run(f"{mod}:estimate")

    assert json.loads(out.read_text()) == {"worker": 3, "task_id": "estimate_03"}


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


def test_run_operator_real_operator_renders_sdk_macros(tmp_path, monkeypatch):
    """With the real Task SDK, macros resolve to its own module (#1843)."""
    pytest.importorskip("airflow.providers.standard.operators.bash")
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(tmp_path / "ret.json"))
    monkeypatch.setenv("LEOFLOW_TASK_ID", "t")
    monkeypatch.setenv("LEOFLOW_DS", "2026-06-14")
    monkeypatch.setenv("LEOFLOW_TS", "2026-06-14T05:30:00Z")
    runner.run_operator(
        "airflow.providers.standard.operators.bash.BashOperator",
        {"bash_command": "echo {{ macros.ds_add(ds, 1) }} {{ logical_date.year }}"},
    )
    assert json.loads((tmp_path / "ret.json").read_text()) == "2026-06-15 2026"


def test_run_operator_real_operator_render_error_names_field(tmp_path, monkeypatch, capsys):
    """With the real Task SDK templater, a field that cannot render fails the
    attempt before execute(), naming the field (#1845)."""
    pytest.importorskip("airflow.providers.standard.operators.bash")
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(tmp_path / "ret.json"))
    monkeypatch.setenv("LEOFLOW_TASK_ID", "t")
    with pytest.raises(runner.TemplateRenderError) as info:
        runner.run_operator(
            "airflow.providers.standard.operators.bash.BashOperator",
            {"bash_command": "echo {{ nope.x }}"},
        )
    assert info.value.field == "bash_command"
    assert not (tmp_path / "ret.json").exists()
    assert "'nope' is undefined" in capsys.readouterr().out


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
