"""gcp_dataform_trigger — compile a Dataform repository and run its workflow using
the real Google provider operators, through Dexaflow's generic operator path (ADR 0040).

This is the reference for **chained operators**: ``invoke`` consumes ``compile``'s
output with the idiomatic ``{{ ti.xcom_pull('compile')['name'] }}`` — Dexaflow resolves
the upstream's return_value exactly like Airflow does, so the two operators chain.

Set the constants below for your environment. Credentials come from the
``google_cloud_platform`` Connection / ADC (see ``examples/gcp_gcs_load`` for the auth
modes); ``connectors: [gcp]`` installs the provider at build.
"""
from __future__ import annotations

from airflow.providers.google.cloud.operators.dataform import (
    DataformCreateCompilationResultOperator,
    DataformCreateWorkflowInvocationOperator,
)
from airflow.sdk import DAG

# --- set these for your environment ---
PROJECT = "your-gcp-project"
REGION = "us-central1"
REPOSITORY = "your-dataform-repository"
GIT_COMMITISH = "main"  # branch / tag / commit to compile
# Set only if your project enforces strict act-as on Dataform invocations; otherwise
# leave empty and the invocation runs as the default Dataform service account.
SERVICE_ACCOUNT = ""

# invoke reads compile's result name from XCom — the Airflow-idiomatic chain.
_invocation: dict = {"compilation_result": "{{ ti.xcom_pull('compile')['name'] }}"}
if SERVICE_ACCOUNT:
    _invocation["invocation_config"] = {"service_account": SERVICE_ACCOUNT}

with DAG("gcp_dataform_trigger", schedule=None, catchup=False, tags=["example"]):
    compile_result = DataformCreateCompilationResultOperator(
        task_id="compile",
        project_id=PROJECT,
        region=REGION,
        repository_id=REPOSITORY,
        compilation_result={"git_commitish": GIT_COMMITISH},
    )
    invoke = DataformCreateWorkflowInvocationOperator(
        task_id="invoke",
        project_id=PROJECT,
        region=REGION,
        repository_id=REPOSITORY,
        workflow_invocation=_invocation,
    )
    compile_result >> invoke
