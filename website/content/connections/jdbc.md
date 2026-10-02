---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/jdbc.html
# --- end AUTO redirect aliases ---
title: JDBC connection
linkTitle: JDBC
weight: 260
description: JDBC connection
---

Connect a task to any database that ships a JDBC driver (DB2, Oracle,
SAP HANA, Vertica, …) over a managed Dexaflow Connection. `JdbcHook` runs
queries through a JVM driver loaded via JayDeBeApi.

## Declare the provider

```yaml
# dexaflow.yaml
dag_id: jdbc_query
connectors:
  - jdbc
```

## URI shape

```
jdbc://<login>:<password>@<host>:<port>
```

The control plane builds this from the Connection's host/port/login/
password and exports it as `AIRFLOW_CONN_<CONN_ID>`. Reserved characters in
the password are percent-escaped; `JdbcHook` un-escapes them.

**Important:** the credentials and host:port travel in the URI, but the
**driver location, driver class, and the actual JDBC connect URL** live in
`Extra`. The delivery test pins only the credential round-trip; the
`Extra` fields below are what make a real connection work.

## Fields the UI asks for

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `jdbc_default`. Exported as `AIRFLOW_CONN_JDBC_DEFAULT`. |
| Conn Type | yes | `jdbc`. |
| Host | yes | Database host, e.g. `db.example.com`. |
| Port | yes | Driver-specific, e.g. `5432`, `1521`, `50000`. |
| Login | yes | Database user. |
| Password | yes | Encrypted at rest (ADR 0019). |
| Extra | yes | JSON with `driver_path`, `driver_class`, and a full JDBC URL — see below. |

`Extra` example:

```json
{
  "driver_path": "/opt/drivers/postgresql.jar",
  "driver_class": "org.postgresql.Driver",
  "connection_url": "jdbc:postgresql://db.example.com:5432/warehouse"
}
```

## Example DAG

```python
# dag.py
from airflow.sdk import DAG, task


@task
def fetch_one():
    from airflow.providers.jdbc.hooks.jdbc import JdbcHook

    hook = JdbcHook(jdbc_conn_id="jdbc_default")
    rows = hook.get_records("SELECT 1")
    print("result:", rows[0][0])


with DAG("jdbc_query", schedule=None, catchup=False, tags=["example"]):
    fetch_one()
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: jdbc_query
python_version: "3.12"
connectors:
  - jdbc
dependencies:
  - JPype1
connections:
  - jdbc_default
```

The runtime image must also bundle a JRE/JDK and the driver `.jar`
referenced by `driver_path`.

## Security notes

- **TLS**: most JDBC drivers take TLS flags in the `connection_url`
  (e.g. `?ssl=true&sslmode=verify-full`). Set them there, not in plaintext.
- Never log `AIRFLOW_CONN_JDBC_DEFAULT`; it carries the password.

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery (`AIRFLOW_CONN_<CONN_ID>`).
- `TestJdbcConnectionURIShapeIntegration` — chain-of-custody delivery test.
