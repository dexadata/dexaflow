---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/mongo.html
# --- end AUTO redirect aliases ---
title: MongoDB connection
linkTitle: MongoDB
weight: 290
description: MongoDB connection
---

Connect a task to MongoDB via a managed Dexaflow Connection and `MongoHook`. The
conn_type is `mongo`. It is the host:port + login/password shape; the **Schema**
field carries the database (or auth database).

## Declare the provider

```yaml
# dexaflow.yaml
dag_id: mongo_load
connectors:
  - mongo
```

## Fields the UI asks for

| Field | Notes |
|---|---|
| Host | Mongo host (or the first of a replica set). |
| Port | Defaults to `27017`. |
| Login / Password | The Mongo user. Password encrypted at rest (ADR 0019). |
| Schema | The database, e.g. `analytics`. |
| Extra | `{"srv": true}` for `mongodb+srv://` (Atlas), `{"authSource": "admin"}`, TLS options. |

The host:port + login/password + db round-trip is covered by the table-driven
`TestConnectionDeliveryChainOfCustodyIntegration` (mongo row).

## Example DAG (copy-paste)

Docs-only recipe (needs a MongoDB instance). Hook imported **inside** the task.

```python
# dag.py
from __future__ import annotations

from airflow.sdk import DAG, task


@task
def load() -> None:
    from airflow.providers.mongo.hooks.mongo import MongoHook

    hook = MongoHook(mongo_conn_id="mongo_default")
    print("load: connecting via MongoHook(mongo_default)")
    coll = hook.get_collection("example_load", mongo_db="analytics")
    coll.delete_many({})
    coll.insert_many([{"name": f"cat_{i}", "score": (i * 7) % 100} for i in range(20)])
    print(f"load: {coll.count_documents({})} docs in example_load")


with DAG("mongo_load", schedule=None, catchup=False, tags=["example"]):
    load()
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: mongo_load
description: Load documents into MongoDB via MongoHook.
owner: examples
tags: [example]
python_version: "3.11"
connectors:
  - mongo
```

### Atlas (mongodb+srv)

For MongoDB Atlas set `Extra: {"srv": true}` and use the cluster host
(`cluster0.xxxx.mongodb.net`) without a port — `MongoHook` builds the
`mongodb+srv://` URI.

## Security notes

- **TLS in transit**: set TLS options in **Extra** (`{"tls": true}`), or use the
  `mongodb+srv://` form (`{"srv": true}`, Atlas) which negotiates TLS by default.
- **Least privilege**: scope the Mongo user to the target database and set
  `authSource` in **Extra** — avoid a cluster-wide admin user.
- **Secrets in logs**: never `print()` the URI — it carries the password. Log
  the host + database + login only.
- **gRPC channel (agent ↔ control plane)**: secrets are served only over an
  authenticated channel (ADR 0021); Pro must run with TLS.

## Related

- `docs/connections/index.md` — *Installing a connector's provider*.
- ADR 0019 / 0021 — secret encryption + agent delivery.
