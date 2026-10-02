---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /planning/airflow-connector-compatibility.html
# --- end AUTO redirect aliases ---
title: Airflow 3.X connector compatibility
linkTitle: Connector compatibility
weight: 10
description: "Planning note: Airflow 3.X connector compatibility."
---

> **Status:** planning document, not yet ADR 0036. The recommendation here is a
> proposal to feed ADR 0036 before any code lands. **Scope is exclusively Airflow
> 3.X (3.2.x line, `main` as of 2026-06-02). Airflow 2.x is out of scope.**
>
> **Goal:**
> - **Long-term:** Dexaflow stays architecturally independent of Apache Airflow
>   (no Airflow at runtime, no Flask/SQLAlchemy/Pendulum stack in our pods).
> - **Short-term:** existing Airflow 3.X DAGs that use connectors
>   (`from airflow.providers.postgres.hooks.postgres import PostgresHook`) run
>   on Dexaflow **unchanged**, so adoption costs nothing for current Airflow
>   users.
>
> Both goals are reachable simultaneously with a small Python shim. This
> document measures how small.

## The model in one picture — connection metadata and connector code never live together

Connection **metadata** (host, login, password, extra JSON) is owned by the
Dexaflow control plane: created in the admin UI, encrypted at rest (ADR 0019).
Connector **code** (`PostgresHook.get_records()`, `GCSHook.upload()`, …) is
owned by the user's DAG image: pip-installed from `apache-airflow-providers-<X>`
declared in `dexaflow.yaml.dependencies`. Dexaflow ships **no** provider code.

The two meet only at runtime, through the `AIRFLOW_CONN_<ID>` environment
variable (ADR 0021 wire format) that the Dexaflow agent stamps into the task
process. The Dexaflow runtime compat shim (ADR 0036) intercepts
`BaseHook.get_connection()` and returns the canonical Connection. The upstream
hook does the real work.

```text
┌─────────────────────────────────────────┐    ┌──────────────────────────────────┐
│  Admin UI / API  (Dexaflow control plane,│    │  dexaflow.yaml  (user, per-DAG)   │
│                   Go — ADR 0014)        │    │                                  │
│                                         │    │  dependencies:                   │
│  POST /api/v2/connections               │    │    - apache-airflow-providers-   │
│  {conn_id: "my_pg", type: "postgres",   │    │      postgres==6.0               │
│   host: "...", login: "...",            │    │    - psycopg2-binary==2.9        │
│   password: "...", extra: "{...}"}      │    │                                  │
└────────────────────┬────────────────────┘    └─────────────────┬────────────────┘
                     │                                            │
                     ▼                                            ▼
┌─────────────────────────────────────────┐    ┌──────────────────────────────────┐
│  Dexaflow DB (encrypted, ADR 0019)       │    │  DAG image (built once per push) │
│  connections:                           │    │  ─────────────                   │
│    id="my_pg" type="postgres"           │    │  leoflow-base:py3.11             │
│    password = AES-256-GCM(...)          │    │  + apache-airflow-providers-     │
│    extra    = AES-256-GCM(...)          │    │      postgres   (PostgresHook)   │
└────────────────────┬────────────────────┘    │  + leoflow-runtime-compat-shim   │
                     │                          │      (airflow.sdk.* shim,        │
                     │ on dispatch              │       ADR 0036)                  │
                     ▼                          └─────────────────┬────────────────┘
┌─────────────────────────────────────────┐                      │
│  Dexaflow agent (Go, in the pod or       │                      │
│  subprocess host)                       │                      │
│  - decrypts password + extra            │                      │
│  - renders the URI:                     │                      │
│    AIRFLOW_CONN_MY_PG=                  │                      │
│      postgres://login:pw@host:5432/db   │                      │
│      ?__extra__={"sslmode": ...}        │                      │
│    (ADR 0021 wire format)               │                      │
│  - injects env var into the task        │                      │
└────────────────────┬────────────────────┘                      │
                     │                                            │
                     └────────────────► task process ◄────────────┘
                                              │
                                              ▼
                ┌──────────────────────────────────────────────┐
                │  User DAG (Python)                           │
                │                                              │
                │  from airflow.providers.postgres.hooks       │
                │       .postgres import PostgresHook          │
                │  hook = PostgresHook(postgres_conn_id=        │
                │                      "my_pg")                │
                │  hook.get_records("SELECT 1")                │
                └─────────────────────┬────────────────────────┘
                                      │
                                      ▼ provider calls
                                      │ BaseHook.get_connection("my_pg")
                ┌──────────────────────────────────────────────┐
                │  Dexaflow runtime compat shim  (ADR 0036)     │
                │  ──────────────────────────                  │
                │  1. Read AIRFLOW_CONN_MY_PG env var          │
                │  2. Parse URI → host/login/password/port/    │
                │      schema/extra                            │
                │  3. Pre-processor (per-type policy seam)     │
                │     • cloud type → cloud resolver            │
                │       (ADR 0035 chain: keyfile_dict →        │
                │        key_path → key_secret_name → ADC)     │
                │       fetches Secret Manager NOW if          │
                │       key_secret_name is set; emits the      │
                │       fetched key as a transient keyfile_    │
                │       dict so the upstream hook understands. │
                │     • non-cloud type → pass through.         │
                │  4. Return a canonical                       │
                │      airflow.sdk.definitions.Connection      │
                └─────────────────────┬────────────────────────┘
                                      │
                                      ▼
                ┌──────────────────────────────────────────────┐
                │  Upstream PostgresHook                       │
                │  (apache-airflow-providers-postgres)         │
                │  - receives the canonical Connection         │
                │  - psycopg2.connect(...) → real query        │
                └──────────────────────────────────────────────┘
```

