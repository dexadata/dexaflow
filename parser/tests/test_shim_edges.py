"""Edge cases the 13 example goldens don't cover — found during the Phase 1/2
review (ADR 0024). Each guards a fidelity gap in the structural shim.
"""
from __future__ import annotations

import json
import textwrap
from pathlib import Path

import pytest

from leoflow_parser.compiler import compile_dag


def _compile(monkeypatch, tmp_path: Path, body: str) -> dict:
    monkeypatch.setenv(
        "LEOFLOW_PROJECT_CONFIG_JSON",
        json.dumps({"schema_version": "1.0"}),
    )
    src = tmp_path / "dag.py"
    src.write_text(textwrap.dedent(body))
    return compile_dag(str(src), str(tmp_path / "ignored.json"), "img:v1")


def _task(spec: dict, task_id: str) -> dict:
    return next(t for t in spec["tasks"] if t["task_id"] == task_id)


def test_task_decorator_trigger_rule_is_preserved(monkeypatch, tmp_path):
    """@task(trigger_rule=…) must reach the dag.json (was silently dropped)."""
    spec = _compile(monkeypatch, tmp_path,"""
        from airflow.sdk import DAG, task
        @task(trigger_rule="all_done")
        def a() -> None: ...
        @task
        def b() -> None: ...
        with DAG("g"):
            a() >> b()
    """)
    assert _task(spec, "a").get("trigger_rule") == "all_done"


def test_operator_attached_via_dag_kwarg_without_context(monkeypatch, tmp_path):
    """BashOperator(dag=dag) outside a `with` block is still collected."""
    spec = _compile(monkeypatch, tmp_path,"""
        from airflow.sdk import DAG
        from airflow.providers.standard.operators.bash import BashOperator
        dag = DAG("g")
        BashOperator(task_id="t", bash_command="echo hi", dag=dag)
    """)
    assert spec["dag_id"] == "g"
    assert _task(spec, "t")["type"] == "bash"


def test_sibling_module_import_resolves(monkeypatch, tmp_path):
    """A DAG importing a helper module next to it must compile (Airflow's DagBag
    puts the DAG folder on sys.path; the shim loader must too)."""
    (tmp_path / "helpers.py").write_text("def names():\n    return ['x', 'y', 'z']\n")
    spec = _compile(monkeypatch, tmp_path,"""
        from airflow.sdk import DAG, task
        from helpers import names
        @task
        def step() -> None: ...
        with DAG("g"):
            for _ in names():
                step()
    """)
    assert len(spec["tasks"]) == 3  # one per helper-provided id (deduped)


def test_sibling_modules_are_isolated_between_compiles(monkeypatch, tmp_path_factory):
    """Two DAGs in different dirs with a same-named helper must not bleed state."""
    d1 = tmp_path_factory.mktemp("one")
    d2 = tmp_path_factory.mktemp("two")
    (d1 / "shared.py").write_text("N = 1\n")
    (d2 / "shared.py").write_text("N = 4\n")
    for d in (d1, d2):
        (d / "dag.py").write_text(
            "from airflow.sdk import DAG, task\n"
            "from shared import N\n"
            "@task\ndef t() -> None: ...\n"
            "with DAG('g'):\n"
            "    [t() for _ in range(N)]\n"
        )
    from leoflow_parser.compiler import compile_dag
    monkeypatch.setenv("LEOFLOW_PROJECT_CONFIG_JSON", json.dumps({"schema_version": "1.0"}))
    s1 = compile_dag(str(d1 / "dag.py"), str(d1 / "ignored.json"), "x:v")
    s2 = compile_dag(str(d2 / "dag.py"), str(d2 / "ignored.json"), "x:v")
    assert len(s1["tasks"]) == 1 and len(s2["tasks"]) == 4


def test_missing_sdk_helper_gives_clear_unsupported_error(monkeypatch, tmp_path):
    """`from airflow.sdk import chain` (a name the shim lacks) is a clear error,
    not a raw ImportError."""
    with pytest.raises(ValueError) as ei:
        _compile(monkeypatch, tmp_path, """
            from airflow.sdk import DAG, task, chain
            @task
            def a() -> None: ...
            with DAG("g"):
                a()
        """)
    assert "not supported by Dexaflow" in str(ei.value)


