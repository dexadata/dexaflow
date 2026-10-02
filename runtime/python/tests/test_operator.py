"""Airflow-free tests for the generic operator runner (ADR 0040 Phase A).

These exercise ``runner.run_operator`` and the ``--operator`` CLI dispatch with a
fake operator class (no real Airflow), so the operator execution path is covered
in CI where ``apache-airflow`` is intentionally absent.
"""

import itertools
import json
import sys
import types

import pytest

from leoflow_runtime import __main__, runner

# Mirrors _RESCHEDULE_AT in the fake module below (datetime(2099,1,2,3,4,5, UTC));
# the runtime writes its .isoformat() to LEOFLOW_RESCHEDULE_PATH.
_RESCHEDULE_AT_ISO = "2099-01-02T03:04:05+00:00"


def _fake_airflow_links(monkeypatch):
    """Inject the minimal Airflow surface the generic link path imports
    (TaskInstanceKey + BaseXCom), so the provider-agnostic get_link path can be
    exercised without installing Airflow (#379)."""
    from collections import namedtuple
    for name in ("airflow", "airflow.models", "airflow.sdk", "airflow.sdk.bases"):
        monkeypatch.setitem(sys.modules, name, types.ModuleType(name))
    tik = types.ModuleType("airflow.models.taskinstancekey")
    tik.TaskInstanceKey = namedtuple(
        "TaskInstanceKey", "dag_id task_id run_id try_number map_index")
    monkeypatch.setitem(sys.modules, "airflow.models.taskinstancekey", tik)
    xc = types.ModuleType("airflow.sdk.bases.xcom")

    class BaseXCom:
        @staticmethod
        def get_value(*, ti_key, key):
            return None

    xc.BaseXCom = BaseXCom
    monkeypatch.setitem(sys.modules, "airflow.sdk.bases.xcom", xc)

_counter = itertools.count()

# A fake operator module: each class mimics the surface run_operator touches
# (``__init__(task_id, **kwargs)`` → ``render_template_fields`` → ``execute``),
# so we can drive every branch without installing Airflow.
_FAKE_OPERATOR_MODULE = '''
from datetime import datetime, timezone


class EchoOperator:
    mode = None

    def __init__(self, task_id, **kwargs):
        self.task_id = task_id
        self.kwargs = kwargs
        self.rendered = False

    def render_template_fields(self, context):
        self.rendered = True

    def execute(self, context):
        return {"task_id": self.task_id, "kwargs": self.kwargs}


class ReadyRescheduleSensor(EchoOperator):
    # mode='reschedule' alone no longer blanket-rejects (ADR 0040 Phase B): a sensor
    # whose poke is immediately ready just runs and succeeds, like any operator.
    mode = "reschedule"


class RenderBoomOperator(EchoOperator):
    def render_template_fields(self, context):
        raise ValueError("cannot render")


class NonJsonOperator(EchoOperator):
    def execute(self, context):
        return object()  # not JSON-serializable -> repr fallback


# Named exactly as Airflow's exception: run_operator matches it by class name
# across the MRO (no Airflow import). It carries reschedule_date like the real one.
class AirflowRescheduleException(Exception):
    def __init__(self, reschedule_date):
        super().__init__(str(reschedule_date))
        self.reschedule_date = reschedule_date


# A fixed future time the sensor asks to be rescheduled to.
_RESCHEDULE_AT = datetime(2099, 1, 2, 3, 4, 5, tzinfo=timezone.utc)


class LateRescheduleSensor(EchoOperator):
    mode = "reschedule"

    def execute(self, context):
        raise AirflowRescheduleException(_RESCHEDULE_AT)


# Named exactly as Airflow's exception: a deferrable operator suspends itself by
# raising TaskDeferred from execute(). Dexaflow has no triggerer (Phase C), so the
# runtime must translate it into a clear "set deferrable=False" message.
class TaskDeferred(Exception):
    pass


class DeferringOperator(EchoOperator):
    def execute(self, context):
        raise TaskDeferred("deferring to a trigger")


class LinkPersistOperator(EchoOperator):
    """Mirrors the provider operator-link persist pattern: almost every Google/AWS
    operator calls context["ti"].xcom_push(...) at the end of execute() (airflow
    .../links/base.py), and some read context["ti"].try_number. With ti=None this
    crashed AFTER the operator's real side effect."""

    def execute(self, context):
        context["ti"].xcom_push(key="my_link", value={"region": "us"})
        assert context["task_instance"] is context["ti"]
        return {"task_id": self.task_id, "try_number": context["ti"].try_number}


class _ConsoleLink:
    """A provider operator_extra_link, shaped like Airflow's BaseGoogleLink: a key it
    persists params under, and _format_link that fills format_str (relative -> prefixed
    with the console base)."""
    name = "Open Console"
    key = "console"
    format_str = "/x?p={project}&j={job}"

    def _format_link(self, **kwargs):
        s = self.format_str.format(**kwargs)
        return s if s.startswith("http") else "https://console.example.com" + s


class ExtraLinkOperator(EchoOperator):
    """An operator that declares an operator_extra_link and persists its params during
    execute() — exactly how Google/AWS operators expose their UI deep-link buttons."""
    operator_extra_links = (_ConsoleLink(),)

    def execute(self, context):
        context["ti"].xcom_push(key="console", value={"project": "p1", "job": "j1"})
        return {"ok": True}


class _GenericLink:
    """A provider-agnostic link, shaped like Airflow's generic BaseOperatorLink: no
    _format_link, only get_link(op, ti_key) reading BaseXCom.get_value — the path
    Amazon/Azure/etc. use (#379)."""
    name = "Generic Console"

    def get_link(self, operator, *, ti_key):
        from airflow.sdk.bases.xcom import BaseXCom
        params = BaseXCom.get_value(key="genlink", ti_key=ti_key)
        return ("https://x.example.com/" + params["id"]) if isinstance(params, dict) else ""


class GenericLinkOperator(EchoOperator):
    operator_extra_links = (_GenericLink(),)

    def execute(self, context):
        context["ti"].xcom_push(key="genlink", value={"id": "42"})
        return {"ok": True}


class MultiKeyOperator(EchoOperator):
    """Pushes a custom-keyed XCom during execute (besides its return value) — the
    multi-key XCom pattern the runtime ships so it is stored/visible (#XCOM)."""

    def execute(self, context):
        context["ti"].xcom_push(key="row_count", value=42)
        return {"ok": True}
'''