### What works out of the box vs what needs a provider declared

Two tiers of "Airflow imports that work on Dexaflow" — they have very
different dependency contracts, and the cookbook pages must state this in
the very first line.

| Tier | Import | Needs a provider in `dexaflow.yaml.dependencies`? | Runtime path |
|---|---|---|---|
| **A. Native (already shipped)** | `from airflow.sdk import DAG, task` | **No** | Parser maps to `python` / `bash` task types; Dexaflow runtime executes directly. |
| A. | `from airflow.providers.standard.operators.python import PythonOperator` | **No** | Same as above. |
| A. | `from airflow.providers.standard.operators.bash import BashOperator` | **No** | Same. |
| A. | `from airflow.providers.standard.operators.empty import EmptyOperator` | **No** | Same. |
| B. | `from airflow.providers.http.operators.http import HttpOperator` | **Yes — `apache-airflow-providers-http`** | Captured as `airflow_operator`; runs `.execute()` in a task pod (ADR 0040). The native inline `http_api` type was removed (ADR 0047). |
| **B. Compat (ADR 0036)** | `from airflow.providers.postgres.hooks.postgres import PostgresHook` | **Yes — `apache-airflow-providers-postgres` + `psycopg2-binary`** | Through the runtime compat shim. |
| B. | `from airflow.providers.google.cloud.hooks.gcs import GCSHook` | **Yes — `apache-airflow-providers-google`** | Shim + GCP resolver (ADR 0035). |
| B. | `from airflow.providers.http.hooks.http import HttpHook` | **Yes — `apache-airflow-providers-http`** | Shim. (Distinct from `HttpOperator` in tier A.) |
| B. | any other `from airflow.providers.<X>.hooks.<Y>...` | **Yes — the matching `apache-airflow-providers-<X>`** | Shim. |

Forgetting the dependency for a tier-B import is a fast, loud failure:
`ModuleNotFoundError: No module named 'airflow.providers.postgres'` at
import time, before any task work runs. The cookbook page for the hook
names the exact error so search engines route the user back to the right
recipe.

Out of scope (still rejected at compile time per the closed-set policy):
sensors, dynamic task mapping (`.expand` / `.partial`), `TaskGroup`,
branching operators, untyped operators outside the standard / http
providers. Parser fails fast with "not supported by Dexaflow".

## 0. TL;DR

| | Strategy A — **Shim Airflow core minimally**, use upstream providers | Strategy B — Re-implement hooks natively in Dexaflow | Strategy C — Allow user-installed `apache-airflow-providers-*` |
|---|---|---|---|
| **New code in Dexaflow** | ~1,200 LOC Python | ~4,000 LOC Python (top-15 hooks + base) | 0 |
| **Runtime image footprint** | +~50 MB (shim + per-provider SDK only) | +~10 MB (per-provider SDK only) | **+200-300 MB** (apache-airflow pulls Flask, SQLAlchemy, FAB, Pendulum, Alembic, Connexion, ~600 transitive deps) |
| **User DAG `import airflow.providers.*` works?** | YES (default) | NO without an import rewriter — every DAG would have to change `from airflow.providers.postgres.hooks.postgres import PostgresHook` → `from leoflow_runtime.hooks.postgres import PostgresHook` | YES |
| **Maintenance** | 1-2 engineer-days per Airflow minor (CI matrix re-runs upstream provider tests against the shim) | Ongoing per-hook drift with the underlying SDKs (boto3, google-cloud-storage, etc.) | None on Dexaflow's side; full surface drift cost lands on the user |
| **Conflict with ADR 0024** | No — natural extension of the same "shim, don't install" principle | No | YES — pulls the full Airflow control plane into a task pod that has no DB |
| **Cold-start of Lite subprocess executor** | Negligible (shim is ~0.05 s import) | Negligible | **Catastrophic** — Airflow imports Flask + SQLAlchemy + Pendulum = 4-7 s on a laptop |

