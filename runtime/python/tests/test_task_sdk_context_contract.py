"""The task context the runtime builds, checked against the pinned Task SDK (#1843).

The runtime hands operators, ``@task`` functions and bash commands a context it
builds itself (``runner._operator_context``), not the Task SDK's. These tests list
the keys of the SDK's ``Context`` TypedDict and the public attributes of its
``RuntimeTaskInstance`` and fail when the runtime neither provides one nor lists it
below with the reason it cannot. A Task SDK bump that adds a key or an attribute
therefore fails here, in the same pull request, instead of in a provider at run time.

They need the Task SDK, so they skip in the Airflow-free runtime job and run in the
``runtime-authoring-contract`` job, which installs the version ``runtime/Dockerfile``
pins.
"""

import json
import sys

import pytest

pytest.importorskip("airflow.sdk")

from airflow.sdk.definitions.context import Context  # noqa: E402
from airflow.sdk.execution_time.task_runner import RuntimeTaskInstance  # noqa: E402
from pydantic import BaseModel  # noqa: E402

from leoflow_runtime import runner  # noqa: E402

# Context keys the runtime does not provide, each with the reason. A key leaves
# this list in the change that provides it.
CONTEXT_KEYS_NOT_PROVIDED = {
    "asset_state_store": "assets are not delivered to the task pod",
    "inlet_events": "assets are not delivered to the task pod",
    "inlets": "assets are not delivered to the task pod",
    "outlet_events": "assets are not delivered to the task pod",
    "outlets": "assets are not delivered to the task pod",
    "triggering_asset_events": "assets are not delivered to the task pod",
    "expanded_ti_count": "the pod runs one task instance; no mapping data is delivered",
    "map_index_template": "the pod runs one task instance; no mapping data is delivered",
    "partition_date": "Dexaflow runs have no partition",
    "partition_key": "Dexaflow runs have no partition",
    "prev_data_interval_start_success": "the previous successful run is not delivered",
    "prev_data_interval_end_success": "the previous successful run is not delivered",
    "prev_start_date_success": "the previous successful run is not delivered",
    "prev_end_date_success": "the previous successful run is not delivered",
    "reason": "no Dexaflow path sets it",
    "task_reschedule_count": "the agent delivers the first reschedule time, not the count",
    "task_state_store": "Dexaflow has no task state store",
    "templates_dict": (
        "the compiler does not capture a task's templates_dict, and None would hide one "
        "the author set"),
    "test_mode": "the Task SDK itself does not set it",
}

# Public RuntimeTaskInstance attributes the runtime's task instance does not have.
TI_ATTRIBUTES_NOT_PROVIDED = {
    "axcom_pull": "async XCom is not supported",
    "axcom_push": "async XCom is not supported",
    "bundle_instance": "the image carries the DAG; there is no DAG bundle",
    "context_carrier": "no trace context is delivered to the task",
    "dag_version_id": "the DAG version id is not delivered to the task",
    "end_date": "the task instance row is not delivered to the task",
    "hostname": "the task instance row is not delivered to the task",
    "id": "the task instance row is not delivered to the task",
    "is_mapped": "no mapping data is delivered",
    "log_url": "the task instance row is not delivered to the task",
    "mark_success_url": "the task instance row is not delivered to the task",
    "queue": "the task instance row is not delivered to the task",
    "rendered_map_index": "no mapping data is delivered",
    "start_date": "the task instance row is not delivered to the task",
    "state": "the task instance row is not delivered to the task",
    "get_dag": "the runtime does not load the DAG object",
    "get_dagrun_state": "needs the metastore, which the task cannot reach",
    "get_dr_count": "needs the metastore, which the task cannot reach",
    "get_previous_dagrun": "needs the metastore, which the task cannot reach",
    "get_previous_ti": "needs the metastore, which the task cannot reach",
    "get_relevant_upstream_map_indexes": "no mapping data is delivered",
    "get_task_breadcrumbs": "needs the metastore, which the task cannot reach",
    "get_task_states": "needs the metastore, which the task cannot reach",
    "get_ti_count": "needs the metastore, which the task cannot reach",
    "get_template_context": "the runtime hands the context to execute() and callbacks",
    "render_templates": "the runtime renders the template fields itself",
    "sentry_integration": "Sentry is not integrated",
}

_FAILING_TASK = """
SEEN = []


def _capture(context):
    SEEN.append(sorted(context))


def task():
    raise RuntimeError("fail on purpose to reach on_failure_callback")


task.on_failure_callback = _capture
"""


