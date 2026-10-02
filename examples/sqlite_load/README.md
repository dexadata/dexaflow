# sqlite_load — write rows into a sqlite file via a managed Connection

This example is a **test DAG** for the sqlite connector. Unlike the other
SQL-family cookbook entries (postgres, mysql, mssql), sqlite has no
server, no port, no login, and no password — the database is a single
file on disk. This entry validates that the Connection delivery path
handles a "no-credentials, file-path-only" shape correctly.

## What it tests

1. Admin creates a `sqlite` Connection in the UI; the **Schema** field
   carries the absolute file path.
2. The control plane stores it (no encryption needed since there is no
   password, but the cipher gate still applies).
3. The agent fetches the URI via gRPC and exports it as
   `AIRFLOW_CONN_SQLITE_TARGET=sqlite:///path/to/db`.
4. The user task `load()` parses the URI with `urllib.parse.urlparse`,
   takes `.path`, and opens it with `sqlite3.connect`.
5. You can verify the rows exist in the resulting sqlite file.

This example uses **only the Python standard library** (`sqlite3`) —
no third-party driver to install.

## How to run it (Lima / subprocess executor)

### 1. Pick a path for the sqlite file

The file does not need to exist; `sqlite3.connect` creates it on first
write. Pick something writable by the task:

- Subprocess executor: `/tmp/leoflow_warehouse.db` (the OS temp dir).
- k3d executor: the file lives inside the pod and disappears when it
  exits. Use a hostPath mount or a PVC if you need persistence.

### 2. Create the Connection in the UI

Open `http://localhost:8088` → **Admin → Connections → +**.

| Field | Value |
|---|---|
| Conn Id | `sqlite_target` |
| Conn Type | `sqlite` |
| Schema | `/tmp/leoflow_warehouse.db` (the absolute path) |
| Host | *(leave blank)* |
| Login | *(leave blank)* |
| Password | *(leave blank)* |
| Port | *(leave blank)* |

Save. The resulting `AIRFLOW_CONN_SQLITE_TARGET` env var will be
`sqlite:///tmp/leoflow_warehouse.db`.

### 3. Trigger the DAG

```sh
dexaflow lite path/to/this/example
```

In the UI: open `sqlite_load` → **Trigger DAG**.

### 4. Verify

```sh
sqlite3 /tmp/leoflow_warehouse.db "SELECT COUNT(*), MIN(name), MAX(score) FROM example_load;"
```

Expected: 20 rows. `MIN(name)` is `cat_0`, scores range 0–99.

## What can go wrong

- **The Schema field is empty** → the URI builder emits `sqlite://` with
  no path, and `urlparse(...).path` is `""`. The DAG raises ValueError
  with a clear message.
- **The path is relative** (e.g. `warehouse.db`) → sqlite resolves it
  relative to the task's CWD, which is the agent's working directory.
  Use an absolute path for predictable behavior.
- **The pod cannot write to the path** (k3d, read-only filesystem,
  PV permission) → sqlite raises `OperationalError: unable to open
  database file`. Mount a writable volume.

## Why this entry has no service container

sqlite is a **library**, not a service. The integration test
(`TestSQLiteConnectionURIShapeIntegration` in `internal/storage/`) runs
on every PR without needing a Docker container — this entry is **Tier 1**
in the [tiered pipeline](https://github.com/dexadata/dexaflow/issues/162).

## Related

- `docs/connections/sqlite.md` — cookbook entry.
- ADR 0021 — agent secret delivery.
- Issue #138 — chain-of-custody contract test (covers sqlite via a
  separate test, not the table-driven SQL-family one).
- Issue #70 — sqlite connector umbrella.
- Issue #162 — tiered integration-test pipeline.