**Recommendation: Strategy A.** ~1,200 LOC of pure Python unlocks ~80 upstream providers without touching ADR 0024's spirit. The Connection model in Dexaflow already has **100% field-level parity** with Airflow 3.X's, so the gap is only "give user code a `BaseHook.get_connection()`-shaped door into the env vars the agent already injects."

## 1. Architecture in Airflow 3.X

A reminder of the 3.X layout, because it differs from 2.x in ways that
materially simplify embedding.

### 1.1 The canonical import paths moved into a task SDK

In Airflow 3.x the runtime hook surface lives at `airflow.sdk.*`, not at the
2.x paths (`airflow.hooks.base`, `airflow.models.Connection`). Provider code
no longer imports those 2.x paths directly — they import from a stable
**compatibility re-export package**:

```python
# How a 3.x provider opens — this is the import surface to mimic.
from airflow.providers.common.compat.sdk import (
    BaseHook,
    Connection,
    AirflowException,
    AirflowOptionalProviderFeatureException,
    conf,
)
```

That `airflow.providers.common.compat.sdk` is a thin re-export of:

| Re-export | Resolves to | LOC | Notes |
|---|---|---:|---|
| `BaseHook` | `airflow.sdk.definitions.hooks.base.BaseHook` | 107 | The 3.x base hook. |
| `Connection` | `airflow.sdk.definitions.connection.Connection` | 571 | URI parser, `__extra__` round-trip, attrs-backed dataclass. **No Fernet here** — encryption is the metastore's job in 3.x. |
| `AirflowException` | `airflow.sdk.exceptions.AirflowException` | (in a 40-LOC exceptions file) | |
| `conf` | `airflow.configuration.conf` | (large; we stub) | |
| `AirflowOptionalProviderFeatureException` | same exceptions file | | |

The **provider-to-provider contract is the compat layer**, not the SDK. That
means a shim only has to mock `compat.*` precisely; the SDK itself we can
implement with looser fidelity.

### 1.2 `BaseHook` (107 LOC) — what providers actually call

The full public surface a Hook subclass uses from `BaseHook`:

- `__init__(self, logger_name=None)`
- `get_connection(cls, conn_id) -> Connection`  *(the workhorse)*
- `aget_connection(cls, conn_id) -> Connection`  *(async siblings call this)*
- `get_hook(cls, conn_id, hook_params=None)`  *(used by sensors)*
- `get_conn(self)`  *(abstract — each provider implements)*
- `get_connection_form_widgets(cls)` + `get_ui_field_behaviour(cls)`  *(form metadata for the Airflow UI; Dexaflow can return `{}`)*
- `log` property (from `LoggingMixin`)

That's the entire seven-method surface to fake.

### 1.3 `Connection` (571 LOC) — what providers actually use

Despite the file's size, providers use a narrow subset:

- `conn_id`, `conn_type`, `host`, `login`, `password`, `port`, `schema`, `extra`
- `extra_dejson` property (returns `json.loads(extra)` or `{}`)
- `get_uri()` (SQLAlchemy-friendly URI; the inverse of `from_uri`)
- `from_uri(uri)` class method (constructs from `AIRFLOW_CONN_<ID>` value)
- `EXTRA_KEY = "__extra__"` class constant

