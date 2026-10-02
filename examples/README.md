# Example DAGs

Twenty reference projects covering every supported task type, operator pattern,
and connector. Each directory ships:

- `dag.py` — the DAG, written against the Airflow SDK 3.2.x
- `dexaflow.yaml` — Dexaflow's deploy config (python version, dependencies,
  per-task overrides)
- `Dockerfile` — the DAG image (#318); kept in sync with `dexaflow.yaml` by
  `scripts/sync-example-dockerfiles.sh`

A handful also ship `README.md` files (the connector cookbook entries plus
`lifecycle` and `http_load` — those are the canonical templates for adding
prose to the others).

## Lite — the dev loop

```sh
dexaflow lite examples/<name>/      # hot-reload at http://localhost:8088
```

`dexaflow lite` watches the workspace, synthesizes the Dockerfile if one is
missing (the script-generated ones in this tree mirror what `lite` would
produce — checking them in lets a Pro operator copy the same file verbatim
without running Lite), builds the image into the local k3d cluster (or runs
the DAG in-process under `--executor=subprocess`), and re-registers the DAG
on every save.

## Pro — the deploy loop

```sh
# 1. Compile + build the DAG image. The image tag must be a registry your
#    cluster can pull from.
dexaflow compile examples/<name>/ \
  --image my-registry.example.com/<name>:<tag> --build --push -o dag.json

# 2. Register the compiled artifact with the control plane.
dexaflow push dag.json --server $DEXAFLOW_SERVER --token $DEXAFLOW_TOKEN

# 3. Trigger from the UI, or:
curl -X POST -H "Authorization: Bearer $DEXAFLOW_TOKEN" -H 'Content-Type: application/json' \
  -d '{}' "$DEXAFLOW_SERVER/api/v2/dags/<name>/dagRuns"
```

The Pro walkthrough — building locally vs Cloud Build, image pull secrets,
registry permissions — is in [`docs/deploy.md`](../docs/deploy.md). The
managed-Connection examples (`*_load`, `gcp_gcs_load`, `http_load`) each ship
a `README.md` walking through their specific Connection wiring.

## Index

| Example | Description |
|---|---|
| [api_chain](api_chain/) | Chain two public API calls — list users, then fetch one user's details. |
| [bash_pipeline](bash_pipeline/) | `BashOperator` tasks (the `bash` task type). |
| [csv_report](csv_report/) | Generate a CSV, compute a report from it, all in-task (no deps). |
| [duckdb_http_csv](duckdb_http_csv/) | DuckDB reads a remote CSV over HTTP and aggregates it (out-of-core). |
| [fan_out_aggregate](fan_out_aggregate/) | Fan-out to parallel workers then fan-in to an aggregate (map-reduce). |
| [gcp/dataform_trigger](gcp/dataform_trigger/) | Chained Google Dataform operators (`compile >> invoke`) — passes the compilation result via `{{ ti.xcom_pull('compile')['name'] }}` (ADR 0040). |
| [gcp/gcs_bucket](gcp/gcs_bucket/) | Google Cloud Storage operators — bucket lifecycle create → list → delete (ADR 0040). |
| [gcp/bigquery_query](gcp/bigquery_query/) | A single BigQuery operator querying a public dataset — the minimal operator example (ADR 0040). |
| [gcp/bigquery_chain](gcp/bigquery_chain/) | BigQuery operators with operator-to-operator XCom chaining via `ti.xcom_pull` (ADR 0040). |
| [gcp_gcs_load](gcp_gcs_load/) | Write + read a GCS object via a managed `google_cloud_platform` Connection (key or keyless/ADC). |
| [http_jsonplaceholder](http_jsonplaceholder/) | Fetch posts from a public JSON API (jsonplaceholder) and summarize. |
| [http_load](http_load/) | POST a payload to an external HTTP endpoint via a managed Connection, assert the echo round-trips. |
| [http_operator](http_operator/) | `HttpOperator` hitting a public API — runs in a pod as an `airflow_operator` (ADR 0040). |
| [lifecycle](lifecycle/) | Three-task pipeline (extract → transform → load) passing data via XCom — the canonical pod-per-task smoke. |
| [ml_hparam_search](ml_hparam_search/) | Parallel hyperparameter search with map-reduce aggregation (toy ML). |
| [montecarlo_pi](montecarlo_pi/) | Estimate pi with parallel Monte-Carlo workers, then combine. |
| [mssql_load](mssql_load/) | Compute rows and load them into an external Microsoft SQL Server via a managed Connection. |
| [mysql_load](mysql_load/) | Compute rows and load them into an external MySQL/MariaDB via a managed Connection. |
| [params_demo](params_demo/) | Declares typed run `params` (bare default, typed enum, required) so the trigger dialog renders its native typed form. |
| [postgres_load](postgres_load/) | Compute rows and load them into an external Postgres via a managed Connection. |
| [redis_load](redis_load/) | Compute a key-value payload and write it into a Redis hash via a managed Connection. |
| [sqlite_load](sqlite_load/) | Compute rows and load them into a sqlite file via a managed Connection. |
| [taskflow_sales](taskflow_sales/) | Pure TaskFlow ETL passing data via XCom (no external deps). |
| [weather_open_meteo](weather_open_meteo/) | Fetch current weather from the public Open-Meteo API (no key) per city. |
| [xcom_typed](xcom_typed/) | Pass typed dict payloads across tasks via XCom with validation. |

## Adding a new example

1. Create `examples/<name>/dag.py` + `dexaflow.yaml` (use any existing example
   as a template; `dexaflow init examples/<name>` scaffolds the pair).
2. Run `bash scripts/sync-example-dockerfiles.sh` to generate the Dockerfile.
3. (Optional) Add a `README.md` describing what the example exercises.
4. Add a row to the index table above.

CI runs `scripts/sync-example-dockerfiles.sh --check` so a Dockerfile out of
sync with `dexaflow.yaml` fails the build — change the YAML, re-run the script,
commit the regenerated Dockerfile in the same change.