def test_generic_provider_operator_is_captured(monkeypatch, tmp_path):
    """A top-level provider OPERATOR (the task itself) is captured generically
    instead of rejected: the shim synthesizes the operator class, the parser emits
    type=airflow_operator with the real class path + the constructor kwargs, which
    the runtime later instantiates and executes (ADR 0040 Phase A)."""
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG
        from airflow.providers.snowflake.operators.snowflake import SQLExecuteQueryOperator
        with DAG("g"):
            SQLExecuteQueryOperator(task_id="q", conn_id="sf", sql="SELECT 1")
    """)
    t = next(x for x in spec["tasks"] if x["task_id"] == "q")
    assert t["type"] == "airflow_operator"
    assert t["operator_class"] == \
        "airflow.providers.snowflake.operators.snowflake.SQLExecuteQueryOperator"
    assert t["operator_args"]["conn_id"] == "sf"
    assert t["operator_args"]["sql"] == "SELECT 1"


def test_generic_provider_sensor_is_captured(monkeypatch, tmp_path):
    """A provider SENSOR is captured too, carrying its poke mode (ADR 0040)."""
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG
        from airflow.providers.amazon.aws.sensors.s3 import S3KeySensor
        with DAG("g"):
            S3KeySensor(task_id="wait", bucket_key="k", bucket_name="b", mode="poke")
    """)
    t = next(x for x in spec["tasks"] if x["task_id"] == "wait")
    assert t["type"] == "airflow_operator"
    assert t["operator_class"] == "airflow.providers.amazon.aws.sensors.s3.S3KeySensor"
    assert t["operator_args"]["bucket_key"] == "k"


def test_top_level_provider_import_gives_actionable_message(monkeypatch, tmp_path):
    """A provider hook imported at DAG module top-level fails the parse (the
    parser has no providers installed). The message must NOT claim the provider
    is categorically unsupported (it IS supported at runtime via connectors:);
    it must name the provider, tell the user to import inside the @task body, and
    point at connectors:/dependencies:. ADR 0038 #2."""
    with pytest.raises(ValueError) as ei:
        _compile(monkeypatch, tmp_path, """
            from airflow.sdk import DAG, task
            from airflow.providers.postgres.hooks.postgres import PostgresHook
            @task
            def a() -> None: ...
            with DAG("g"):
                a()
        """)
    msg = str(ei.value)
    assert "airflow.providers.postgres" in msg
    assert "connectors:" in msg
    assert "@task" in msg
    # It must NOT fall through to the generic operator-unsupported wording.
    assert "supported: Bash, Http" not in msg


def test_operator_arg_bound_to_upstream_becomes_xcom_input(monkeypatch, tmp_path):
    """A generic-operator arg set to an upstream task's output is wired as an
    xcom_input (ADR 0040 A1.1), not silently dropped; sibling literals still
    land in operator_args."""
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, task
        from airflow.providers.snowflake.operators.snowflake import SQLExecuteQueryOperator
        @task
        def make_sql() -> str: ...
        with DAG("g"):
            s = make_sql()
            SQLExecuteQueryOperator(task_id="q", conn_id="sf", sql=s)
    """)
    q = _task(spec, "q")
    assert q["xcom_input"] == {"sql": ["make_sql"]}
    assert "sql" not in q.get("operator_args", {})
    assert q["operator_args"]["conn_id"] == "sf"


def test_operator_non_serialisable_arg_is_loud_reject(monkeypatch, tmp_path):
    """A non-JSON, non-XCom operator arg (e.g. a callable) is a loud compile
    error, not a silent drop (ADR 0040 A1.1)."""
    import pytest
    with pytest.raises(ValueError):
        _compile(monkeypatch, tmp_path, """
            from airflow.sdk import DAG
            from airflow.providers.snowflake.operators.snowflake import SQLExecuteQueryOperator
            with DAG("g"):
                SQLExecuteQueryOperator(task_id="q", conn_id="sf", sql="SELECT 1",
                                        on_success_callback=lambda ctx: None)
        """)


def test_operator_on_failure_callback_is_marked_not_rejected(monkeypatch, tmp_path):
    """on_failure_callback is a callable Dexaflow cannot serialise into dag.json,
    but instead of the general non-serialisable reject (ADR 0040 A1.1) it is
    ACCEPTED and marked on the task (#424 inc 4): the runtime re-imports dag.py and
    runs it in the task process on failure. The callable itself is not carried."""
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG
        from airflow.providers.snowflake.operators.snowflake import SQLExecuteQueryOperator
        def notify(context):
            pass
        with DAG("g"):
            SQLExecuteQueryOperator(task_id="q", conn_id="sf", sql="SELECT 1",
                                    on_failure_callback=notify)
    """)
    q = _task(spec, "q")
    assert q["type"] == "airflow_operator"
    assert q.get("on_failure_callback") is True
    # the callable must NOT leak into the serialised operator args
    assert "on_failure_callback" not in q.get("operator_args", {})


