"""gcp_bigquery_chain — BigQuery operators with chaining, through Dexaflow's generic
operator path (ADR 0040). Demonstrates operator-to-operator XCom: ``second_job``
consumes ``first_job``'s output (its job id) via ``{{ ti.xcom_pull('first_job') }}``,
which Dexaflow resolves like Airflow.

Set the constants below for your environment. Credentials come from the
``google_cloud_platform`` Connection / ADC (see ``examples/gcp_gcs_load``);
``connectors: [gcp]`` installs the provider. The dataset is created and dropped, so the
run leaves nothing behind.
"""
from __future__ import annotations

from airflow.providers.google.cloud.operators.bigquery import (
    BigQueryCreateEmptyDatasetOperator,
    BigQueryDeleteDatasetOperator,
    BigQueryInsertJobOperator,
)
from airflow.sdk import DAG

# --- set these for your environment ---
PROJECT = "your-gcp-project"
DATASET = "leoflow_demo"
LOCATION = "US"


def _query(sql: str) -> dict:
    return {"query": {"query": sql, "useLegacySql": False}}


with DAG("gcp_bigquery_chain", schedule=None, catchup=False, tags=["example"]):
    create = BigQueryCreateEmptyDatasetOperator(
        task_id="create_dataset", dataset_id=DATASET, project_id=PROJECT, location=LOCATION,
    )
    first_job = BigQueryInsertJobOperator(
        task_id="first_job", configuration=_query("SELECT 1 AS x"),
        project_id=PROJECT, location=LOCATION,
    )
    # second_job consumes first_job's return (its job id) — the Airflow-idiomatic chain.
    second_job = BigQueryInsertJobOperator(
        task_id="second_job",
        configuration=_query("SELECT '{{ ti.xcom_pull('first_job') }}' AS upstream_job_id"),
        project_id=PROJECT, location=LOCATION,
    )
    cleanup = BigQueryDeleteDatasetOperator(
        task_id="cleanup", dataset_id=DATASET, project_id=PROJECT,
        delete_contents=True, trigger_rule="all_done",
    )
    create >> first_job >> second_job >> cleanup
