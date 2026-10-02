"""http_operator — HttpOperator hitting a public API.

Dexaflow compiles an HttpOperator to an 'airflow_operator' task that runs in its
own task pod (ADR 0040), like any other provider operator — declare
`connectors: [http]` so the provider is installed in the image. (The old native
inline 'http_api' type, which ran the request in the control plane, was removed —
ADR 0047/0048.) The endpoint here is a full URL, so no Connection is needed.
"""
from __future__ import annotations

from airflow.providers.http.operators.http import HttpOperator
from airflow.sdk import DAG

with DAG("http_operator", schedule=None, catchup=False, tags=["example"]):
    HttpOperator(
        task_id="get_todo",
        method="GET",
        endpoint="https://jsonplaceholder.typicode.com/todos/1",
    )