def test_python_task_on_failure_callback_is_marked(monkeypatch, tmp_path):
    """A native @task runs in Python (run() has an except), so on_failure_callback
    is supported and marked — same as a provider operator (#424)."""
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, task
        def notify(ctx): pass
        @task(on_failure_callback=notify)
        def a() -> None: ...
        with DAG("g"):
            a()
    """)
    a = _task(spec, "a")
    assert a["type"] == "python"
    assert a.get("on_failure_callback") is True


def test_bash_task_on_failure_callback_is_loud_reject(monkeypatch, tmp_path):
    """A bash task replaces its process with bash (os.execvp), so no Python is left
    to run a callback — refuse loud rather than silently drop it (#424)."""
    with pytest.raises(ValueError):
        _compile(monkeypatch, tmp_path, """
            from airflow.sdk import DAG
            from airflow.providers.standard.operators.bash import BashOperator
            def notify(ctx): pass
            with DAG("g"):
                BashOperator(task_id="b", bash_command="false", on_failure_callback=notify)
        """)


def test_native_task_on_success_callback_is_loud_reject(monkeypatch, tmp_path):
    """on_success/on_retry callbacks are unsupported everywhere; on a native task
    they must fail loud too (not be silently dropped)."""
    with pytest.raises(ValueError):
        _compile(monkeypatch, tmp_path, """
            from airflow.sdk import DAG
            from airflow.providers.standard.operators.bash import BashOperator
            def notify(ctx): pass
            with DAG("g"):
                BashOperator(task_id="b", bash_command="true", on_success_callback=notify)
        """)


def test_operator_on_success_callback_still_rejected(monkeypatch, tmp_path):
    """Only on_failure_callback is wired (#424 inc 4); other callback kwargs stay a
    loud reject so we never silently drop them."""
    with pytest.raises(ValueError):
        _compile(monkeypatch, tmp_path, """
            from airflow.sdk import DAG
            from airflow.providers.snowflake.operators.snowflake import SQLExecuteQueryOperator
            with DAG("g"):
                SQLExecuteQueryOperator(task_id="q", conn_id="sf", sql="SELECT 1",
                                        on_success_callback=lambda ctx: None)
        """)


def test_http_sensor_is_generic_operator_not_native_http(monkeypatch, tmp_path):
    """A SENSOR whose name contains 'Http' (HttpSensor) must be captured as a
    generic airflow_operator (poke in a pod), NOT mistranslated to the native
    http_api inline call. Regression for the substring fast-path (e2e finding)."""
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG
        from airflow.providers.http.sensors.http import HttpSensor
        with DAG("g"):
            HttpSensor(task_id="probe", http_conn_id="cp", endpoint="readyz")
    """)
    probe = _task(spec, "probe")
    assert probe["type"] == "airflow_operator"
    assert probe["operator_class"] == "airflow.providers.http.sensors.http.HttpSensor"


def test_http_operator_runs_in_pod_not_inline(monkeypatch, tmp_path):
    """HttpOperator runs through the generic pod executor (ADR 0047), NOT the
    native inline http_api path (the control-plane SSRF surface, H5). The author's
    DAG is unchanged; it compiles to type=airflow_operator with the real class."""
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG
        from airflow.providers.http.operators.http import HttpOperator
        with DAG("g"):
            HttpOperator(task_id="call", method="GET", endpoint="https://example.com/x")
    """)
    t = _task(spec, "call")
    assert t["type"] == "airflow_operator"
    assert t["operator_class"] == "airflow.providers.http.operators.http.HttpOperator"


