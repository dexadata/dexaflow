# mysql_load — write rows into an external MySQL/MariaDB via a managed Connection

This example is a **test DAG** for the MySQL/MariaDB connector. The same
shape as
[`postgres_load`](../postgres_load/README.md) — it just exercises the
`mysql://` URI path instead of `postgres://`.

## What it tests

1. Admin creates a `mysql` (or `mariadb`) Connection in the UI.
2. The control plane encrypts and stores it (ADR 0019).
3. The agent fetches the URI via gRPC and exports it as
   `AIRFLOW_CONN_MY_DB`.
4. The user task `load()` parses the URI with `urllib.parse`, opens a
   real `pymysql` connection, and writes 20 rows.
5. You can verify the rows exist in the target MySQL/MariaDB.

## Why parse the URI (the PyMySQL gotcha)

`pymysql.connect()` accepts kwargs (`host=`, `port=`, `user=`,
`password=`, `database=`), **not** a URI string. The DAG therefore
parses `AIRFLOW_CONN_MY_DB` with `urllib.parse.urlparse` and un-quotes
the password (the URI builder percent-escapes reserved characters so
`p@ss` becomes `p%40ss`; the un-quote brings it back).

This pattern applies to most non-postgres SQL connectors; the cookbook
page at `docs/connections/mysql.md` covers it.

## How to run it (Lima / subprocess executor)

### 1. Spin up a target MySQL

```sh
docker run --rm -d --name dexaflow-warehouse-mysql \
  -e MYSQL_ROOT_PASSWORD=etl \
  -e MYSQL_DATABASE=warehouse \
  -p 53306:3306 \
  mysql:8
```

Wait ~10 s for the server to initialise (first run is slow).

### 2. Create the Connection in the UI

Open `http://localhost:8088` → **Admin → Connections → +**.

| Field | Value |
|---|---|
| Conn Id | `my_db` |
| Conn Type | `mysql` |
| Host | `localhost` (host) or `host.k3d.internal` (k3d) |
| Schema | `warehouse` |
| Login | `root` |
| Password | `etl` |
| Port | `53306` |

Save.

### 3. Trigger the DAG

```sh
dexaflow lite path/to/this/example
```

In the UI: open `mysql_load` → **Trigger DAG**.

### 4. Verify

```sh
docker exec dexaflow-warehouse-mysql \
  mysql -uroot -petl -D warehouse \
  -e "SELECT COUNT(*), MIN(name), MAX(score) FROM example_load;"
```

Expected: 20 rows. `MIN(name)` is `cat_0`, scores range 0–99.

## MariaDB

Identical, but use `conn_type=mariadb` and `mariadb:11` for the Docker
image. The URI scheme becomes `mariadb://` and `pymysql` connects to
either equally.

## Related

- `docs/connections/mysql.md` — cookbook entry for MySQL/MariaDB.
- `examples/postgres_load/` — the sibling Postgres example.
- ADR 0021 — agent secret delivery.
- Issue #138 — chain-of-custody contract test (covers mysql + mariadb).
- Issue #69 — MySQL connector umbrella.
