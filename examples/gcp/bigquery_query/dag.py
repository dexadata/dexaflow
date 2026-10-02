"""gcp_bigquery_query — a single real BigQuery operator through Dexaflow's generic
operator path (ADR 0040), credentials from the google_cloud_platform connector.

Runs one query with BigQueryInsertJobOperator against a BigQuery public dataset
(no setup, free-tier). Set PROJECT to a project you can run BigQuery jobs in
(billing/execution project); credentials come from the google_cloud_platform
Connection / ADC and ``connectors: [gcp]`` installs the provider.
"""
from __future__ import annotations

from airflow.providers.google.cloud.operators.bigquery import BigQueryInsertJobOperator
from airflow.sdk import DAG

# --- set this for your environment (the project BigQuery jobs run/bill in) ---
PROJECT = "your-gcp-project"
LOCATION = "US"

# Query the catalog (INFORMATION_SCHEMA) — lists the project's datasets, touching no
# real data table. BigQuery bills per byte scanned and some public datasets are
# terabytes, so a SELECT over real data is a cost trap. (INFORMATION_SCHEMA itself has
# a 10 MB minimum billing — it is cheap, not free.)
QUERY = "SELECT schema_name, location FROM `region-us`.INFORMATION_SCHEMA.SCHEMATA ORDER BY schema_name LIMIT 50"

# Cost guardrail: cap the bytes BigQuery may bill. If a query would scan more than
# this, the job is REJECTED before running (no cost) — so editing QUERY to hit a large
# table can't surprise-bill you. ~100 MB leaves headroom over the 10 MB metadata floor.
MAX_BYTES_BILLED = "100000000"  # 100 MB

with DAG("gcp_bigquery_query", schedule=None, catchup=False, tags=["example"]):
    BigQueryInsertJobOperator(
        task_id="query",
        configuration={
            "query": {
                "query": QUERY,
                "useLegacySql": False,
                "maximumBytesBilled": MAX_BYTES_BILLED,
            }
        },
        project_id=PROJECT,
        location=LOCATION,
    )
