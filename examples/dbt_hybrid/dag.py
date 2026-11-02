"""dbt_hybrid: Python tasks around an embedded dbt project."""
from __future__ import annotations

from airflow.sdk import DAG, task
from dexaflow import dbt_group


@task
def extract() -> int:
    # Stands in for the step that lands raw data; the dbt seed carries the rows.
    print("extract: orders are seeded by the dbt project")
    return 6


@task
def report(expected: int) -> None:
    print(f"report: dbt built the models over {expected} orders")


with DAG("dbt_hybrid", schedule=None, catchup=False, tags=["example", "dbt"]):
    rows = extract()
    models = dbt_group("transform")
    rows >> models >> report(rows)