def _write_operator_module(tmp_path, monkeypatch) -> str:
    name = f"fakeops_{next(_counter)}"
    (tmp_path / f"{name}.py").write_text(_FAKE_OPERATOR_MODULE)
    monkeypatch.syspath_prepend(str(tmp_path))
    return name


def test_run_operator_executes_and_writes_return(tmp_path, monkeypatch):
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    monkeypatch.setenv("LEOFLOW_TASK_ID", "load")
    mod = _write_operator_module(tmp_path, monkeypatch)

    runner.run_operator(f"{mod}.EchoOperator", {"bucket": "b1"})

    written = json.loads(out.read_text())
    assert written == {"task_id": "load", "kwargs": {"bucket": "b1"}}


def test_run_operator_requires_dotted_class():
    with pytest.raises(ValueError, match="dotted"):
        runner.run_operator("NotDotted", {})


def test_run_operator_reschedule_mode_ready_succeeds(tmp_path, monkeypatch):
    # mode='reschedule' no longer blanket-rejects (ADR 0040 Phase B): a sensor whose
    # poke is immediately ready runs and returns like any operator.
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    mod = _write_operator_module(tmp_path, monkeypatch)
    runner.run_operator(f"{mod}.ReadyRescheduleSensor", {})
    assert out.exists()


def test_run_operator_render_template_fields_is_best_effort(tmp_path, monkeypatch, capsys):
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    mod = _write_operator_module(tmp_path, monkeypatch)

    # A render failure must not fail the task — execute still runs.
    runner.run_operator(f"{mod}.RenderBoomOperator", {})

    assert out.exists()
    assert "render_template_fields skipped" in capsys.readouterr().out


def test_run_operator_reschedule_signals_when_wired(tmp_path, monkeypatch):
    # A reschedule poke-not-ready, with the agent's LEOFLOW_RESCHEDULE_PATH wired:
    # write the requested reschedule time and exit with the reschedule sentinel so
    # the agent reports up_for_reschedule, releasing the pod (ADR 0040 Phase B, #380).
    rp = tmp_path / "reschedule.txt"
    monkeypatch.setenv("LEOFLOW_RESCHEDULE_PATH", str(rp))
    mod = _write_operator_module(tmp_path, monkeypatch)
    with pytest.raises(SystemExit) as exc:
        runner.run_operator(f"{mod}.LateRescheduleSensor", {})
    assert exc.value.code == runner.RESCHEDULE_EXIT_CODE
    assert rp.read_text().strip() == _RESCHEDULE_AT_ISO


