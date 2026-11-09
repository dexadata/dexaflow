"""Constructs the compiler refuses instead of compiling into a run-time failure
or a silent change of semantics (#2336, #225).

Each refusal names what the construct needs, what to do instead, and the issue
that tracks real support, so the author can act on the message alone.
"""
from __future__ import annotations

import json
import textwrap
from pathlib import Path

import pytest

from leoflow_parser.compiler import compile_dag


def _compile(monkeypatch, tmp_path: Path, body: str) -> dict:
    monkeypatch.setenv("LEOFLOW_PROJECT_CONFIG_JSON", json.dumps({"schema_version": "1.0"}))
    src = tmp_path / "dag.py"
    src.write_text(textwrap.dedent(body))
    return compile_dag(str(src), str(tmp_path / "ignored.json"), "img:v1", dag_version="v1")


def _refusal(monkeypatch, tmp_path: Path, body: str) -> str:
    with pytest.raises(ValueError) as excinfo:
        _compile(monkeypatch, tmp_path, body)
    return str(excinfo.value)


# ───────────── operators that need the Airflow metadata database ─────────────

_METADATA_DB_OPERATORS = [
    ("airflow.providers.standard.sensors.external_task", "ExternalTaskSensor",
     "external_dag_id='up'", "#2340"),
    ("airflow.providers.standard.sensors.external_task", "ExternalTaskMarker",
     "external_dag_id='down', external_task_id='t'", "#2340"),
    ("airflow.providers.standard.operators.trigger_dagrun", "TriggerDagRunOperator",
     "trigger_dag_id='down'", "#2340"),
    ("airflow.providers.standard.operators.latest_only", "LatestOnlyOperator",
     "", "#2348"),
]


@pytest.mark.parametrize("module,cls,args,issue", _METADATA_DB_OPERATORS)
def test_metadata_database_operator_is_refused_at_compile(monkeypatch, tmp_path,
                                                          module, cls, args, issue):
    sep = ", " if args else ""
    message = _refusal(monkeypatch, tmp_path, f"""
        from airflow.sdk import DAG
        from {module} import {cls}
        with DAG("g"):
            {cls}(task_id="t"{sep}{args})
    """)
    assert cls in message
    assert "'t'" in message
    assert "metadata database" in message
    assert issue in message
    assert "#operators-that-need-the-airflow-metadata-database" in message


def test_subclass_of_a_metadata_database_operator_is_refused(monkeypatch, tmp_path):
    message = _refusal(monkeypatch, tmp_path, """
        from airflow.sdk import DAG
        from airflow.providers.standard.sensors.external_task import ExternalTaskSensor

        class WaitForWarehouse(ExternalTaskSensor):
            pass

        with DAG("g"):
            WaitForWarehouse(task_id="wait", external_dag_id="warehouse")
    """)
    assert "WaitForWarehouse" in message
    assert "ExternalTaskSensor" in message
    assert "metadata database" in message


def test_branch_operator_subclass_without_branch_in_its_name_is_refused(monkeypatch, tmp_path):
    # LatestOnlyOperator escaped the name check because it is a branch operator
    # whose name does not say so; any BaseBranchOperator subclass can.
    message = _refusal(monkeypatch, tmp_path, """
        from airflow.sdk import DAG
        from airflow.providers.standard.operators.branch import BaseBranchOperator

        class Chooser(BaseBranchOperator):
            pass

        with DAG("g"):
            Chooser(task_id="choose")
    """)
    assert "Chooser" in message
    assert "branching" in message


