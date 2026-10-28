# dags/ — your DAG projects

This is the conventional home for **your** Dexaflow DAG projects, kept separate
from `examples/` (which are reference/demo DAGs maintained with the product).

Each subdirectory is one DAG project (one image per DAG, ADR 0003):

```
dags/
  hello/
    dag.py         # Airflow SDK 3.2.x DAG (TaskFlow or operators)
    dexaflow.yaml   # deploy config: dag_id, deps, per-task overrides (ADR 0023)
```

## Create a new project
```
dexaflow init dags/my_pipeline
```

## Run one locally with hot reload
```
dexaflow lite dags/hello                        # k3d if Docker is present, else the host
dexaflow lite --executor=k8s dags/hello         # real pods on an isolated k3d cluster
dexaflow lite --executor=subprocess dags/hello  # fast host loop, unsandboxed
```
Open the LITE-marked UI at http://localhost:8088.

Generated artifacts (`Dockerfile`, `dag.json`) are git-ignored so your project
stays clean — Dexaflow regenerates them on demand.
