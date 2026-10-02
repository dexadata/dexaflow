---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/spark.html
# --- end AUTO redirect aliases ---
title: Spark connection
linkTitle: Spark
weight: 480
description: Spark connection
---

Submit Spark jobs from a task via a managed Dexaflow Connection and the Apache
Spark provider hooks. The provider exposes a few conn types — all from
`apache-airflow-providers-apache-spark`:

| conn_type | Use | Hook |
|---|---|---|
| `spark` | spark-submit to a master | `SparkSubmitHook` |
| `spark_sql` | Spark SQL | `SparkSqlHook` |
| `spark_connect` | Spark Connect | `SparkConnectHook` |
| `spark_jdbc` | JDBC via Spark | `SparkJDBCHook` |

## Declare the provider

```yaml
# dexaflow.yaml
dag_id: spark_job
connectors:
  - spark
```

## Fields the UI asks for

| Field | Where it goes | Notes |
|---|---|---|
| Host | host | The master URL host, e.g. `spark-master.example.com`. |
| Port | port | e.g. `7077`. |
| Extra | extra | Tuning: `queue`, `deploy-mode`, `namespace`, `principal`, `keytab`. |

The host:port + Extra round-trip is pinned by
`TestSparkConnectionURIShapeIntegration`.

## Example DAG (copy-paste)

Docs-only recipe (needs a Spark cluster). The hook is imported **inside** the
task.

```python
# dag.py
from __future__ import annotations

from airflow.sdk import DAG, task


@task
def submit() -> None:
    from airflow.providers.apache.spark.hooks.spark_submit import SparkSubmitHook

    hook = SparkSubmitHook(conn_id="spark_default", application="/opt/jobs/etl.py")
    print("submit: spark-submit via SparkSubmitHook(spark_default)")
    hook.submit()
    print("submit: ok")


with DAG("spark_job", schedule=None, catchup=False, tags=["example"]):
    submit()
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: spark_job
description: Submit a Spark job via SparkSubmitHook.
owner: examples
tags:
  - example
python_version: "3.11"
connectors:
  - spark
```

### Run it

1. **Admin → Connections → +**, type `spark`. Set Host + Port (the master), and
   any Extra tuning.
2. `dexaflow lite path/to/this/dag` → trigger `spark_job`.

{{% alert title="spark-submit needs a JVM" color="info" %}}
`SparkSubmitHook` shells out to `spark-submit`, which needs a Spark/JVM in the
task image. Add it via `system_packages:` or a custom base image. The
Connection only carries where to submit, not the binary.
{{% /alert %}}

## Security notes

- **Keytab / principal stay in Extra**: for Kerberos-secured clusters keep
  `principal` and `keytab` in **Extra** (encrypted at rest, ADR 0019) — never
  inline them in the DAG source.
- **Authenticated submit endpoint**: submit to a master/Connect endpoint that
  enforces auth and TLS where the cluster supports it; the Connection only
  carries where to submit, not the transport policy.
- **Secrets in logs**: never `print()` the URI — it may carry credentials. Log
  the host + port only.
- **gRPC channel (agent ↔ control plane)**: secrets are served only over an
  authenticated channel (ADR 0021); Pro must run with TLS.

## Related

- `docs/connections/index.md` — *Installing a connector's provider*.
- ADR 0019 / 0021 — secret encryption + agent delivery.