The other ~450 LOC handle Fernet roundtripping (not relevant — Dexaflow encrypts
elsewhere) and the metastore ORM (not relevant — Dexaflow's metastore is Go).

### 1.4 Connection resolution chain

`_get_connection(conn_id)` in `airflow.sdk.execution_time.context`:

```text
1. SecretCache.get_connection_uri(conn_id) → if hit, build Connection.from_uri(...)
2. For each backend in ensure_secrets_backend_loaded():
     conn = backend.get_connection(conn_id) → if non-None, return
3. raise AirflowNotFoundException(...)
```

The default backend chain in 3.x starts with the
`EnvironmentVariablesBackend`, which reads exactly the `AIRFLOW_CONN_<ID>`
env var Dexaflow's agent already produces (`internal/agent/runner.go:170`).

**Implication:** Dexaflow's shim can skip the entire backend chain and read
the env var directly. Strictly less code, strictly more deterministic.

### 1.5 Provider package layout in 3.x

Each provider is a separate PyPI package (Apache 2.0) at
`providers/<name>/src/airflow/providers/<name>/{hooks,operators,sensors,
transfers,triggers}/`. Distributed independently
(`apache-airflow-providers-<name>` on PyPI).

The runtime requirement of each provider package on `apache-airflow>=3.0` is
the **compat re-export**, not core Airflow itself. Mock `compat.*` and the
hard dep on `apache-airflow` is satisfied (the package metadata cares about
the import surface, not the wheel).

## 2. Provider catalog (top-15, measured against upstream 3.x main)

For each provider: file path of the main hook(s), LOC, what they import from
`airflow.*`, what underlying SDK they wrap, how thick the wrapper is.

| Provider | PyPI package | Hook LOC | `airflow.*` imports | Underlying SDK | Depth |
|---|---|---:|---|---|---|
| **postgres** | `apache-airflow-providers-postgres` | ~900 (`hooks/postgres.py`) | 8 lines: `common.compat.sdk` (BaseHook/Connection/conf), `common.sql.hooks.sql.DbApiHook`, `common.sql.hooks.lineage`, `postgres.dialects`, `openlineage.sqlparser`, cross-deps for IAM | psycopg2, psycopg3, sqlalchemy, pandas, polars, more_itertools | THICK (~30 methods, IAM, dialects, lineage) |
| **mysql** | `apache-airflow-providers-mysql` | 658 | 6 lines, same DbApiHook + amazon cross-dep | mysqlclient, mysql-connector | THICK (13 methods, dual driver, IAM) |
| **sqlite** | `apache-airflow-providers-sqlite` | **51** | 1 line: `DbApiHook` | stdlib `sqlite3` | THIN |
| **mssql** | `apache-airflow-providers-microsoft-mssql` | 175 | 4 lines | pymssql | MEDIUM (12 methods) |
| **snowflake** | `apache-airflow-providers-snowflake` | 1,040 | 7 lines | snowflake-connector-python, snowflake-sqlalchemy, snowpark | THICK (35 methods, OAuth, private key) |
| **http** | `apache-airflow-providers-http` | ~750 | 5 lines (BaseHook, Connection, async helper) | requests, aiohttp, tenacity, pydantic | THICK (sync+async, retry) |
| **redis** | `apache-airflow-providers-redis` | **162** | 1 line: `BaseHook` | redis | THIN (4 methods, pure connection factory) |
| **amazon** (S3 alone) | `apache-airflow-providers-amazon` | **1,543** (`hooks/s3.py`); 52 hook files total | 8 lines | boto3, botocore, aiobotocore | THICK (60+ methods on S3 alone) |
| **google** (GCS alone) | `apache-airflow-providers-google` | **1,850** (`hooks/gcs.py`); 47 hook files total | 7 lines | google-cloud-storage, gcloud-aio-storage | THICK (~40 methods) |
| **microsoft.azure** (WASB) | `apache-airflow-providers-microsoft-azure` | 1,047 (`hooks/wasb.py`) | 5 lines | azure-storage-blob, azure-identity | THICK (sync+async, multi-auth) |
| **slack** | `apache-airflow-providers-slack` | 427 | 4 lines | slack-sdk | THIN-MEDIUM |
| **ftp** | `apache-airflow-providers-ftp` | 358 | 1 line: `BaseHook` | stdlib `ftplib` | MEDIUM |
| **ssh** | `apache-airflow-providers-ssh` | 672 | 6 lines (incl. `airflow.sdk.definitions._internal.types.NOTSET`) | paramiko, asyncssh | THICK (sync+async) |
| **databricks** | `apache-airflow-providers-databricks` | ~1,050 | 3 lines | requests, databricks-sdk | THICK (40 methods, sync+async) |
| **dbt-cloud** | `apache-airflow-providers-dbt-cloud` | ~1,050 | 3 lines (extends `HttpHook`, not `BaseHook` directly) | aiohttp, requests, tenacity, asgiref | THICK |

### 2.1 The hidden lynchpin: `apache-airflow-providers-common-sql`

Every DB provider (postgres, mysql, sqlite, mssql, snowflake, plus oracle,
db2, trino, vertica, exasol, etc.) extends `DbApiHook` defined in
`providers/common/sql/src/airflow/providers/common/sql/hooks/sql.py` —
**~1,250 LOC** that depends on `airflow.exceptions`,
`airflow.providers.common.compat.module_loading`,
`airflow.providers.common.compat.sdk` (`BaseHook` + `conf`), and
`airflow.providers.common.sql.dialects`.

This file is the single biggest lever in the whole study: **vendor it
verbatim** (Apache 2.0 permits with attribution) and ~10 DB providers light
up at zero re-implementation cost.

### 2.2 The other lynchpin: `airflow.providers.common.compat`

`compat.sdk`, `compat.connection`, `compat.lineage` and `compat.module_loading`
are universally imported. They're each ~10-30 LOC of pure re-exports. **Any
embedding strategy must mock this package.**

## 3. Three embedding strategies — concrete cost

### Strategy A — Stub Airflow core minimally, use upstream providers

We extend the existing parser shim (`parser/leoflow_parser/_shim/airflow/`)
into a **runtime shim** that provides:

| Module | LOC estimate | Notes |
|---|---:|---|
| `airflow.sdk.definitions.hooks.base.BaseHook` | ~120 | Mirror the 107-line upstream file; replace `Connection.get` to read `AIRFLOW_CONN_*` directly. |
| `airflow.sdk.definitions.connection.Connection` | ~400 | Re-implement `from_uri` / `get_uri` / `__extra__` handling. Cannot shortcut: every DB hook calls `conn.get_uri()` for SQLAlchemy. |
| `airflow.sdk.definitions.variable.Variable` | ~80 | `get/set` against `AIRFLOW_VAR_*` env. |
| `airflow.sdk.exceptions` | ~40 | `AirflowException`, `AirflowNotFoundException`, `AirflowRuntimeError`, `ErrorType` enum, `AirflowOptionalProviderFeatureException`. |
| `airflow.sdk.log` | ~30 | `mask_secret` (Dexaflow already masks in the UI; can be a no-op for the agent's purposes). |
| `airflow.sdk.execution_time.context` | ~150 | `_get_connection`, `_async_get_connection`, `_get_variable` reading from env + a worker-local cache. |
| `airflow.sdk._shared.module_loading` | ~20 | `import_string` (wraps stdlib `importlib`). |
| `airflow.sdk.definitions._internal.logging_mixin.LoggingMixin` | ~25 | Wraps `logging.getLogger(self.__class__.__name__)`. |
| `airflow.sdk.definitions._internal.types` | ~15 | `NOTSET`, `ArgNotSet`, `is_arg_set` (SSH hook needs these). |
| `airflow.sdk.providers_manager_runtime` | ~50 | Stub `ProvidersManagerTaskRuntime` (`Connection` imports it). |
| `airflow.providers.common.compat.sdk` | ~30 | Re-export aliases — pure forwarding. |
| `airflow.providers.common.compat.connection` | ~30 | `get_async_connection`. |
| `airflow.providers.common.compat.lineage.hook` | ~15 | `get_hook_lineage_collector` returning a no-op. |
| `airflow.providers.common.compat.module_loading` | ~10 | Re-export `import_string`. |
| `airflow.exceptions` | ~25 | `AirflowProviderDeprecationWarning` alias. |
| `airflow.utils.log.logging_mixin` (legacy alias) | ~10 | Some hooks still use this path. |
| `airflow.utils.helpers` | ~30 | `chunks`, `exactly_one` (S3 + Slack need them). |
| `airflow.models.Connection` (legacy alias used by HttpHook) | ~5 | Re-export. |
| `airflow.configuration.conf` | ~40 | Stub returning `None`/defaults; providers call `conf.getint("...")`. |
| `airflow.utils.strings.to_boolean` | ~10 | Snowflake needs it. |
| `airflow.utils.timezone` | ~20 | datetime helpers (Snowflake, GCS). |

**Total shim:** ~1,100-1,300 LOC of pure Python that covers ~95% of the
surface DB + HTTP + S3 + GCS + Redis + Slack hooks actually touch.

**Critical decision: vendor `providers.common.sql.hooks.sql.DbApiHook`
verbatim.** Re-distribute the file under Apache 2.0 attribution. That single
1,250-LOC file lights up postgres / mysql / sqlite / mssql / snowflake / oracle
/ trino / db2 / vertica / exasol at **zero re-implementation cost**. Same
treatment for `providers.common.sql.dialects.dialect` (~200 LOC).

**Image size impact:** ~1,500 LOC of pure Python + the user-installed
provider wheels. **No `apache-airflow` install.** A typical postgres-only
image goes from ~80 MB (Python slim) to ~140 MB (slim + psycopg2-binary +
sqlalchemy + shim). Versus ~600 MB if Strategy C lets apache-airflow get
pulled in.

**Risk surface:** every Airflow 3.x minor release can rename or add an
internal helper. Empirical pattern from 2.x→3.x: ~3-5 breaking internal
moves per minor. **Maintenance: budget 1-2 engineer-days per Airflow minor**
to keep the shim aligned, gated by a CI matrix that pip-installs each
upstream provider against the shim and runs its smoke tests.

**ADR 0024 alignment:** the existing shim is parser-only. Extending it to a
runtime shim is a deliberate, principled scope expansion of the same idea
("we never install Airflow"), not a contradiction. Worth a new ADR
("Runtime hook compatibility shim").

### Strategy B — Re-implement hooks natively in `leoflow_runtime.hooks.*`

Estimated LOC for native re-implementations of the top-15 (Dexaflow-flavored,
no Airflow surface):

| Hook | Native LOC estimate | Saved vs. Airflow |
|---|---:|---|
| Postgres | 200 | 4.5× shrinkage (drop IAM, dialects, lineage, pandas/polars helpers) |
| MySQL | 180 | 3.6× |
| SQLite | 40 | parity |
| MSSQL | 120 | similar |
| Snowflake | 350 | 3× (still need OAuth + key auth) |
| HTTP | 200 | 3.7× (drop sync+async dichotomy) |
| Redis | 80 | parity |
| S3 | 400 | 4× (cover the 15 ops people actually use) |
| GCS | 400 | 4.5× |
| WASB | 350 | 3× |
| Slack | 100 | 4× |
| FTP | 200 | 1.8× |
| SSH | 350 | 2× |
| Databricks | 400 | 2.6× |
| dbt Cloud | 350 | 3× |
| **Total** | **~3,720 LOC** | |

Plus a shared `leoflow_runtime.connections.Connection` (~150 LOC) and
`BaseHook` (~80 LOC): **~4,000 LOC total**.

**Pros:** owns the surface; ADR-clean; no Airflow version drift; full
control over error messages, logging, and observability.

**Cons:** the upgrade story is brutal. Every Airflow DAG that does
`from airflow.providers.postgres.hooks.postgres import PostgresHook`
breaks. We'd need an import-rewriter
(`airflow.providers.postgres.hooks.postgres` →
`leoflow_runtime.hooks.postgres`) and the public surface would need to
match Airflow's method signatures *anyway* — most of the LOC savings vanish.

### Strategy C — Allow user-installed `apache-airflow-providers-*`

User declares `apache-airflow-providers-postgres` in `dexaflow.yaml.deps`;
runtime pip-installs at venv build / image build time. The providers import
`from airflow.providers.common.compat.sdk import BaseHook, Connection` which
forces `apache-airflow>=3.0` as a runtime dep.

- **Image size impact:** apache-airflow 3.2.x is **~150-180 MB installed**
  (Flask, SQLAlchemy, Alembic, Pendulum, FAB, Connexion, …). On top of slim
  Python: image grows from ~80 MB → **~280-350 MB** before the provider's
  own deps. With postgres + psycopg2-binary + sqlalchemy, expect ~450 MB. A
  real-world DAG image with 3 providers: **600-800 MB**.
- **Conflict with ADR 0024:** the ADR forbids importing real Airflow during
  *parsing*. It does not formally forbid importing it at *runtime*.
  However: (a) it makes the Lite edition's `subprocess` executor terrible
  (cold start dominated by Airflow's import of Flask + SQLAlchemy +
  Pendulum, easily 4-7 s on a laptop); (b) it pulls in CVEs we can't audit;
  (c) it imports a database stack into a *task pod* that has no DB.
- **Conflict with "Python minimal, Go max" principle:** very large negative
  impact. apache-airflow brings ~600 transitive Python deps.

**Verdict:** acceptable only as an escape hatch for connectors we don't
support; never the default path.

## 4. Connection model gap (field-by-field)

Already 100% parity. The Dexaflow Connection model
(`internal/domain/connection.go`) and the AIRFLOW_CONN URI renderer
(`internal/storage/conn_uri.go:40`) cover every field a 3.x `Connection`
exposes:

| Field | Airflow 3.X `Connection` | Dexaflow `domain.Connection` | Status |
|---|---|---|---|
| `conn_id` | `str` | `ConnID string` | PARITY |
| `conn_type` | `str \| None` | `ConnType string` | PARITY |
| `description` | `str \| None` | `Description string` | PARITY |
| `host` | `str \| None` | `Host string` | PARITY |
| `schema` | `str \| None` | `Schema string` | PARITY |
| `login` | `str \| None` | `Login string` | PARITY |
| `password` | `str \| None` | `Password string` (AES-256-GCM at rest, ADR 0019) | PARITY + better — Airflow 3.x SDK doesn't encrypt; the metastore does. |
| `port` | `int \| None` | `Port *int` | PARITY |
| `extra` | `str \| None` (JSON-as-string) | `Extra string` (AES-256-GCM at rest) | PARITY |
| `EXTRA_KEY = "__extra__"` | URI carries extra in `?__extra__=` | `internal/storage/conn_uri.go:40` emits exactly this | PARITY |

**URI form** `<conn_type>://<login>:<password>@<host>:<port>/<schema>?__extra__=<json>`
— Dexaflow already renders this byte-for-byte. The only edge case handled:
`sqlite:///` triple-slash idempotency (`conn_uri.go:31-35`).

**Gap on the model side: zero.** The Connection is feature-complete for
Airflow 3.X parity. The gap is entirely in how that connection is
*consumed*: today user code reads the env var and parses the URI manually.
Once a `BaseHook.get_connection()` exists, every Airflow-style hook works.

## 5. Recommendation

### Pick Strategy A as the default. Use Strategy B as a targeted overlay for the 5 hooks where Dexaflow-native ergonomics matter.

**Why:**

1. The shim already exists in spirit (the parser shim at
   `parser/leoflow_parser/_shim/airflow/`) and ADR 0024 already documented
   the "shim, don't install" principle. This is an extension, not a
   reversal.
2. Field-level parity on `Connection` is already 100%. The only missing
   piece is a `BaseHook` class wired to the existing `AIRFLOW_CONN_*` env-var
   delivery — which Dexaflow's agent (`internal/agent/runner.go:170`) already
   produces.
3. ~1,200-1,500 LOC of Python shim unlocks the long tail (~80 providers).
   Re-implementing all 80 natively would be 15k+ LOC and nobody on
   Dexaflow's roadmap wants to maintain that.
4. The 3.x design — secrets backends, no Fernet in the SDK, attrs-based
   dataclasses — is dramatically more shimmable than 2.x's `airflow.models`
   jungle. **The window to do this cheaply is now**; only 3.x as the cut
   matters.

### Phasing

**Phase A.0 — Foundations (~1 sprint, ~1-2 weeks of focused work).**

- Write a failing integration test:
  ```python
  from airflow.providers.postgres.hooks.postgres import PostgresHook
  rows = PostgresHook(postgres_conn_id="my_db").get_records("SELECT 1")
  ```
  inside a real Lite-executed task. This is the regression contract.
- Move the parser shim into a shared package:
  `leoflow_python_compat/airflow/...` with two entry points (parser-mode =
  lazy stubs; runtime-mode = real shim). Runtime install gated by
  `dexaflow.yaml.airflow_compat: true`.
- Implement `BaseHook`, `Connection`, `Variable`, `exceptions`,
  `log.mask_secret`, `execution_time.context._get_connection` (reads
  `AIRFLOW_CONN_*` env directly — skip the SecretsBackend chain entirely;
  it's overkill for our model).
- Vendor `providers.common.sql.hooks.sql.DbApiHook` +
  `providers.common.sql.dialects.dialect` verbatim (Apache 2.0
  attribution).
- CI matrix: pip-install upstream
  `apache-airflow-providers-{postgres,sqlite,redis,http}` against the shim
  and run their unit tests. **This is the regression gate.**

**Phase A.1 — The 80/20 cut.** Ship official Dexaflow support
(CI matrix + cookbook page + connection-test DAG, matching the precedent set
by the prior connector-rigor work) for these **five hooks**:

1. **postgres** — Lite's own datastore tier; the most-used connector
   full-stop.
2. **http** — REST APIs, webhooks; the lingua franca of integration.
3. **sqlite** — 51 LOC upstream, almost free; great for tutorials.
4. **redis** — 162 LOC, ~zero risk; already a Dexaflow infra primitive.
5. **mysql** — Postgres' counterpart; very common.

These five cover the majority of "I want to migrate my Airflow DAG" cases
per Airflow's own provider download stats (postgres + http + mysql
consistently top-3 downloads).

**Phase A.2 — Cloud expansion.** Add S3, GCS, WASB. These three account for
almost all object-storage traffic. Each is heavy upstream (1k+ LOC) but our
cost is *zero* if Strategy A holds — we just need the shim to satisfy
their imports.

**Phase A.3 — Long tail.** Slack, FTP, SSH, Snowflake, Databricks, dbt:
documented as "should work via the shim, not formally tested." Users opt-in
via `airflow_compat: true`.

**Phase B (later, optional overlay).** For the 5 hooks in A.1 *only*, ship
a native `leoflow_runtime.hooks.postgres.PostgresHook` that subclasses the
upstream PostgresHook by composition. This gives Dexaflow-native ergonomics
(better error messages, structured logging into our lifecycle stream)
while still satisfying `isinstance(h, PostgresHook)` for DAG code.
**Total new LOC for B: ~500.** Skip this until A is in production for 2
releases.

### Risks

1. **Airflow provider drift.** A minor release renames
   `providers.common.compat.sdk`. *Mitigation:* CI matrix on every Airflow
   minor; pin the supported Airflow line per Dexaflow release (e.g. Dexaflow
   0.2 supports Airflow providers compatible with `apache-airflow~=3.2`,
   0.3 bumps to 3.3). Document the support matrix.
2. **License.** All providers are Apache 2.0 — re-distributing `DbApiHook`
   is explicitly permitted with attribution. No GPL anywhere in the
   top-15.
3. **Provider-to-provider deps.** PostgresHook imports `AwsBaseHook` and
   `AzureBaseHook` for IAM. *The cleanest workaround:* monkey-patch those
   imports to `None`-yielding stubs and lazily fail only when the user
   actually invokes IAM auth. **Cost: ~30 LOC of import hooks.** Same
   trick handles OpenLineage cross-imports.
4. **Async surface.** Async hooks (`aget_connection`, `HttpAsyncHook`) use
   `sync_to_async` from `asgiref` and `asyncio`. The shim must stub
   `_async_get_connection`. Trivial — already shown in 3.x source.
5. **Connection encryption mismatch.** Dexaflow encrypts `password` at rest;
   Airflow 3.x SDK does not. **Zero conflict** — encryption is metastore-
   side. We decrypt before stamping `AIRFLOW_CONN_*` (already the case at
   `internal/agent/runner.go:170`).
6. **Variable scope.** Dexaflow Variables are plaintext today. The shim's
   `Variable.get()` will read `AIRFLOW_VAR_<KEY>` which the agent already
   emits (`internal/agent/runner.go:163`) — no behavior change required.

### Bottom line

Build a ~1,200 LOC Python "runtime compatibility shim" that fakes
`airflow.sdk.definitions.{hooks.base, connection, variable}`,
`airflow.sdk.exceptions/log/execution_time.context`, the
`airflow.providers.common.compat.*` re-export layer, and vendors the
1,250-line `DbApiHook` verbatim. Gate it behind an opt-in
`airflow_compat: true` in `dexaflow.yaml` so the default Lite footprint stays
slim. Officially support 5 hooks via CI (postgres / http / sqlite / redis /
mysql), document the rest as best-effort. The Connection model has zero
gap. Avoid Strategy C (pulling apache-airflow into the image) at all costs
— it adds 200+ MB and 600 transitive Python deps for no architectural gain.
Strategy B (native re-implementation) is a tempting overlay later but the
wrong primary path: it would force re-litigating every Airflow method
signature for ~4,000 LOC of ongoing maintenance, with no compatibility
upside the shim doesn't already provide.

## 6. Open questions for the ADR

These are the decisions that ADR 0036 would need to lock before code lands.
Not blockers for this study, but listed so they're explicit:

1. **Module namespace.** Do we expose the shim at `airflow.*` (so user
   imports work unchanged) or at `leoflow_compat.airflow.*` (so it's
   explicit)? Strong lean: `airflow.*` for compatibility, but only on
   `sys.path` when `airflow_compat: true`. This avoids polluting non-
   compat builds.
2. **Versioned support matrix.** Dexaflow 0.2 supports providers from
   Airflow 3.2; 0.3 from 3.3. Or do we pick a single "supported Airflow
   range" per Dexaflow release and document it?
3. **Test isolation.** Should the CI matrix run *upstream* provider test
   suites (slow, large, real network) or only a curated subset of "smoke"
   tests we write ourselves?
4. **Error surface.** When an unsupported provider's hook raises an
   internal-looking error (e.g. `AirflowOptionalProviderFeatureException`),
   do we re-raise as a Dexaflow-branded error or let it bubble?

## 7. Files this study referenced (Dexaflow side)

- Parser shim: `parser/leoflow_parser/_shim/airflow/_core.py`,
  `parser/leoflow_parser/_shim/airflow/sdk/__init__.py`
- Runtime: `runtime/python/leoflow_runtime/runner.py`,
  `runtime/python/leoflow_runtime/xcom.py`
- Connection model: `internal/domain/connection.go`
- AIRFLOW_CONN URI rendering: `internal/storage/conn_uri.go`
- Agent env injection: `internal/agent/runner.go` (lines 153-170)
- Encryption ADR: `docs/adr/0019-secret-encryption-at-rest.md`
- Parser shim ADR: `docs/adr/0024-dag-parsing-structural-shim.md`