def test_other_standard_sensors_still_compile(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG
        from airflow.providers.standard.sensors.filesystem import FileSensor
        with DAG("g"):
            FileSensor(task_id="f", filepath="/tmp/x", mode="reschedule")
    """)
    assert spec["tasks"][0]["type"] == "airflow_operator"


# ───────────── task arguments the scheduler does not honor ─────────────

@pytest.mark.parametrize("kwarg,needle", [
    ("depends_on_past=True", "depends_on_past"),
    ("wait_for_downstream=True", "wait_for_downstream"),
    ("priority_weight=10", "priority_weight"),
])
def test_unhonored_task_argument_is_refused_on_a_task(monkeypatch, tmp_path, kwarg, needle):
    message = _refusal(monkeypatch, tmp_path, f"""
        from airflow.sdk import DAG, task
        @task({kwarg})
        def a(): ...
        with DAG("g"):
            a()
    """)
    assert needle in message
    assert "'a'" in message
    assert "ignore" in message


@pytest.mark.parametrize("kwarg,needle", [
    ("depends_on_past=True", "depends_on_past"),
    ("priority_weight=5", "priority_weight"),
])
def test_unhonored_task_argument_is_refused_on_a_provider_operator(monkeypatch, tmp_path,
                                                                   kwarg, needle):
    message = _refusal(monkeypatch, tmp_path, f"""
        from airflow.sdk import DAG
        from airflow.providers.postgres.operators.postgres import PostgresOperator
        with DAG("g"):
            PostgresOperator(task_id="q", sql="select 1", {kwarg})
    """)
    assert needle in message
    assert "'q'" in message


def test_unhonored_task_argument_is_refused_from_default_args(monkeypatch, tmp_path):
    message = _refusal(monkeypatch, tmp_path, """
        from airflow.sdk import DAG
        from airflow.providers.standard.operators.bash import BashOperator
        with DAG("g", default_args={"depends_on_past": True}):
            BashOperator(task_id="b", bash_command="true")
    """)
    assert "depends_on_past" in message
    assert "default_args" in message
    assert "#2350" in message


@pytest.mark.parametrize("kwargs", [
    "depends_on_past=False",
    "wait_for_downstream=False",
    "priority_weight=1",
])
def test_default_values_of_unhonored_arguments_still_compile(monkeypatch, tmp_path, kwargs):
    spec = _compile(monkeypatch, tmp_path, f"""
        from airflow.sdk import DAG
        from airflow.providers.standard.operators.bash import BashOperator
        with DAG("g", default_args={{"depends_on_past": False}}):
            BashOperator(task_id="b", bash_command="true", {kwargs})
    """)
    assert spec["tasks"][0]["task_id"] == "b"


@pytest.mark.parametrize("kwarg,needle", [
    ("max_active_tis_per_dag=1", "max_active_tis_per_dag"),
    ("max_active_tis_per_dagrun=2", "max_active_tis_per_dagrun"),
])
def test_task_concurrency_caps_are_refused_until_carried(monkeypatch, tmp_path, kwarg, needle):
    message = _refusal(monkeypatch, tmp_path, f"""
        from airflow.sdk import DAG, task
        from airflow.providers.standard.operators.bash import BashOperator
        @task({kwarg})
        def a(): ...
        with DAG("g"):
            a()
            BashOperator(task_id="b", bash_command="true")
    """)
    assert needle in message
    assert "'a'" in message
    assert "#2428" in message


def test_task_concurrency_cap_is_refused_from_default_args(monkeypatch, tmp_path):
    message = _refusal(monkeypatch, tmp_path, """
        from airflow.sdk import DAG
        from airflow.providers.standard.operators.bash import BashOperator
        with DAG("g", default_args={"max_active_tis_per_dag": 1}):
            BashOperator(task_id="b", bash_command="true")
    """)
    assert "max_active_tis_per_dag" in message
    assert "default_args" in message


@pytest.mark.parametrize("kwarg,needle", [
    ("dagrun_timeout=timedelta(hours=1)", "dagrun_timeout"),
    ("max_consecutive_failed_dag_runs=3", "max_consecutive_failed_dag_runs"),
])
def test_unhonored_dag_argument_is_refused(monkeypatch, tmp_path, kwarg, needle):
    message = _refusal(monkeypatch, tmp_path, f"""
        from datetime import timedelta
        from airflow.sdk import DAG
        from airflow.providers.standard.operators.bash import BashOperator
        with DAG("g", {kwarg}):
            BashOperator(task_id="b", bash_command="true")
    """)
    assert needle in message
    assert "'g'" in message
    assert "ignore" in message


@pytest.mark.parametrize("kwargs", [
    "dagrun_timeout=None",
    "max_consecutive_failed_dag_runs=0",
])
def test_default_values_of_unhonored_dag_arguments_still_compile(monkeypatch, tmp_path, kwargs):
    spec = _compile(monkeypatch, tmp_path, f"""
        from airflow.sdk import DAG
        from airflow.providers.standard.operators.bash import BashOperator
        with DAG("g", {kwargs}):
            BashOperator(task_id="b", bash_command="true", max_active_tis_per_dag=None)
    """)
    assert spec["tasks"][0]["task_id"] == "b"


def test_dag_without_the_refused_arguments_compiles_unchanged(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG
        from airflow.providers.standard.operators.bash import BashOperator
        with DAG("g", schedule="@daily", max_active_runs=2):
            BashOperator(task_id="b", bash_command="true", retries=1)
    """)
    assert spec == {
        "schema_version": "1.0", "dag_id": "g", "dag_version": "v1", "image": "img:v1",
        "schedule": "@daily", "max_active_runs": 2,
        "tasks": [{"task_id": "b", "type": "bash", "entrypoint": "true", "retries": 1}],
    }