@pytest.mark.parametrize("class_name", [
    "AcmePythonModelOperator",  # contains "Python"
    "AcmeBashStyleOperator",    # contains "Bash"
    "AcmeHttpCallOperator",     # contains "Http"
])
def test_generic_operator_with_native_substring_name_is_captured(
        monkeypatch, tmp_path, class_name):
    """A long-tail provider OPERATOR (captured by _generic) whose class name happens
    to contain Bash/Http/Python must route to airflow_operator — NOT the native task
    type the substring fast-path infers. The fast-path exists only for the bundled
    shim operators (which carry no __leoflow_operator_class__); routing a captured
    class natively would drop its operator_class and silently mistranslate it (same
    class of bug as the HttpSensor regression above)."""
    spec = _compile(monkeypatch, tmp_path, f"""
        from airflow.sdk import DAG
        from airflow.providers.acme.operators.run import {class_name}
        with DAG("g"):
            {class_name}(task_id="t")
    """)
    t = _task(spec, "t")
    assert t["type"] == "airflow_operator"
    assert t["operator_class"] == f"airflow.providers.acme.operators.run.{class_name}"


# --- Airflow scheduling attrs on the operator / default_args (#434) --------------
# retries / retry_delay / execution_timeout set the Airflow way must be captured, not
# silently dropped (a DAG migrated with retries=3 must not run once with no retry).

def test_operator_retries_and_timeouts_are_captured(monkeypatch, tmp_path):
    """retries / retry_delay / execution_timeout set ON the operator reach dag.json
    (converted to seconds), instead of being silently dropped (#434)."""
    spec = _compile(monkeypatch, tmp_path, """
        from datetime import timedelta
        from airflow.providers.standard.operators.bash import BashOperator
        from airflow.sdk import DAG
        with DAG("g"):
            BashOperator(task_id="b", bash_command="false",
                         retries=3, retry_delay=timedelta(seconds=30),
                         execution_timeout=timedelta(minutes=5))
    """)
    b = _task(spec, "b")
    assert b["retries"] == 3
    assert b["retry_delay_seconds"] == 30
    assert b["execution_timeout_seconds"] == 300


def test_dag_default_args_retries_are_captured(monkeypatch, tmp_path):
    """default_args on the DAG apply to a task that does not set its own (#434)."""
    spec = _compile(monkeypatch, tmp_path, """
        from datetime import timedelta
        from airflow.providers.standard.operators.bash import BashOperator
        from airflow.sdk import DAG
        with DAG("g", default_args={"retries": 2, "retry_delay": timedelta(seconds=15)}):
            BashOperator(task_id="b", bash_command="false")
    """)
    b = _task(spec, "b")
    assert b["retries"] == 2
    assert b["retry_delay_seconds"] == 15


def test_operator_retries_win_over_default_args(monkeypatch, tmp_path):
    """A per-operator value is more specific than default_args and wins (#434)."""
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.providers.standard.operators.bash import BashOperator
        from airflow.sdk import DAG
        with DAG("g", default_args={"retries": 2}):
            BashOperator(task_id="b", bash_command="false", retries=5)
    """)
    assert _task(spec, "b")["retries"] == 5


def test_no_scheduling_attrs_emits_no_keys(monkeypatch, tmp_path):
    """A task with no retries/timeout emits none — so the Go side's default_args /
    dexaflow.yaml defaults still apply (nil, not a forced 0)."""
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.providers.standard.operators.bash import BashOperator
        from airflow.sdk import DAG
        with DAG("g"):
            BashOperator(task_id="b", bash_command="false")
    """)
    b = _task(spec, "b")
    assert "retries" not in b
    assert "retry_delay_seconds" not in b
    assert "execution_timeout_seconds" not in b


