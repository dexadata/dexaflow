"""gcp_gcs_bucket — Google Cloud Storage operators through Dexaflow's generic
operator path (ADR 0040). A self-contained bucket lifecycle: create -> list -> delete,
using the real Google provider operators (not a hand-written client).

Set the constants below for your environment. Credentials come from the
``google_cloud_platform`` Connection / ADC (see ``examples/gcp_gcs_load`` for the auth
modes); ``connectors: [gcp]`` installs the provider.
"""
from __future__ import annotations

from airflow.providers.google.cloud.operators.gcs import (
    GCSCreateBucketOperator,
    GCSDeleteBucketOperator,
    GCSListObjectsOperator,
)
from airflow.sdk import DAG

# --- set these for your environment ---
PROJECT = "your-gcp-project"
BUCKET = "your-globally-unique-bucket-name"  # GCS bucket names are global
LOCATION = "US"

with DAG("gcp_gcs_bucket", schedule=None, catchup=False, tags=["example"]):
    create = GCSCreateBucketOperator(
        task_id="create",
        bucket_name=BUCKET,
        project_id=PROJECT,
        storage_class="STANDARD",
        location=LOCATION,
    )
    listing = GCSListObjectsOperator(task_id="list", bucket=BUCKET)
    delete = GCSDeleteBucketOperator(task_id="delete", bucket_name=BUCKET)
    create >> listing >> delete
