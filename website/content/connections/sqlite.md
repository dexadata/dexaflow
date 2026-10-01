---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/sqlite.html
# --- end AUTO redirect aliases ---
title: SQLite connection
linkTitle: SQLite
weight: 490
description: SQLite connection
---

Connect a task to a sqlite database file. Unlike the SQL-family entries
(postgres, mysql, mssql), sqlite has no server — the database is a single
file on disk. This page documents what makes the connector different.

## URI shape

```
sqlite:///<absolute file path>
```

Three slashes total: two for the `scheme://` separator and one for the
leading `/` of an absolute path. Examples:

| File path | URI |
|---|---|
| `/var/lib/leoflow/warehouse.db` | `sqlite:///var/lib/leoflow/warehouse.db` |
| `/tmp/demo.db` | `sqlite:///tmp/demo.db` |
| (relative path — avoid) | `sqlite://warehouse.db` ← parses, but `urlparse(...).path` returns `""` for the host-less 2-slash form. Use an absolute path. |

The control plane builds this URI from the Connection's **Schema** field
(which is, by sqlite convention, the file path). There is no host, port,
login, or password.

## Fields the UI asks for

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `sqlite_target`. Exported as `AIRFLOW_CONN_SQLITE_TARGET`. |
| Conn Type | yes | `sqlite`. |
| Schema | yes | The **absolute path** to the database file. |
| Host | no | Leave blank. |
| Login | no | Leave blank. |
| Password | no | Leave blank. There is nothing to encrypt; the cipher gate still applies (#142). |
| Port | no | Leave blank. |
| Extra | optional | JSON for connect-time pragmas, e.g. `{"timeout":5,"detect_types":"PARSE_DECLTYPES"}`. The DAG passes these to `sqlite3.connect`. |

## The path-extraction pattern

`sqlite3.connect()` takes a path string, NOT a URI. The user task must
parse the URI itself and pull `parsed.path`:

```python
import os, sqlite3
from urllib.parse import urlparse

raw = os.environ["AIRFLOW_CONN_SQLITE_TARGET"]
path = urlparse(raw).path
if not path:
    raise ValueError(f"sqlite URI has no path: {raw!r}")
conn = sqlite3.connect(path)
```

Unlike the SQL family there is no `unquote` step — paths are not
percent-escaped.

## Example DAG

[`examples/sqlite_load`](https://github.com/dexadata/leoflow/tree/main/examples/sqlite_load) uses only Python's standard-library
`sqlite3`. The example's
[README](https://github.com/dexadata/leoflow/tree/main/examples/sqlite_load/README.md)
walks through Connection setup and verification.

## Lite vs Pro caveats

- **Lite (subprocess)** runs the task on the host. The path resolves
  against the host filesystem — a writable temp dir is fine for demos.
- **Lite (k3d)** runs the task in a pod. The path resolves inside the
  pod; the file disappears when the pod exits unless you mount a
  hostPath or a PVC.
- **Pro (Kubernetes)** same as k3d: persistence requires a volume. For
  shared state across pods, sqlite is the wrong tool — use a real
  service (postgres / mysql) instead.

## Why no encryption / no password

sqlite's file-system permissions are the access control. If the task can
read the file, it can read the data; if the file mode says `0600` and
the file is owned by the running user, only that user can open it. The
URI carries no secret, so the Repository's cipher does not actually
encrypt anything for a sqlite Connection — but it must still be
configured (the SetConnection gate refuses writes without a cipher; the
gate's purpose is "never store a credential in plaintext", and for
sqlite there is no credential to store).

For at-rest encryption of the sqlite **file itself**, use SQLCipher
(`pip install sqlcipher3-binary`) and pass the key via Extra. That is
out of scope for the cookbook entry but a documented pattern in the
SQLCipher project.

## Tier 1 integration test

`TestSQLiteConnectionURIShapeIntegration` (in `internal/storage/`)
covers this entry. It runs on every PR with no service container —
sqlite is a library, not a service, so the Tier 1 cost is zero (see
[#162](https://github.com/dexadata/leoflow/issues/162)).

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `OperationalError: unable to open database file` | Path missing or unwritable | Use an absolute path; ensure the pod / process can write to it. |
| `urlparse(...).path` is empty | The Schema field was left blank, or the URI uses the 2-slash form | Use an absolute path in Schema (starts with `/`). |
| `database is locked` | Multiple concurrent writers | Pick a different DB; sqlite serializes writes by design. |

## Related

- ADR 0019 — secret encryption at rest (mostly a no-op for sqlite).
- ADR 0021 — agent secret delivery.
- #70 — sqlite connector umbrella.
- #138 — chain-of-custody contract test (sqlite gets a dedicated test).
- #162 — tiered integration-test pipeline (sqlite is Tier 1).