def test_task_decorator_retries_are_captured(monkeypatch, tmp_path):
    """@task(retries=…, retry_delay=…) — the common TaskFlow style — is captured too
    (the decorator kwargs flow to the underlying python operator, #434)."""
    spec = _compile(monkeypatch, tmp_path, """
        from datetime import timedelta
        from airflow.sdk import DAG, task
        @task(retries=2, retry_delay=timedelta(seconds=10))
        def a() -> None: ...
        with DAG("g"):
            a()
    """)
    a = _task(spec, "a")
    assert a["type"] == "python"
    assert a["retries"] == 2
    assert a["retry_delay_seconds"] == 10


def test_provider_operator_retries_are_captured(monkeypatch, tmp_path):
    """A generic provider operator (type=airflow_operator) also carries its retries
    /timeout, so a captured operator is not exempt from the #434 fix."""
    spec = _compile(monkeypatch, tmp_path, """
        from datetime import timedelta
        from airflow.providers.snowflake.operators.snowflake import SQLExecuteQueryOperator
        from airflow.sdk import DAG
        with DAG("g"):
            SQLExecuteQueryOperator(task_id="q", conn_id="sf", sql="SELECT 1",
                                    retries=4, execution_timeout=timedelta(minutes=2))
    """)
    q = _task(spec, "q")
    assert q["type"] == "airflow_operator"
    assert q["retries"] == 4
    assert q["execution_timeout_seconds"] == 120


# --- oversized literal args exceed the env-var limit at dispatch (#149) -----------

def test_oversized_task_literal_arg_is_a_loud_compile_error(monkeypatch, tmp_path):
    """A @task bound to a huge literal rides as a single LEOFLOW_CALL_ARGS_JSON env
    var and would blow the POSIX per-var limit at dispatch — the compiler must
    reject it with an actionable message (#149)."""
    with pytest.raises(ValueError) as ei:
        _compile(monkeypatch, tmp_path, """
            from airflow.sdk import DAG, task
            @task
            def consume(rows) -> None: ...
            with DAG("g"):
                consume(["x" * 1024] * 200)   # ~200 KiB literal
        """)
    msg = str(ei.value)
    assert "consume" in msg                                   # names the task
    assert "call_args" in msg
    assert "limit" in msg.lower() or "exceed" in msg.lower()  # says it's too big
    assert "Connection" in msg or "external" in msg           # points at the fix


def test_small_task_literal_arg_compiles(monkeypatch, tmp_path):
    """A small literal is fine — the cap only rejects oversized payloads (#149)."""
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, task
        @task
        def consume(rows) -> None: ...
        with DAG("g"):
            consume([1, 2, 3])
    """)
    assert _task(spec, "consume")["call_args"] == {"rows": [1, 2, 3]}


def test_oversized_operator_arg_is_a_loud_compile_error(monkeypatch, tmp_path):
    """The same env-var limit applies to a generic operator's literal kwargs
    (LEOFLOW_OPERATOR_ARGS) — the cap covers operator_args too (#149)."""
    with pytest.raises(ValueError) as ei:
        _compile(monkeypatch, tmp_path, """
            from airflow.providers.snowflake.operators.snowflake import SQLExecuteQueryOperator
            from airflow.sdk import DAG
            with DAG("g"):
                SQLExecuteQueryOperator(task_id="q", conn_id="sf", sql="x" * (200 * 1024))
        """)
    msg = str(ei.value)
    assert "q" in msg and "operator_args" in msg
    assert "Connection" in msg or "external" in msg


def test_python_task_on_failure_callback_as_list_is_marked(monkeypatch, tmp_path):
    """Airflow 3 normalises a task's on_failure_callback to a LIST, so a DAG copied
    from a real Airflow deployment carries `[fn]`, not `fn`. The runtime already
    handles both (_normalize_callbacks, #442); the compiler gate must too, or the
    flag never reaches dag.json and the callback is dropped in silence — the exact
    outcome _check_callbacks' docstring promises will never happen (#470)."""
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, task
        def notify_a(ctx): pass
        def notify_b(ctx): pass
        @task(on_failure_callback=[notify_a, notify_b])
        def a() -> None: ...
        with DAG("g"):
            a()
    """)
    a = _task(spec, "a")
    assert a["type"] == "python"
    assert a.get("on_failure_callback") is True


def test_operator_on_failure_callback_as_list_is_marked(monkeypatch, tmp_path):
    """Same for a generically-captured provider operator, whose kwargs arrive via
    __leoflow_args__ rather than as attributes."""
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG
        from airflow.providers.snowflake.operators.snowflake import SQLExecuteQueryOperator
        def notify(context): pass
        with DAG("g"):
            SQLExecuteQueryOperator(task_id="q", conn_id="sf", sql="SELECT 1",
                                    on_failure_callback=[notify])
    """)
    q = _task(spec, "q")
    assert q.get("on_failure_callback") is True
    assert "on_failure_callback" not in q.get("operator_args", {})


