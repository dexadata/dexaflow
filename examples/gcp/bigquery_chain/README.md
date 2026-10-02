# gcp_bigquery_chain — BigQuery operators with chaining

BigQuery provider operators through Dexaflow's generic operator path (ADR 0040),
demonstrating **operator-to-operator XCom**: `second_job` consumes `first_job`'s
output (its job id) via `{{ ti.xcom_pull('first_job') }}`, which Dexaflow resolves the
same way Airflow does.

```
create_dataset >> first_job >> second_job >> cleanup
                     │            ▲
                     └─ job_id ───┘  (via ti.xcom_pull)
```

The dataset is created and dropped (`cleanup` runs with `trigger_rule="all_done"`), so
the run leaves nothing behind. DDL and the `SELECT` of a constant process ~0 bytes —
free-tier.

## Set up

1. Edit the constants in `dag.py`: `PROJECT`, `DATASET`, `LOCATION`.
2. Credentials come from the `google_cloud_platform` Connection / ADC — see
   [`examples/gcp_gcs_load`](../../gcp_gcs_load/) for the auth modes. `connectors: [gcp]`
   installs the provider.

## Run

```bash
# Lite (local): host ADC via `gcloud auth application-default login`
dexaflow lite --executor=subprocess examples/gcp/bigquery_chain
```

`second_job`'s query embeds the real job id produced by `first_job`, proving the
chaining wire end to end.