# ───────────── setup and teardown, task groups ─────────────

def test_as_setup_is_refused_with_a_message(monkeypatch, tmp_path):
    message = _refusal(monkeypatch, tmp_path, """
        from airflow.sdk import DAG
        from airflow.providers.standard.operators.bash import BashOperator
        with DAG("g"):
            BashOperator(task_id="create", bash_command="true").as_setup()
    """)
    assert "setup and teardown" in message
    assert "all_done" in message


def test_as_teardown_is_refused_with_a_message(monkeypatch, tmp_path):
    message = _refusal(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, task
        @task
        def drop(): ...
        with DAG("g"):
            drop().as_teardown()
    """)
    assert "setup and teardown" in message


@pytest.mark.parametrize("decorator", ["setup", "teardown"])
def test_setup_and_teardown_decorators_are_refused(monkeypatch, tmp_path, decorator):
    message = _refusal(monkeypatch, tmp_path, f"""
        from airflow.sdk import DAG, {decorator}
        @{decorator}
        def step(): ...
        with DAG("g"):
            step()
    """)
    assert "setup and teardown" in message


@pytest.mark.parametrize("import_line", [
    "from airflow.sdk import TaskGroup",
    "from airflow.utils.task_group import TaskGroup",
    "from airflow.sdk import task_group",
    "from airflow.decorators import task_group",
])
def test_task_group_is_refused_with_flatten_advice(monkeypatch, tmp_path, import_line):
    message = _refusal(monkeypatch, tmp_path, f"""
        {import_line}
        from airflow.sdk import DAG
        with DAG("g"):
            pass
    """)
    assert "flatten" in message
    assert "#2338" in message
    assert "cannot import name" not in message


# ──────────── default_args: trigger_rule applied, dropped keys refused (#2448) ────────────

def _task(spec: dict, task_id: str) -> dict:
    return next(t for t in spec["tasks"] if t["task_id"] == task_id)


def test_trigger_rule_from_default_args_applies_to_every_task(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, task
        from airflow.providers.standard.operators.bash import BashOperator
        @task
        def a(): ...
        with DAG("g", default_args={"trigger_rule": "all_done"}):
            first = a()
            first >> BashOperator(task_id="b", bash_command="true")
    """)
    assert _task(spec, "a")["trigger_rule"] == "all_done"
    assert _task(spec, "b")["trigger_rule"] == "all_done"


def test_trigger_rule_of_the_task_wins_over_default_args(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, task, TriggerRule
        @task(trigger_rule=TriggerRule.ALL_SUCCESS)
        def a(): ...
        @task
        def b(): ...
        with DAG("g", default_args={"trigger_rule": TriggerRule.ONE_FAILED}):
            a() >> b()
    """)
    assert "trigger_rule" not in _task(spec, "a")
    assert _task(spec, "b")["trigger_rule"] == "one_failed"


def test_unsupported_trigger_rule_in_default_args_is_refused(monkeypatch, tmp_path):
    message = _refusal(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, task
        @task
        def a(): ...
        with DAG("g", default_args={"trigger_rule": "none_failed_min_one_success"}):
            a()
    """)
    assert "none_failed_min_one_success" in message


