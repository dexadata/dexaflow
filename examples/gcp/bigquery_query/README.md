# gcp_bigquery_query — a single BigQuery operator

The minimal "hello, BigQuery operator" for Dexaflow's generic operator path (ADR 0040):
one `BigQueryInsertJobOperator` running a **metadata** query (`INFORMATION_SCHEMA` —
lists the project's datasets), so it touches no real data table.

**Cost note:** BigQuery bills per byte scanned, and some public datasets are terabytes —
a careless `SELECT` over real data is a cost trap. This example (a) queries the catalog
(INFORMATION_SCHEMA, billed at its 10 MB minimum — cheap, not free) and (b) sets
`maximumBytesBilled` (100 MB) so any query that would scan more is **rejected before it
runs, at no cost**. Keep both guardrails when you adapt it.

## Set up

1. Edit `PROJECT` in `dag.py` to a project you can run BigQuery jobs in.
2. Credentials come from the `google_cloud_platform` Connection / ADC — see
   [`examples/gcp_gcs_load`](../../gcp_gcs_load/) for the auth modes. `connectors: [gcp]`
   installs the provider.

## Run

```bash
# Lite (local): host ADC via `gcloud auth application-default login`
dexaflow lite --executor=subprocess examples/gcp/bigquery_query
```

For multi-step BigQuery (dataset + chained queries + cleanup), see
[`examples/gcp/bigquery_chain`](../bigquery_chain/).
