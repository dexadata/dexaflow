"""redis_load — compute a small payload and write it into a Redis hash.

The target URI comes from a managed Dexaflow Connection injected as
AIRFLOW_CONN_REDIS_TARGET (create it in Admin -> Connections); falls back to a
local URI for a quick run on a developer machine.

Unlike the SQL-family connectors, Redis has no schemas/tables: data is
key-value with optional hash / set / list types. This example uses a hash so
the verification step (``redis-cli HGETALL dexaflow:example_load``) is one
command.
"""
from __future__ import annotations

import os

from airflow.sdk import DAG, task


@task
def compute() -> dict[str, str]:
    rows = {f"cat_{i}": str((i * 7) % 100) for i in range(20)}
    print(f"compute: {len(rows)} keys")
    return rows


@task
def load(payload: dict[str, str]) -> None:
    import redis

    uri = os.environ.get("AIRFLOW_CONN_REDIS_TARGET") or os.environ.get(
        "REDIS_TARGET_URI", "redis://host.k3d.internal:56379/0"
    )
    src = "managed Connection redis_target" if os.environ.get("AIRFLOW_CONN_REDIS_TARGET") else "fallback URI"
    print(f"load: connecting via {src}")
    client = redis.Redis.from_url(uri, decode_responses=True)
    key = "dexaflow:example_load"
    client.delete(key)
    client.hset(key, mapping=payload)
    count = client.hlen(key)
    print(f"load: {count} fields in {key}")


with DAG("redis_load", schedule=None, catchup=False, tags=["example"]):
    load(compute())