_DROPPED_TASK_ARGS = [
    ("email", "['ops@example.com']", "alerts:"),
    ("executor_config", "{'pod_override': None}", "size:"),
    ("queue", "'gpu'", "size:"),
    ("executor", "'CeleryExecutor'", "own pod"),
    ("run_as_user", "'etl'", "image's user"),
    ("weight_rule", "'upstream'", None),
    ("retry_exponential_backoff", "True", "#2454"),
    ("max_retry_delay", "timedelta(hours=1)", "#2454"),
    ("sla", "timedelta(hours=1)", "removed"),
    ("end_date", "datetime(2030, 1, 1)", "Pause"),
]


@pytest.mark.parametrize("name,value,hint", _DROPPED_TASK_ARGS)
@pytest.mark.parametrize("where", ["task", "default_args"])
def test_dropped_task_argument_is_refused(monkeypatch, tmp_path, name, value, hint, where):
    task_kw = f"{name}={value}" if where == "task" else ""
    dag_kw = f"default_args={{{name!r}: {value}}}" if where == "default_args" else ""
    message = _refusal(monkeypatch, tmp_path, f"""
        from datetime import datetime, timedelta
        from airflow.sdk import DAG
        from airflow.providers.standard.operators.bash import BashOperator
        with DAG("g", {dag_kw}):
            BashOperator(task_id="t", bash_command="true", {task_kw})
    """)
    assert f"{name}=" in message
    assert "'t'" in message
    assert "not honored by Dexaflow" in message
    if where == "default_args":
        assert "default_args" in message
    if hint:
        assert hint in message


@pytest.mark.parametrize("value", ["'default'", "None"])
def test_default_queue_still_compiles(monkeypatch, tmp_path, value):
    spec = _compile(monkeypatch, tmp_path, f"""
        from airflow.sdk import DAG
        from airflow.providers.standard.operators.bash import BashOperator
        with DAG("g", default_args={{"owner": "data", "email_on_failure": True,
                                     "queue": {value}, "weight_rule": "downstream"}}):
            BashOperator(task_id="t", bash_command="true", doc_md="docs")
    """)
    assert _task(spec, "t")["type"] == "bash"


def test_dropped_argument_on_a_provider_operator_is_refused(monkeypatch, tmp_path):
    message = _refusal(monkeypatch, tmp_path, """
        from airflow.sdk import DAG
        from airflow.providers.common.sql.operators.sql import SQLExecuteQueryOperator
        with DAG("g"):
            SQLExecuteQueryOperator(task_id="q", sql="select 1", conn_id="db", queue="gpu")
    """)
    assert "queue='gpu'" in message


def test_pre_execute_hook_is_refused(monkeypatch, tmp_path):
    message = _refusal(monkeypatch, tmp_path, """
        from airflow.sdk import DAG
        from airflow.providers.standard.operators.bash import BashOperator
        def hook(context): ...
        with DAG("g"):
            BashOperator(task_id="t", bash_command="true", pre_execute=hook)
    """)
    assert "pre_execute=" in message


@pytest.mark.parametrize("name", ["on_execute_callback", "on_skipped_callback"])
@pytest.mark.parametrize("where", ["task", "default_args"])
def test_unsupported_task_callbacks_are_refused(monkeypatch, tmp_path, name, where):
    task_kw = f"{name}=notify" if where == "task" else ""
    dag_kw = f"default_args={{{name!r}: notify}}" if where == "default_args" else ""
    message = _refusal(monkeypatch, tmp_path, f"""
        from airflow.sdk import DAG, task
        def notify(context): ...
        @task({task_kw})
        def a(): ...
        with DAG("g", {dag_kw}):
            a()
    """)
    assert name in message
    assert "not supported by Dexaflow" in message


def test_start_date_from_default_args_is_the_dag_start_date(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, """
        import pendulum
        from airflow.sdk import DAG, task
        @task
        def a(): ...
        with DAG("g", schedule="@daily",
                 default_args={"start_date": pendulum.datetime(2026, 1, 1, tz="UTC")}):
            a()
    """)
    assert spec["start_date"] == "2026-01-01T00:00:00+00:00"


