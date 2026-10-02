# gcp_gcs_bucket — Google Cloud Storage operators

A self-contained Cloud Storage bucket lifecycle using the **real Google provider
operators** through Dexaflow's generic operator path (ADR 0040):

```
create (GCSCreateBucketOperator) >> list (GCSListObjectsOperator) >> delete (GCSDeleteBucketOperator)
```

Contrast [`examples/gcp_gcs_load`](../gcp_gcs_load/), which talks to GCS from a
hand-written client inside a `@task`. This one drives the provider **operators**
directly — the path Dexaflow runs standalone in the task pod.

## Set up

1. Edit the constants in `dag.py`: `PROJECT`, `BUCKET` (globally unique), `LOCATION`.
2. Credentials come from the `google_cloud_platform` Connection / ADC — see
   [`examples/gcp_gcs_load`](../gcp_gcs_load/) for the auth modes (keyless / Workload
   Identity recommended). `connectors: [gcp]` installs the provider.

## Run

```bash
# Lite (local): host ADC via `gcloud auth application-default login`
dexaflow lite --executor=subprocess examples/gcp_gcs_bucket
```

The DAG creates the bucket, lists it (empty), then deletes it — leaving nothing behind.
Bucket create/list/delete are free-tier operations.