def test_run_operator_reschedule_rejects_when_not_wired(tmp_path, monkeypatch):
    # Without LEOFLOW_RESCHEDULE_PATH (an older agent), a reschedule request fails
    # with a clear error rather than a silent sentinel exit.
    monkeypatch.delenv("LEOFLOW_RESCHEDULE_PATH", raising=False)
    mod = _write_operator_module(tmp_path, monkeypatch)
    with pytest.raises(RuntimeError, match="reschedule"):
        runner.run_operator(f"{mod}.LateRescheduleSensor", {})


def test_run_operator_translates_deferral(tmp_path, monkeypatch):
    # A deferrable operator (deferrable=True) raises TaskDeferred. With no
    # triggerer (Phase C), the runtime must fail with a clear, actionable message
    # naming deferrable — not leak a raw TaskDeferred traceback.
    mod = _write_operator_module(tmp_path, monkeypatch)
    with pytest.raises(RuntimeError, match="deferrable"):
        runner.run_operator(f"{mod}.DeferringOperator", {})


def test_run_operator_merges_upstream_xcom(tmp_path, monkeypatch):
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    monkeypatch.setenv("LEOFLOW_XCOM_SQL", '"SELECT 1"')
    mod = _write_operator_module(tmp_path, monkeypatch)

    runner.run_operator(f"{mod}.EchoOperator", {"bucket": "b1"})

    written = json.loads(out.read_text())
    # The upstream XCom is delivered as the ``sql`` kwarg alongside the literal.
    assert written["kwargs"] == {"bucket": "b1", "sql": "SELECT 1"}


def test_run_operator_stringifies_non_json_return(tmp_path, monkeypatch):
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    mod = _write_operator_module(tmp_path, monkeypatch)

    runner.run_operator(f"{mod}.NonJsonOperator", {})

    # A non-serializable return is stringified (repr) rather than failing.
    assert json.loads(out.read_text()).startswith("<object object")


def test_run_operator_tolerates_ti_xcom_push_and_try_number(tmp_path, monkeypatch):
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    monkeypatch.setenv("LEOFLOW_TASK_ID", "invoke")
    monkeypatch.setenv("LEOFLOW_TRY_NUMBER", "2")
    mod = _write_operator_module(tmp_path, monkeypatch)

    # 43 of the google provider's operators persist a UI link via
    # context["ti"].xcom_push(...) at the end of execute(). With ti=None this
    # crashed AFTER the real side effect (the worst failure — double-exec on
    # retry). The standalone context must provide a tolerant ti.
    runner.run_operator(f"{mod}.LinkPersistOperator", {})

    assert json.loads(out.read_text()) == {"task_id": "invoke", "try_number": 2}


def test_operator_context_provides_tolerant_ti(monkeypatch):
    monkeypatch.setenv("LEOFLOW_TASK_ID", "t1")
    monkeypatch.setenv("LEOFLOW_TRY_NUMBER", "3")
    ti = runner._operator_context()["ti"]
    assert ti is not None
    assert ti.xcom_push(key="k", value=1) is None  # no-op, no crash
    assert ti.xcom_pull(key="k") is None
    assert ti.try_number == 3 and ti.task_id == "t1"


def test_ti_xcom_pull_resolves_upstream_return_value(monkeypatch):
    # Like Airflow: ti.xcom_pull('compile') returns the upstream's real
    # return_value, so chained operators (compile >> invoke) can pass data. The
    # agent delivers the upstream return_values as the UPSTREAM_XCOM_ENV map.
    monkeypatch.setenv(runner.UPSTREAM_XCOM_ENV, json.dumps({
        "compile": {"name": "projects/p/compilationResults/abc"},
        "extract": [1, 2, 3],
    }))
    ti = runner._operator_context()["ti"]
    assert ti.xcom_pull("compile") == {"name": "projects/p/compilationResults/abc"}
    assert ti.xcom_pull(task_ids="compile", key="return_value")["name"].endswith("abc")
    assert ti.xcom_pull(task_ids=["compile", "extract"]) == [
        {"name": "projects/p/compilationResults/abc"}, [1, 2, 3]]
    assert ti.xcom_pull("missing") is None      # unknown upstream -> None
    assert ti.xcom_pull("compile", key="custom") is None  # only return_value is carried