# ──────────── DAG arguments that were dropped (#2449) ────────────

@pytest.mark.parametrize("schedule,hint", [
    ("timedelta(minutes=30)", "timedelta"),
    ("'@continuous'", "@continuous"),
    ("object()", "#2352"),
])
def test_schedule_that_is_not_a_cron_string_is_refused(monkeypatch, tmp_path, schedule,
                                                       hint):
    message = _refusal(monkeypatch, tmp_path, f"""
        from datetime import timedelta
        from airflow.sdk import DAG, task
        @task
        def a(): ...
        with DAG("g", schedule={schedule}):
            a()
    """)
    assert hint in message
    assert "only when triggered" in message


_DROPPED_DAG_ARGS = [
    ("end_date", "datetime(2030, 1, 1)", "Pause"),
    ("on_failure_callback", "notify", "default_args"),
    ("on_success_callback", "notify", "alerts:"),
    ("sla_miss_callback", "notify", "SLA"),
    ("fail_fast", "True", None),
    ("render_template_as_native_obj", "True", "Jinja"),
    ("user_defined_macros", "{'x': 1}", "Jinja"),
    ("user_defined_filters", "{'f': notify}", "Jinja"),
    ("template_searchpath", "'/sql'", "Jinja"),
    ("jinja_environment_kwargs", "{'trim_blocks': True}", "Jinja"),
    ("access_control", "{'role': {'can_read'}}", "roles"),
]


@pytest.mark.parametrize("name,value,hint", _DROPPED_DAG_ARGS)
def test_dropped_dag_argument_is_refused(monkeypatch, tmp_path, name, value, hint):
    message = _refusal(monkeypatch, tmp_path, f"""
        from datetime import datetime
        from airflow.sdk import DAG, task
        def notify(context): ...
        @task
        def a(): ...
        with DAG("g", {name}={value}):
            a()
    """)
    assert f"{name}=" in message
    assert "DAG 'g'" in message
    if hint:
        assert hint in message


def test_notifier_as_dag_callback_is_named(monkeypatch, tmp_path):
    message = _refusal(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, task
        from airflow.providers.slack.notifications.slack import SlackNotifier
        @task
        def a(): ...
        with DAG("g", on_failure_callback=SlackNotifier(text="failed")):
            a()
    """)
    assert "Notifier" in message


def test_notifier_built_inside_the_dag_is_not_a_task(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, task
        from airflow.providers.slack.notifications.slack import SlackNotifier
        with DAG("g"):
            @task(on_failure_callback=SlackNotifier(text="failed"))
            def a(): ...
            a()
    """)
    assert [t["task_id"] for t in spec["tasks"]] == ["a"]
    assert _task(spec, "a")["on_failure_callback"] is True


# ──────────── operators a task pod cannot run (#2449, #2450) ────────────

@pytest.mark.parametrize("module,cls,needs", [
    ("airflow.providers.cncf.kubernetes.operators.pod", "KubernetesPodOperator", "pod"),
    ("airflow.providers.cncf.kubernetes.operators.job", "KubernetesJobOperator", "pod"),
    ("airflow.providers.google.cloud.operators.kubernetes_engine", "GKEStartPodOperator",
     "pod"),
    ("airflow.providers.docker.operators.docker", "DockerOperator", "Docker daemon"),
    ("airflow.providers.standard.operators.hitl", "ApprovalOperator", "API server"),
    ("airflow.providers.standard.operators.hitl", "HITLBranchOperator", "API server"),
])
def test_operator_a_task_pod_cannot_run_is_refused(monkeypatch, tmp_path, module, cls,
                                                   needs):
    message = _refusal(monkeypatch, tmp_path, f"""
        from airflow.sdk import DAG
        from {module} import {cls}
        with DAG("g"):
            {cls}(task_id="t")
    """)
    assert cls in message
    assert needs in message
    assert "not supported by Dexaflow" in message


