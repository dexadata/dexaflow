# postgres_load — write rows into an external Postgres via a managed Connection

This example is a **test DAG**: it exercises the full connection-delivery
contract end-to-end, from the Admin → Connections UI to user Python reading
the env var and connecting. Running it against a real target Postgres is the
manual companion to the Go-side integration test
(`TestConnectionDeliveryChainOfCustodyIntegration`).

## What it tests

1. Admin creates a `postgres` Connection in the UI.
2. The control plane encrypts and stores it (ADR 0019).
3. The agent fetches the URI via gRPC and exports it as
   `AIRFLOW_CONN_PG_TARGET`.
4. The user task `load()` reads the env var, opens a real `psycopg2`
   connection, and writes 20 rows into the target.
5. You can verify the rows exist in the target Postgres.

Without a Connection, `load()` falls back to a hardcoded local DSN so the
example also runs in a quick demo on a developer machine.

## How to run it (Lima / subprocess executor)

### 1. Spin up a target Postgres

```sh
docker run --rm -d --name leoflow-warehouse \
  -e POSTGRES_PASSWORD=etl \
  -e POSTGRES_DB=warehouse \
  -p 55432:5432 \
  postgres:16
```

The DAG defaults to `postgresql://postgres:etl@host.k3d.internal:55432/warehouse`
which works inside a k3d cluster. From the host or via subprocess, use
`localhost:55432`.

### 2. Create the Connection in the UI

Open `http://localhost:8088` → **Admin → Connections → +**.

| Field | Value |
|---|---|
| Conn Id | `pg_target` |
| Conn Type | `postgres` |
| Host | `localhost` (host) or `host.k3d.internal` (k3d) |
| Schema | `warehouse` |
| Login | `postgres` |
| Password | `etl` |
| Port | `55432` |

Save. The UI never shows the password again — it is encrypted at rest.

### 3. Trigger the DAG

```sh
dexaflow lite path/to/this/example
```

In the UI: open `postgres_load` → **Trigger DAG**.

### 4. Verify

```sh
docker exec leoflow-warehouse psql -U postgres -d warehouse \
  -c "SELECT count(*), min(name), max(score) FROM example_load"
```

Expected: 20 rows. `min(name)` is `cat_0`, scores range 0–99.

The task logs in the UI also report
`load: connecting via managed Connection pg_target` (vs the fallback DSN
banner if the env var is missing). That single log line is the visible
contract.

## What can go wrong

- **AIRFLOW_CONN_PG_TARGET not set** → the DAG falls back to the hardcoded
  DSN. If that DSN does not match your target, the connect fails. The log
  line tells you which path was taken.
- **Password with reserved characters** (`@`, `:`, `/`, `?`, `#`) — should
  work; the URI builder percent-escapes them and `psycopg2` un-escapes back.
  The integration test
  (`TestConnectionDeliveryChainOfCustodyIntegration`) pins this; if a real
  run still breaks, file an issue with the password shape that triggered it.
- **Connection lost between runs** — Connections persist across
  `dexaflow lite` restarts (they live in the managed Postgres). They survive
  `dexaflow uninstall` (without `--purge`).

## Related

- `docs/connections/postgres.md` — the cookbook entry for the postgres
  connector (URI shape, supported drivers).
- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery (`AIRFLOW_VAR_<KEY>` /
  `AIRFLOW_CONN_<CONN_ID>`).
- Issue #138 — the contract test this example documents.
