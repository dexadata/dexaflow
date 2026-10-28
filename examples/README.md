# Example DAGs

Reference projects covering every supported task type, operator pattern, and
connector. Each directory ships:

- `dag.py`: the DAG, written against the Airflow SDK 3.2.x
- `dexaflow.yaml`: Dexaflow's deploy config (python version, dependencies,
  connectors, per-task overrides)

There is no `Dockerfile`. The DAG image is generated from `dexaflow.yaml` when
it is built, `FROM` the published runtime base, so what the yaml declares is
what the image contains. A `Dockerfile` placed next to `dag.py` would be used
verbatim instead and the yaml's dependencies would stop reaching the image, so
the examples never ship one (`internal/cli/examples_dockerfile_test.go` fails
if one appears).

A handful also ship `README.md` files (the connector cookbook entries plus
`lifecycle` and `http_load`; those are the canonical templates for adding
prose to the others).

## Lite: the dev loop

```sh
dexaflow lite examples/<name>/      # hot-reload at http://localhost:8088
```

`dexaflow lite` watches the workspace, builds the image into the local k3d
cluster (or runs the DAG in-process under `--executor=subprocess`), and
re-registers the DAG on every save.

The k3d mode writes a `Dockerfile` into the example directory, `FROM` the
`leoflow-base` image Lite builds on your machine, and leaves it there. Delete it
before a Pro deploy: a `Dockerfile` in the directory is built verbatim, and that
base exists on no other builder.

## Pro: the deploy loop

A Pro deploy pushes the DAG image to a registry your cluster can pull from.
Each example's `dexaflow.yaml` ends with a commented `registry:` block, since
the registry is yours: uncomment it, or leave it as it is when the target names
the registry (`--registry` on `dexaflow target add`, below) or the cluster
advertises one in its deploy defaults.

The CLI needs a credential for that registry. For Artifact Registry, ECR and
ACR, being signed in to the `gcloud`, `aws` or `az` CLI is enough while the
image is built without a daemon (with `uv` on `PATH`); for any other registry,
or when the local `docker` builds, sign in with the command
[Registry credentials](https://dexaflow.dexadata.ai/operate/builders/#registry-credentials)
names (`docker login <registry>` for most). Then add the control plane as a
target, sign in to it and deploy:

```sh
dexaflow target add prod https://pro.example.com --registry ghcr.io/your-org
dexaflow auth login --target prod
dexaflow deploy examples/<name>/ --active --trigger --follow   # compile, build, push, pin by digest, register, run
```

A new DAG starts paused, as in Airflow, and a run triggered while it is paused
waits in `queued`. `--active` unpauses it in the same register, and `--follow`
prints the run's task states and logs and exits with its result.

The full walkthrough is [Deploy your first Pro DAG](https://dexaflow.dexadata.ai/operate/first-pro-dag/),
and the pipeline recipes are in [CI/CD & deploy](https://dexaflow.dexadata.ai/operate/cicd-deploy/).
The managed-Connection examples (`*_load`, `gcp_gcs_load`, `http_load`) each
ship a `README.md` walking through their specific Connection wiring, and the
connection a DAG expects has to exist on the control plane before it runs.

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
   as a template; `dexaflow init examples/<name>` scaffolds the pair). Declare
   every package the DAG imports in `dependencies:` or `connectors:`.
2. (Optional) Add a `README.md` describing what the example exercises.
3. Add a row to the index table above.
