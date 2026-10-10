---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/mssql.html
# --- end AUTO redirect aliases ---
title: Microsoft SQL Server connection
linkTitle: Microsoft SQL Server
weight: 310
description: Microsoft SQL Server connection
---

Connect a task to an external Microsoft SQL Server (Azure SQL, on-prem
instance, or a Docker `mcr.microsoft.com/mssql/server` container) over a
managed Dexaflow Connection.

## URI shape

```
mssql://<login>:<password>@<host>:<port>/<database>
```

The control plane builds this URI from the Connection's fields. Reserved
characters in the password are percent-escaped (the `@` in a strong SQL
Server password becomes `%40`); the user-side Python must un-escape on
the way out. See the gotcha below.

## Fields the UI asks for

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `mssql_target`. Exported as `AIRFLOW_CONN_MSSQL_TARGET`. |
| Conn Type | yes | `mssql`. |
| Host | yes | DNS name or IP. For Azure SQL: `<server>.database.windows.net`. |
| Schema | yes | The database name (yes, Airflow calls it "schema"; SQL Server calls it "database"). |
| Login | yes | The SQL user (`sa`, `app_user`, …). Azure AD logins look like `user@tenant`. |
| Password | yes | Stored encrypted at rest (ADR 0019). SQL Server's strong-password policy means special chars are typical. |
| Port | optional | Defaults to `1433`. |
| Extra | optional | JSON: `{"encrypt":"true","TrustServerCertificate":"false"}` for TLS with cert verification. |

## The pymssql gotcha

`pymssql.connect()` accepts kwargs (`server=`, `port=`, `user=`,
`password=`, `database=`), **not** a URI string. The user task must
parse the URI itself:

```python
import os
from urllib.parse import unquote, urlparse
import pymssql

url = urlparse(os.environ["AIRFLOW_CONN_MSSQL_TARGET"])
conn = pymssql.connect(
    server=url.hostname,
    port=url.port or 1433,
    user=url.username or "",
    password=unquote(url.password or ""),
    database=(url.path or "").lstrip("/"),
)
```

The `unquote` is the critical step: the URI builder percent-escapes
reserved characters (`@` → `%40`); without `unquote`, pymssql would see
`Etl%401234` instead of `Etl@1234` and authentication would fail.

## Alternative drivers

- **pyodbc**: the most common production driver. Build a connection
  string from the parsed URL:
  ```python
  cnxn_str = (
      f"DRIVER={{ODBC Driver 18 for SQL Server}};"
      f"SERVER={url.hostname},{url.port or 1433};"
      f"DATABASE={(url.path or '').lstrip('/')};"
      f"UID={url.username};PWD={unquote(url.password)};"
      "Encrypt=yes;TrustServerCertificate=no"
  )
  cnxn = pyodbc.connect(cnxn_str)
  ```
  Requires the ODBC driver installed in the image
  (`apt install msodbcsql18`).
- **SQLAlchemy** accepts the URL with the right dialect:
  `create_engine("mssql+pyodbc://...")` for pyodbc, or
  `create_engine("mssql+pymssql://...")` for pymssql. SQLAlchemy
  handles the un-escape automatically.

## Example DAG

[`examples/mssql_load`](https://github.com/dexadata/dexaflow/tree/main/examples/mssql_load) reads `AIRFLOW_CONN_MSSQL_TARGET`, opens a
`pymssql` connection, and writes 20 rows. The example's
[README](https://github.com/dexadata/dexaflow/tree/main/examples/mssql_load/README.md)
walks through Docker spin-up, Connection setup (with a password
containing `@`), and verification.

## Lite vs Pro caveats

- **Lite (subprocess)** runs the task on the host. The Connection's
  `host` resolves against the host's name lookup.
- **Lite (k3d)** runs the task in a pod. Use `host.k3d.internal` to
  reach a host-bound port.
- **Pro (Kubernetes)** runs the task in a pod. The target SQL Server
  must be reachable via cluster DNS, an Azure-managed PrivateLink, or
  external DNS the pod's network policy permits.
- **Azure SQL specifics**: the host is `<server>.database.windows.net`,
  port is always `1433`, and `Extra` should set
  `{"encrypt":"true","TrustServerCertificate":"false"}` to force TLS
  with certificate verification. Azure AD logins look like
  `user@tenant`; that single `@` is enough to exercise the percent-escape
  path.

## Security notes

- **TLS in transit**: SQL Server 2017+ requires TLS by default. Set
  `Encrypt=yes;TrustServerCertificate=no` in pyodbc / pymssql kwargs.
- **MSSQL_SA_PASSWORD strength**: SQL Server's strong-password policy
  requires 8+ characters, at least 3 of: uppercase, lowercase, digits,
  special. Trivially-weak passwords (`password`, `etl`) are refused at
  container start.
- **SSPI / Kerberos**: not supported by `pymssql` cleanly; use pyodbc
  with the appropriate driver if you need it.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `Login failed for user 'sa'` with the right password | The password contains `@` (or other reserved chars) and you did NOT call `unquote` | Wrap with `urllib.parse.unquote(url.password)`. |
| `Adaptive Server connection failed` | Network/DNS issue or wrong port | From the task's pod / host, try `sqlcmd -S host,port` with the same coords. |
| `Cannot open database "warehouse" requested by the login` | The database does not exist | Create it: `sqlcmd -Q "CREATE DATABASE warehouse"`. |
| `SSL provider: The certificate chain was issued by an authority that is not trusted` | TLS with a self-signed cert + verify-on | For local dev only, set `TrustServerCertificate=yes`. Pro: trust the CA. |

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery.
- #71 — MSSQL connector umbrella.
- #138 — chain-of-custody contract test (covers `mssql`).