def test_unsupported_callback_as_list_is_still_loudly_rejected(monkeypatch, tmp_path):
    """The same predicate guards the loud reject for callbacks Dexaflow does not
    support. In list form they slipped past it and were dropped silently, which is
    worse than the unsupported case it was written for: the author is told nothing
    while the error text promises they would be (ADR 0024, #470)."""
    with pytest.raises(ValueError, match="on_success_callback"):
        _compile(monkeypatch, tmp_path, """
            from airflow.sdk import DAG, task
            def notify(ctx): pass
            @task(on_success_callback=[notify])
            def a() -> None: ...
            with DAG("g"):
                a()
        """)


def test_empty_callback_list_is_not_marked(monkeypatch, tmp_path):
    """An empty list declares no callback. Marking it would make the runtime
    re-import dag.py on every failure to call nothing."""
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, task
        @task(on_failure_callback=[])
        def a() -> None: ...
        with DAG("g"):
            a()
    """)
    assert _task(spec, "a").get("on_failure_callback") is None


# ─── Airflow-3 canonical import spellings (import-alias compatibility) ───
# Airflow 3 kept `from airflow.decorators import task/dag` and
# `from airflow import DAG` as deprecated-but-valid spellings that map onto
# `airflow.sdk`. The shim only exposed `airflow.sdk`, so these line-1 imports
# were rejected. `airflow.operators.*`, by contrast, was REMOVED from core in
# 3.0 (relocated to apache-airflow-providers-standard): it must not be aliased,
# but the error must name the canonical providers.standard path.


def test_decorators_task_matches_sdk_task(monkeypatch, tmp_path):
    """`from airflow.decorators import task` must resolve to the SAME shim object
    as `from airflow.sdk import task` — identical captured graph."""
    sdk_spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, task
        @task
        def extract() -> None: ...
        @task
        def load() -> None: ...
        with DAG("g"):
            extract() >> load()
    """)
    dec_spec = _compile(monkeypatch, tmp_path, """
        from airflow.decorators import task
        from airflow.sdk import DAG
        @task
        def extract() -> None: ...
        @task
        def load() -> None: ...
        with DAG("g"):
            extract() >> load()
    """)
    assert dec_spec["tasks"] == sdk_spec["tasks"]
    assert [t["task_id"] for t in dec_spec["tasks"]] == ["extract", "load"]
    assert _task(dec_spec, "load")["depends_on"] == ["extract"]