def _sdk_context_keys() -> set:
    return set(Context.__annotations__)


def _sdk_ti_attributes() -> set:
    names = set(RuntimeTaskInstance.model_fields)
    names |= {n for n in dir(RuntimeTaskInstance) if not n.startswith("_")}
    return names - {n for n in dir(BaseModel) if not n.startswith("_")}


def _provided_context_keys(tmp_path, monkeypatch) -> set:
    """Run a failing @task through the real entry point with every value the agent
    delivers, and return the keys its on_failure_callback received: the fullest
    context the runtime builds."""
    for name, value in {
        "LEOFLOW_DAG_ID": "report",
        "LEOFLOW_TASK_ID": "build",
        "LEOFLOW_RUN_ID": "scheduled__2026-06-14T05:30:00+00:00",
        "LEOFLOW_TRY_NUMBER": "1",
        "LEOFLOW_MAX_TRIES": "1",
        "LEOFLOW_TS": "2026-06-14T05:30:00Z",
        "LEOFLOW_DS": "2026-06-14",
        "LEOFLOW_DATA_INTERVAL_START": "2026-06-13T05:30:00Z",
        "LEOFLOW_DATA_INTERVAL_END": "2026-06-14T05:30:00Z",
        "LEOFLOW_PARAMS": json.dumps({"table": "orders"}),
        "LEOFLOW_RUN_CONF": json.dumps({"table": "orders"}),
        "LEOFLOW_RUN_TYPE": "scheduled",
        "LEOFLOW_ON_FAILURE_CALLBACK": "1",
        "LEOFLOW_RETURN_VALUE_PATH": str(tmp_path / "rv.json"),
    }.items():
        monkeypatch.setenv(name, value)
    (tmp_path / "contract_dag.py").write_text(_FAILING_TASK)
    monkeypatch.syspath_prepend(str(tmp_path))
    monkeypatch.delitem(sys.modules, "contract_dag", raising=False)
    with pytest.raises(RuntimeError):
        runner.run("contract_dag:task")
    (seen,) = sys.modules["contract_dag"].SEEN
    return set(seen)


def test_context_covers_the_task_sdk_context(tmp_path, monkeypatch):
    provided = _provided_context_keys(tmp_path, monkeypatch)
    missing = _sdk_context_keys() - provided - set(CONTEXT_KEYS_NOT_PROVIDED)
    assert not missing, (
        f"the pinned Task SDK's Context has keys the runtime neither provides nor "
        f"lists in CONTEXT_KEYS_NOT_PROVIDED: {sorted(missing)}")


def test_context_exemptions_are_current(tmp_path, monkeypatch):
    provided = _provided_context_keys(tmp_path, monkeypatch)
    exempt = set(CONTEXT_KEYS_NOT_PROVIDED)
    assert not exempt & provided, f"provided now, drop the exemption: {sorted(exempt & provided)}"
    gone = exempt - _sdk_context_keys()
    assert not gone, f"no longer in the Task SDK's Context, drop the exemption: {sorted(gone)}"


def test_context_has_no_keys_outside_the_task_sdk(tmp_path, monkeypatch):
    extra = _provided_context_keys(tmp_path, monkeypatch) - _sdk_context_keys()
    assert not extra, f"keys the Task SDK's Context does not define: {sorted(extra)}"


def test_task_instance_covers_the_runtime_task_instance(monkeypatch):
    monkeypatch.setenv("LEOFLOW_TASK_ID", "build")
    ti = runner._operator_context()["ti"]
    missing = {
        name for name in _sdk_ti_attributes() - set(TI_ATTRIBUTES_NOT_PROVIDED)
        if not hasattr(ti, name)
    }
    assert not missing, (
        f"the pinned Task SDK's RuntimeTaskInstance has attributes the runtime's task "
        f"instance neither has nor lists in TI_ATTRIBUTES_NOT_PROVIDED: {sorted(missing)}")


def test_task_instance_exemptions_are_current():
    ti = runner._operator_context()["ti"]
    exempt = set(TI_ATTRIBUTES_NOT_PROVIDED)
    provided = {name for name in exempt if hasattr(ti, name)}
    assert not provided, f"provided now, drop the exemption: {sorted(provided)}"
    gone = exempt - _sdk_ti_attributes()
    assert not gone, f"no longer on RuntimeTaskInstance, drop the exemption: {sorted(gone)}"