def test_subclass_of_pod_operator_is_refused(monkeypatch, tmp_path):
    message = _refusal(monkeypatch, tmp_path, """
        from airflow.sdk import DAG
        from airflow.providers.cncf.kubernetes.operators.pod import KubernetesPodOperator
        class MyPod(KubernetesPodOperator):
            pass
        with DAG("g"):
            MyPod(task_id="t")
    """)
    assert "MyPod (a KubernetesPodOperator)" in message


def test_subclass_of_provider_operator_is_refused(monkeypatch, tmp_path):
    message = _refusal(monkeypatch, tmp_path, """
        from airflow.sdk import DAG
        from airflow.providers.common.sql.operators.sql import SQLExecuteQueryOperator
        class AuditedSQL(SQLExecuteQueryOperator):
            def execute(self, context):
                return super().execute(context)
        with DAG("g"):
            AuditedSQL(task_id="q", sql="select 1", conn_id="db")
    """)
    assert "AuditedSQL" in message
    assert "SQLExecuteQueryOperator" in message
    assert "airflow.providers.common.sql.operators.sql.SQLExecuteQueryOperator" in message


def test_provider_operator_itself_still_compiles(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG
        from airflow.providers.common.sql.operators.sql import SQLExecuteQueryOperator
        with DAG("g"):
            SQLExecuteQueryOperator(task_id="q", sql="select 1", conn_id="db")
    """)
    assert _task(spec, "q")["type"] == "airflow_operator"


def test_custom_operator_message_says_what_to_do(monkeypatch, tmp_path):
    message = _refusal(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, BaseOperator
        class Mine(BaseOperator):
            def execute(self, context): ...
        with DAG("g"):
            Mine(task_id="t")
    """)
    assert "Mine" in message
    assert "custom operator" in message
    assert "@task" in message


@pytest.mark.parametrize("decorator,hint", [
    ("docker", "Docker daemon"),
    ("kubernetes", "pod"),
    ("virtualenv", "dependencies:"),
    ("external_python", "dependencies:"),
    ("pyspark", "Spark"),
    ("short_circuit", "#787"),
    ("sensor", "#2339"),
])
def test_refused_task_decorator_names_itself(monkeypatch, tmp_path, decorator, hint):
    message = _refusal(monkeypatch, tmp_path, f"""
        from airflow.sdk import DAG, task
        @task.{decorator}
        def a(): ...
        with DAG("g"):
            a()
    """)
    assert f"@task.{decorator}" in message
    assert hint in message


@pytest.mark.parametrize("decorator", ["run_if", "skip_if"])
def test_conditional_task_decorators_are_refused(monkeypatch, tmp_path, decorator):
    message = _refusal(monkeypatch, tmp_path, f"""
        from airflow.sdk import DAG, task
        @task.{decorator}(lambda context: True)
        @task
        def a(): ...
        with DAG("g"):
            a()
    """)
    assert f"@task.{decorator}" in message


@pytest.mark.parametrize("cls,hint", [
    ("ShortCircuitOperator", "short-circuit"),
    ("BranchPythonOperator", "branching"),
    ("PythonVirtualenvOperator", "virtualenv"),
    ("ExternalPythonOperator", "external interpreter"),
])
def test_standard_python_operators_get_their_own_refusal(monkeypatch, tmp_path, cls, hint):
    message = _refusal(monkeypatch, tmp_path, f"""
        from airflow.sdk import DAG
        from airflow.providers.standard.operators.python import {cls}
        def f(): ...
        with DAG("g"):
            {cls}(task_id="t", python_callable=f)
    """)
    assert cls in message
    assert hint in message


@pytest.mark.parametrize("call", [
    "Op.partial(task_id='t').expand(x=[1])",
    "a.expand_kwargs([{'x': 1}])",
    "a().map(lambda v: v)",
])
def test_every_form_of_dynamic_mapping_is_refused(monkeypatch, tmp_path, call):
    message = _refusal(monkeypatch, tmp_path, f"""
        from airflow.sdk import DAG, task
        from airflow.providers.standard.operators.bash import BashOperator as Op
        @task
        def a(x=None): ...
        with DAG("g"):
            {call}
    """)
    assert "dynamic task mapping" in message


