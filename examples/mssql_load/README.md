# mssql_load — write rows into an external SQL Server via a managed Connection

This example is a **test DAG** for the Microsoft SQL Server connector. Same
shape as [`postgres_load`](../postgres_load/README.md) and
[`mysql_load`](../mysql_load/README.md) — it just exercises the `mssql://`
URI path.

## What it tests

1. Admin creates an `mssql` Connection in the UI.
2. The control plane encrypts and stores it (ADR 0019).
3. The agent fetches the URI via gRPC and exports it as
   `AIRFLOW_CONN_MSSQL_TARGET`.
4. The user task `load()` parses the URI with `urllib.parse`, opens a real
   `pymssql` connection, and writes 20 rows.
5. You can verify the rows exist in the target SQL Server.

Like PyMySQL, `pymssql.connect()` takes kwargs (not a URI), so the DAG
parses the URI itself. See `docs/connections/mssql.md` for the gotcha and
the alternative drivers (pyodbc, SQLAlchemy).

## How to run it (Lima / subprocess executor)

### 1. Spin up a target SQL Server

```sh
docker run --rm -d --name leoflow-warehouse-mssql \
  -e ACCEPT_EULA=Y \
  -e MSSQL_SA_PASSWORD='Etl@1234' \
  -p 51433:1433 \
  mcr.microsoft.com/mssql/server:2022-latest
```

SQL Server takes ~30 s to initialise the system DBs on first run. Then
create the `warehouse` database:

```sh
docker exec leoflow-warehouse-mssql \
  /opt/mssql-tools18/bin/sqlcmd -No -S localhost -U sa -P 'Etl@1234' \
  -Q "CREATE DATABASE warehouse"
```

(`-No` skips the TLS prompt the 2022 image asks for.)

### 2. Create the Connection in the UI

Open `http://localhost:8088` → **Admin → Connections → +**.

| Field | Value |
|---|---|
| Conn Id | `mssql_target` |
| Conn Type | `mssql` |
| Host | `localhost` (host) or `host.k3d.internal` (k3d) |
| Schema | `warehouse` |
| Login | `sa` |
| Password | `Etl@1234` (notice the `@`) |
| Port | `51433` |

The `@` in the password is the point — it must percent-escape through the
URI builder (`%40`) and come back via `unquote` to `@` when the task runs.
A regression would surface as an authentication failure here.

### 3. Trigger the DAG

```sh
dexaflow lite path/to/this/example
```

In the UI: open `mssql_load` → **Trigger DAG**.

### 4. Verify

```sh
docker exec leoflow-warehouse-mssql \
  /opt/mssql-tools18/bin/sqlcmd -No -S localhost -U sa -P 'Etl@1234' \
  -d warehouse -Q "SELECT COUNT(*) FROM example_load;"
```

Expected: 20.

## Related

- `docs/connections/mssql.md` — cookbook entry for SQL Server.
- `examples/postgres_load/`, `examples/mysql_load/` — sibling examples.
- ADR 0021 — agent secret delivery.
- Issue #138 — chain-of-custody contract test (now covers mssql).
- Issue #71 — MSSQL connector umbrella.
