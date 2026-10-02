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
leoflow dev dags/hello          # cluster-mode (real pods, isolated k3d)
leoflow dev --executor=subprocess dags/hello   # fast host loop
```
Open the DEV-marked UI at http://localhost:8088.

Generated artifacts (`Dockerfile`, `dag.json`) are git-ignored so your project
stays clean — Dexaflow regenerates them on demand.