def test_run_operator_does_not_inject_xcom_pull_map_as_kwarg(tmp_path, monkeypatch):
    # The upstream-xcom map exposed to ti.xcom_pull must NOT be mistaken by
    # _merge_operator_xcom for a param-bound LEOFLOW_XCOM_<PARAM> and injected as an
    # operator constructor kwarg — that crashes the operator with an unexpected
    # `by_task=` arg. (Caught by the live GCP compile >> invoke chain test.)
    out = tmp_path / "rv.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    monkeypatch.setenv(runner.UPSTREAM_XCOM_ENV, json.dumps({"compile": {"name": "x"}}))
    mod = _write_operator_module(tmp_path, monkeypatch)

    runner.run_operator(f"{mod}.EchoOperator", {"bucket": "b1"})

    assert json.loads(out.read_text())["kwargs"] == {"bucket": "b1"}


def test_run_operator_writes_extra_links(tmp_path, monkeypatch):
    # Operators expose UI deep-link buttons (operator_extra_links) by persisting
    # params via ti.xcom_push during execute(). The runtime — which has Airflow in
    # the pod, unlike the Go control plane — computes each link's URL and writes it
    # out for the agent to ship back, so the UI can render the "open in <provider>"
    # button (ADR 0040, #375).
    out = tmp_path / "rv.json"
    links = tmp_path / "links.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(out))
    monkeypatch.setenv("LEOFLOW_EXTRA_LINKS_PATH", str(links))
    mod = _write_operator_module(tmp_path, monkeypatch)

    runner.run_operator(f"{mod}.ExtraLinkOperator", {})

    assert json.loads(links.read_text()) == {"Open Console": "https://console.example.com/x?p=p1&j=j1"}


def test_run_operator_generic_extra_links(tmp_path, monkeypatch):
    # The generic path (used in the pod for every provider): reuse each link's own
    # get_link, bridging BaseXCom to the captured ti.xcom_push params — no metastore,
    # no Google-specific format_str (#379). Validated live against GCP; here via a
    # faked-Airflow generic link so it is covered without installing Airflow.
    _fake_airflow_links(monkeypatch)
    links = tmp_path / "links.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(tmp_path / "rv.json"))
    monkeypatch.setenv("LEOFLOW_EXTRA_LINKS_PATH", str(links))
    monkeypatch.setenv("LEOFLOW_TASK_ID", "t")
    monkeypatch.setenv("LEOFLOW_DAG_ID", "d")
    monkeypatch.setenv("LEOFLOW_RUN_ID", "r")
    mod = _write_operator_module(tmp_path, monkeypatch)

    runner.run_operator(f"{mod}.GenericLinkOperator", {})

    assert json.loads(links.read_text()) == {"Generic Console": "https://x.example.com/42"}


def test_run_operator_writes_custom_xcom_pushes(tmp_path, monkeypatch):
    # Custom-keyed ti.xcom_push during execute is shipped (besides return_value) so the
    # control plane stores it and it shows in the XCom tab — Airflow parity for operator
    # XComs. return_value is excluded (it travels via _write_return).
    pushes = tmp_path / "pushes.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(tmp_path / "rv.json"))
    monkeypatch.setenv("LEOFLOW_PUSHES_PATH", str(pushes))
    mod = _write_operator_module(tmp_path, monkeypatch)

    runner.run_operator(f"{mod}.MultiKeyOperator", {})

    assert json.loads(pushes.read_text()) == {"row_count": 42}


def test_run_operator_no_custom_pushes_writes_nothing(tmp_path, monkeypatch):
    pushes = tmp_path / "pushes.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(tmp_path / "rv.json"))
    monkeypatch.setenv("LEOFLOW_PUSHES_PATH", str(pushes))
    mod = _write_operator_module(tmp_path, monkeypatch)

    runner.run_operator(f"{mod}.EchoOperator", {"bucket": "b1"})

    assert not pushes.exists()  # no custom xcom_push -> no file


def test_run_operator_no_extra_links_writes_nothing(tmp_path, monkeypatch):
    links = tmp_path / "links.json"
    monkeypatch.setenv("LEOFLOW_RETURN_VALUE_PATH", str(tmp_path / "rv.json"))
    monkeypatch.setenv("LEOFLOW_EXTRA_LINKS_PATH", str(links))
    mod = _write_operator_module(tmp_path, monkeypatch)

    runner.run_operator(f"{mod}.EchoOperator", {"bucket": "b1"})

    assert not links.exists()  # an operator with no extra links writes no file