def test_decorators_dag_matches_sdk_dag(monkeypatch, tmp_path):
    """`from airflow.decorators import dag` must resolve to the SAME shim object as
    `from airflow.sdk import dag`; the @dag TaskFlow decorator builds an equivalent
    graph either way."""
    sdk_spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import dag, task
        @task
        def a() -> None: ...
        @dag(schedule="@daily")
        def pipeline():
            a()
        pipeline()
    """)
    dec_spec = _compile(monkeypatch, tmp_path, """
        from airflow.decorators import dag, task
        @task
        def a() -> None: ...
        @dag(schedule="@daily")
        def pipeline():
            a()
        pipeline()
    """)
    assert dec_spec == sdk_spec
    assert dec_spec["dag_id"] == "pipeline"
    assert dec_spec.get("schedule") == "@daily"
    assert [t["task_id"] for t in dec_spec["tasks"]] == ["a"]


def test_from_airflow_import_dag_matches_sdk(monkeypatch, tmp_path):
    """`from airflow import DAG` (deprecated Airflow-3 convenience alias) resolves
    to the same DAG shim as `from airflow.sdk import DAG`."""
    top_spec = _compile(monkeypatch, tmp_path, """
        from airflow import DAG
        from airflow.sdk import task
        @task
        def a() -> None: ...
        with DAG("g"):
            a()
    """)
    sdk_spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, task
        @task
        def a() -> None: ...
        with DAG("g"):
            a()
    """)
    assert top_spec == sdk_spec


@pytest.mark.parametrize("submodule", ["bash", "python"])
def test_removed_core_operator_import_names_providers_standard(
    monkeypatch, tmp_path, submodule
):
    """`airflow.operators.*` was removed from Airflow core in 3.0 and relocated to
    apache-airflow-providers-standard. It must NOT be aliased (that would be a
    2.x-only spelling); it must raise a clear compile error naming the canonical
    airflow.providers.standard.operators.<x> path — not the generic
    operator-unsupported fall-through."""
    op = {"bash": "BashOperator", "python": "PythonOperator"}[submodule]
    with pytest.raises(ValueError) as ei:
        _compile(monkeypatch, tmp_path, f"""
            from airflow.operators.{submodule} import {op}
            from airflow.sdk import DAG
            with DAG("g"):
                pass
        """)
    msg = str(ei.value)
    assert f"airflow.providers.standard.operators.{submodule}" in msg
    # Must NOT fall through to the generic operator-unsupported wording.
    assert "supported: Bash, Http" not in msg


def test_task_branch_rejected_cleanly_not_attributeerror(monkeypatch, tmp_path):
    """@task.branch must fail with the clear branching reject (ADR 0040 Phase D),
    not an opaque AttributeError from a missing decorator attribute. Branching is
    parked pending scheduler skip-state; the parser owes a loud, actionable error
    for the TaskFlow spelling just as it does for BranchPythonOperator."""
    with pytest.raises(ValueError) as ei:
        _compile(monkeypatch, tmp_path, """
            from airflow.sdk import DAG, task
            @task
            def a() -> None: ...
            @task.branch
            def pick() -> str:
                return "a"
            with DAG("g"):
                a()
                pick()
        """)
    msg = str(ei.value)
    assert "not supported by Dexaflow" in msg
    assert "branching" in msg


def test_deferrable_operator_rejected_at_compile(monkeypatch, tmp_path):
    """deferrable=True is refused at compile (before the image build), not left to
    fail inside the pod: Dexaflow has no triggerer (ADR 0040 Phase C)."""
    with pytest.raises(ValueError) as ei:
        _compile(monkeypatch, tmp_path, """
            from airflow.sdk import DAG
            from airflow.providers.amazon.aws.sensors.s3 import S3KeySensor
            with DAG("g"):
                S3KeySensor(task_id="wait", bucket_key="k", bucket_name="b", deferrable=True)
        """)
    msg = str(ei.value)
    assert "deferrable" in msg
    assert "reschedule" in msg or "synchronously" in msg


def test_non_deferrable_operator_compiles(monkeypatch, tmp_path):
    """The guard is explicit-kwarg-only: deferrable=False compiles cleanly."""
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG
        from airflow.providers.amazon.aws.sensors.s3 import S3KeySensor
        with DAG("g"):
            S3KeySensor(task_id="wait", bucket_key="k", bucket_name="b", deferrable=False)
    """)
    assert any(t["task_id"] == "wait" for t in spec["tasks"])
