---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/mysql.html
# --- end AUTO redirect aliases ---
title: MySQL / MariaDB connection
linkTitle: MySQL / MariaDB
weight: 320
description: MySQL / MariaDB connection
---

Connect a task to an external MySQL or MariaDB over a managed Dexaflow
Connection. MariaDB uses the same `mysql` protocol, so the URI shape
and the Python driver are identical; only the `conn_type` differs
(`mysql` vs `mariadb`).

## URI shape

```
mysql://<login>:<password>@<host>:<port>/<schema>
```

The control plane builds this URI from the Connection's fields. The
percent-escaping is identical to the Postgres path — special characters
in the password become `%XX` sequences. The Python side must
un-escape: see "The PyMySQL gotcha" below.

## Fields the UI asks for

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `my_db`. Exported as `AIRFLOW_CONN_MY_DB`. |
| Conn Type | yes | `mysql` or `mariadb`. Both produce a `mysql://` (or `mariadb://`) URI. |
| Host | yes | DNS name or IP. From inside a k3d cluster use `host.k3d.internal` for a host-bound port. |
| Schema | yes | The database name. |
| Login | yes | The MySQL user (`root`, `app_user`, …). |
| Password | yes | Stored encrypted at rest (ADR 0019). |
| Port | optional | Defaults to `3306`. |
| Extra | optional | JSON object, e.g. `{"ssl":{"ca":"/etc/ssl/certs/ca.pem"}}` for TLS. |

## The PyMySQL gotcha

PyMySQL's `pymysql.connect()` accepts **kwargs**, not a URI string. The
user task must parse the URI itself:

```python
import os
from urllib.parse import unquote, urlparse
import pymysql

url = urlparse(os.environ["AIRFLOW_CONN_MY_DB"])
conn = pymysql.connect(
    host=url.hostname,
    port=url.port or 3306,
    user=url.username or "",
    password=unquote(url.password or ""),  # important: un-escape the percent encoding
    database=(url.path or "").lstrip("/"),
)
```

The `unquote` is the critical step: the URI builder percent-escapes
reserved characters (`@` → `%40`, `:` → `%3A`, etc.) so the URI parses
correctly; without `unquote`, PyMySQL would see `%40` instead of `@` in
the password and authentication would fail with a confusing error.

Alternative drivers:

- **SQLAlchemy** accepts the URL directly:
  `create_engine("mysql+pymysql://...")`. SQLAlchemy handles the
  un-escape automatically.
- **mysqlclient** (the C driver): same kwarg shape as PyMySQL; un-escape
  the password.
- **mysql-connector-python**: accepts kwargs OR a URI; un-escape recommended.

## Example DAG

[`examples/mysql_load`](https://github.com/dexadata/dexaflow/tree/main/examples/mysql_load) reads `AIRFLOW_CONN_MY_DB`, parses it, opens
a `pymysql` connection, and writes 20 rows. The example's
[README](https://github.com/dexadata/dexaflow/tree/main/examples/mysql_load/README.md)
walks through Docker spin-up, Connection setup, and verification.

## Lite vs Pro caveats

- **Lite (subprocess)** runs the task on the host. The Connection URI's
  `host` resolves against the host's name lookup — `localhost` works
  for a host-bound Docker container.
- **Lite (k3d)** runs the task in a pod. Use `host.k3d.internal` to
  reach a host-bound port.
- **Pro (Kubernetes)** runs the task in a pod. The target MySQL must be
  reachable via cluster DNS (a `Service`) or external DNS the pod's
  network policy permits.

## Security notes

- **TLS in transit**: pass an SSL config in **Extra**, e.g.
  `{"ssl":{"ca":"/etc/ssl/certs/ca.pem"}}`. PyMySQL accepts this via
  the `ssl=` kwarg. Re-pass it to `pymysql.connect()` in your task.
- **MariaDB**: identical wire protocol; choose `conn_type=mariadb` so
  the URI scheme reflects the database. The driver does not care.
- **MySQL 8 auth plugin**: `caching_sha2_password` is the default for
  MySQL 8 users; PyMySQL ≥1.1 supports it. Older drivers may need
  `mysql_native_password`.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `Access denied for user 'root'@'host' (using password: YES)` with the right password | The password contains percent-escaped chars and you did NOT call `unquote` | Wrap with `urllib.parse.unquote(url.password)` — see the gotcha above. |
| `Authentication plugin 'caching_sha2_password' cannot be loaded` | Old driver, MySQL 8 default | Upgrade PyMySQL to ≥1.1 or use `mysql_native_password` in the MySQL user. |
| `Can't connect to MySQL server on 'host' (111)` | Network/DNS issue | From the task's pod / host, `mysql 'mysql://...'` to isolate. |

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery.
- #69 — MySQL connector umbrella.
- #138 — chain-of-custody contract test (covers `mysql` + `mariadb`).