def test_operator_context_injects_var_conn_accessors(monkeypatch):
    # The context exposes Airflow's var/conn template accessors so {{ var.value.X }} /
    # {{ var.json.X }} / {{ conn.X }} resolve from the AIRFLOW_VAR_*/AIRFLOW_CONN_* env
    # the agent delivers. Reuses Airflow's own accessor classes; here faked so the wiring
    # is covered without installing Airflow.
    m = types.ModuleType("airflow.sdk.execution_time.context")

    class VariableAccessor:
        def __init__(self, deserialize_json):
            self.deserialize_json = deserialize_json

    class ConnectionAccessor:
        pass

    m.VariableAccessor = VariableAccessor
    m.ConnectionAccessor = ConnectionAccessor
    for n in ("airflow", "airflow.sdk", "airflow.sdk.execution_time"):
        monkeypatch.setitem(sys.modules, n, types.ModuleType(n))
    monkeypatch.setitem(sys.modules, "airflow.sdk.execution_time.context", m)

    ctx = runner._operator_context()
    assert set(ctx["var"]) == {"value", "json"}
    assert ctx["var"]["value"].deserialize_json is False
    assert ctx["var"]["json"].deserialize_json is True
    assert isinstance(ctx["conn"], ConnectionAccessor)


def test_operator_context_no_accessors_without_airflow(monkeypatch):
    # Force both accessor imports to fail (sys.modules[...] = None) so the graceful-absence
    # path is covered deterministically, whether or not Airflow is installed in the dev env.
    monkeypatch.setitem(sys.modules, "airflow.sdk.execution_time.context", None)
    monkeypatch.setitem(sys.modules, "airflow.utils.context", None)
    ctx = runner._operator_context()
    assert "var" not in ctx and "conn" not in ctx


def test_operator_context_exposes_data_interval(monkeypatch):
    # The DagRun's data interval the agent stamps (RFC3339) is exposed as datetimes,
    # so operators that filter by interval (and {{ data_interval_start }} templates)
    # get real values (ADR 0040).
    monkeypatch.setenv("LEOFLOW_DATA_INTERVAL_START", "2026-06-13T00:00:00Z")
    monkeypatch.setenv("LEOFLOW_DATA_INTERVAL_END", "2026-06-14T06:30:00Z")
    ctx = runner._operator_context()
    assert ctx["data_interval_start"].day == 13
    assert ctx["data_interval_end"].hour == 6 and ctx["data_interval_end"].day == 14


def test_operator_context_data_interval_unset_is_none(monkeypatch):
    monkeypatch.delenv("LEOFLOW_DATA_INTERVAL_START", raising=False)
    monkeypatch.delenv("LEOFLOW_DATA_INTERVAL_END", raising=False)
    ctx = runner._operator_context()
    assert ctx["data_interval_start"] is None and ctx["data_interval_end"] is None


def test_operator_context_parses_params(monkeypatch):
    monkeypatch.setenv("LEOFLOW_PARAMS", '{"a": 1}')
    monkeypatch.setenv("LEOFLOW_DS", "2026-06-09")
    ctx = runner._operator_context()
    assert ctx["params"] == {"a": 1}
    assert ctx["ds"] == "2026-06-09"


def test_operator_context_ignores_malformed_params(monkeypatch):
    monkeypatch.setenv("LEOFLOW_PARAMS", "{not json")
    assert runner._operator_context()["params"] == {}


def test_operator_context_non_dict_params_coerced(monkeypatch):
    # Valid JSON that is not an object (e.g. a conf of "null") must still yield a dict,
    # so operators that do context["params"].get(...) never crash (#148).
    monkeypatch.setenv("LEOFLOW_PARAMS", "null")
    assert runner._operator_context()["params"] == {}
    monkeypatch.setenv("LEOFLOW_PARAMS", "[1, 2]")
    assert runner._operator_context()["params"] == {}


def test_main_operator_dispatch(monkeypatch):
    calls = {}

    def _capture(cls, args):
        calls.update(cls=cls, args=args)

    monkeypatch.setattr(__main__, "run_operator", _capture)
    monkeypatch.setenv("LEOFLOW_OPERATOR_ARGS", '{"bucket": "b1"}')

    assert __main__.main(["--operator", "pkg.mod.MyOperator"]) == 0
    assert calls == {"cls": "pkg.mod.MyOperator", "args": {"bucket": "b1"}}


def test_main_operator_rejects_bad_args_json(monkeypatch, capsys):
    monkeypatch.setattr(__main__, "run_operator", lambda cls, args: pytest.fail("should not run"))
    monkeypatch.setenv("LEOFLOW_OPERATOR_ARGS", "{not json")

    assert __main__.main(["--operator", "pkg.mod.MyOperator"]) == 2
    assert "invalid LEOFLOW_OPERATOR_ARGS JSON" in capsys.readouterr().err