def test_indexing_a_task_output_is_refused(monkeypatch, tmp_path):
    message = _refusal(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, task
        @task(multiple_outputs=True)
        def a(): return {"k": 1}
        @task
        def b(v): ...
        with DAG("g"):
            b(a()["k"])
    """)
    assert "result['key']" in message


# ──────────── imports and idioms (#2451) ────────────

def test_common_airflow3_imports_compile(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, """
        import pendulum
        from airflow.exceptions import AirflowFailException, AirflowSkipException
        from airflow.sdk.exceptions import AirflowException
        from airflow.sdk import (DAG, task, get_current_context, TriggerRule,
                                 BaseSensorOperator, PokeReturnValue, BaseHook,
                                 Connection, Context, Metadata, ParamsDict, WeightRule,
                                 ObjectStoragePath, get_parsing_context, literal)
        from airflow.sdk.bases.sensor import BaseSensorOperator as Base2
        from airflow.models.dag import DAG as OldDAG
        from airflow.models.baseoperator import chain
        from airflow.hooks.base import BaseHook as OldHook
        from airflow.utils.trigger_rule import TriggerRule as OldRule
        from airflow.providers.standard.operators.bash import BashOperator

        BASE = ObjectStoragePath("s3://bucket/raw", conn_id="aws") / "events"

        @task(trigger_rule=TriggerRule.ALL_DONE)
        def a(context: Context = None):
            get_current_context()
            raise AirflowFailException("never at compile")

        with DAG("g", start_date=pendulum.datetime(2026, 1, 1, tz="America/Sao_Paulo"),
                 schedule="@daily",
                 default_args={"retry_delay": pendulum.duration(minutes=5)}):
            echo = BashOperator(task_id="echo", bash_command="echo hi")
            first = a()
            first.set_downstream(echo)
            OldRule.ALL_DONE
            get_parsing_context().dag_id
    """)
    assert _task(spec, "a")["trigger_rule"] == "all_done"
    assert _task(spec, "a")["retry_delay_seconds"] == 300
    assert spec["start_date"] == "2026-01-01T00:00:00-03:00"
    assert _task(spec, "echo")["depends_on"] == ["a"]


def test_operator_output_is_the_return_value(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG, task
        from airflow.providers.standard.operators.bash import BashOperator
        @task
        def use(line): ...
        with DAG("g"):
            use(BashOperator(task_id="echo", bash_command="echo hi").output)
    """)
    assert _task(spec, "use")["xcom_input"] == {"line": ["echo"]}


@pytest.mark.parametrize("call,hint", [
    ("get_current_context()", "inside the task body"),
    ("BaseHook.get_connection('db')", "inside the task body"),
])
def test_runtime_only_helpers_at_module_level_are_refused(monkeypatch, tmp_path, call, hint):
    message = _refusal(monkeypatch, tmp_path, f"""
        from airflow.sdk import DAG, task, get_current_context, BaseHook
        {call}
        @task
        def a(): ...
        with DAG("g"):
            a()
    """)
    assert hint in message


@pytest.mark.parametrize("line,hint", [
    ("from airflow.sdk import Asset", "#2352"),
    ("from airflow.sdk import asset", "#2352"),
    ("from airflow.timetables.trigger import CronTriggerTimetable", "#2351"),
    ("from cosmos import DbtDag", "dbt_group()"),
    ("import pandas_not_installed_anywhere", "dependencies:"),
])
def test_import_messages_name_the_construct_and_the_way_out(monkeypatch, tmp_path, line,
                                                            hint):
    message = _refusal(monkeypatch, tmp_path, f"""
        {line}
        from airflow.sdk import DAG
        with DAG("g"):
            pass
    """)
    assert hint in message


def test_templates_dict_is_carried_in_call_args(monkeypatch, tmp_path):
    spec = _compile(monkeypatch, tmp_path, """
        from airflow.sdk import DAG
        from airflow.providers.standard.operators.python import PythonOperator
        def f(templates_dict=None): ...
        with DAG("g"):
            PythonOperator(task_id="t", python_callable=f,
                           templates_dict={"day": "{{ ds }}"})
    """)
    assert _task(spec, "t")["call_args"] == {"templates_dict": {"day": "{{ ds }}"}}
