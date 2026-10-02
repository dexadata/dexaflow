---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /dbt.html
# --- end AUTO redirect aliases ---
title: dbt in your DAGs
linkTitle: dbt
weight: 30
description: Drop dbt_group() into a dag.py and your dbt project's models become tasks in the same graph.
---

A Leoflow DAG is a `dag.py` plus a `dexaflow.yaml`. **`dbt_group("name")` puts a dbt
project inside one**: Leoflow reads dbt's own `manifest.json` at compile time and
turns each node (seed, model, snapshot, test) into a task in the same graph as your
Python and Bash tasks, executed **pod-per-task** against your warehouse — no Apache
Airflow in the control plane, and no [Cosmos](https://astronomer.github.io/astronomer-cosmos/)
at runtime.

If your DAG is *only* models, there is a shortcut that skips the `dag.py`
entirely — see [If your DAG is only models](#if-your-dag-is-only-models). Start
here either way: the shortcut's exit cost is real, and this section is what you
will need the day you add one Python task.

{{% pageinfo %}}
**Your `ref()` graph *is* the DAG.** Leoflow reads dbt's manifest at compile time
and emits one task per node — you never write task dependencies, and there is no
library to import and no profile-mapping boilerplate.
{{% /pageinfo %}}

---

## 1. Put a dbt project in your DAG

Author a `dag.py` and call `dbt_group("<name>")` where the models belong. The
group is a task like any other: operators before it, operators after it, one
graph. Configure the project under `dbt_groups:` in `dexaflow.yaml`.

{{% alert title="Packing models into fewer pods" color="info" %}}
By default (`granularity: node`) each model is its own pod — like Cosmos. Set
`granularity: level` or `folder` (as below) to pack models into **grouped tasks**:
each group compiles to a single `dbt build --select …` task — one pod that dbt runs
internally — so a project of *N* models needn't become *N* pods. This is Leoflow's
answer to pod sprawl for dbt; see
[Core concepts → When you pay for a pod](/concepts/core-concepts/#when-you-pay-for-a-pod-and-when-you-dont).
{{% /alert %}}

Embed the dbt project between your operators with `dbt_group("<name>")`:

```python
# sales/dag.py
from leoflow import dbt_group
from airflow.providers.standard.operators.python import PythonOperator
from airflow.sdk import DAG

def extract(): ...
def notify(): ...

with DAG("sales", schedule="@daily"):
    pull   = PythonOperator(task_id="extract", python_callable=extract)
    models = dbt_group("transform")          # the dbt project, expanded
    ping   = PythonOperator(task_id="notify", python_callable=notify)

    pull >> models >> ping
```

```yaml
# sales/dexaflow.yaml
# The schedule lives in dag.py's DAG(schedule=…) — there is no top-level
# schedule: key, and dexaflow.yaml rejects one.
dag_id: sales
dependencies:
  - dbt-postgres==1.9.*       # the adapter; the base image ships no dbt
dbt_groups:
  transform:                  # the name passed to dbt_group()
    project: ./transform
    granularity: level
    connection: warehouse_pg  # managed connection (see §3)
```

At compile, the operators and the dbt project become **one** `dag.json`. The dbt
tasks are namespaced under the group (`transform__stg`, `transform__orders`, …),
the group's **roots** depend on `extract`, and `notify` depends on the group's
**leaves**:

```
extract → transform__level_0 → transform__level_1 → transform__level_2 → notify
```

---

## 2. Granularity — split vs fused

`granularity` controls how dbt nodes are packed into pods. It is a knob, not a
fixed "one model = one pod".

| `granularity` | what it does | pods |
|---|---|---|
| **`node`** *(default)* | one task per dbt node — `dbt run/seed/test --select <node>` | many — full per-model isolation, retry, and grid granularity |
| **`level`** | one task per topological wave; safe by construction | few |
| **`folder`** | one task per **first** folder segment (`staging`, `marts`, …) | few |

The `folder` key is the first segment, not the containing folder: `marts/finance`
and `marts/sales` both become one `marts` task. A model with no folder at all
falls back to its resource-type plural (`models`, `seeds`), so a project with a
folder *named* `models` **and** a model at the root of `models/` gets both in a
single task — the compile warns on stderr when that happens, naming the members.
Use `granularity: node` if you need them apart.

`node` is **split** (Leoflow's scheduler parallelizes across pods, one model per
pod). `level`/`folder` are **fused** — a group runs as a single
`dbt build --select <members>` invocation.

### Fused is parallel, not sequential

A fused group runs in **one pod**, but dbt's own engine parallelizes the group's
independent models up to its `threads` setting, respecting the internal DAG.
Measured on three independent models (3s each):

| | time |
|---|---|
| `dbt build --threads 1` | ~14s (sequential) |
| `dbt build --threads 4` | ~8s (the three run concurrently) |

So `fused` trades **per-model isolation and grid granularity** for **far fewer
pod startups**, while keeping in-pod parallelism. Rule of thumb:

- **Lite (dev loop):** `node` — cheap local subprocess, per-model visibility.
- **Pro (production):** `level`/`folder` — fewer pods at scale; `node` when you
  want strict per-model isolation and can afford the pods.

> The fused trade-off is the same one Cosmos faces; the difference is Leoflow
> exposes it as a single declarative knob.

---

## 3. The warehouse connection

dbt needs a `profiles.yml`. Leoflow resolves it for you — pick the one that fits:

### Zero-config local (Lite → duckdb)

On **Lite**, a dbt project with **no `connection:` and no `profiles.yml` of its own**
just runs — against an embedded **duckdb** file (`leoflow_local.duckdb`, in the project)
with no setup at all:

```console
$ leoflow lite            # write models, hit Trigger — that's it
```

Leoflow generates the duckdb profile transparently at both compile (`dbt parse`) and run
time, in the task's working dir — **never touching your global `~/.dbt`**. It's the
ideal way to develop and test transformations before wiring a real warehouse. Add a
`connection:` (below) or a project `profiles.yml` at any time and that wins instead — the
default only kicks in when there's nothing configured.

### Managed connection (recommended for Pro)

{{% alert title="Top-level `connections:` and `variables:` reach dbt tasks too" color="info" %}}
`dbt.connection` is the one the runtime turns into `profiles.yml`. Anything else
your models need — a connection a pre-hook calls, a variable a macro reads — is
declared at the top level of `dexaflow.yaml`, exactly as for a `dag.py` project:

```yaml
connections: [warehouse_pg, reporting]
variables: [env]
```

Those declarations are the scope a dbt task is granted, and they are merged with
the `dbt.connection` above rather than replaced by it — every dbt task is granted
the managed connection **and** everything the DAG declares. (Under the default
`permissive` scoping the pod still receives the whole tenant vault; the declared
set is what `enforce` restricts it to, and what an external backend is queried
for.)

Previously a **dbt-only** project dropped both on the way to `dag.json`. Under
`auth.secret_scoping: enforce` that meant the pod got none of them and the DAG
failed inside the task rather than at compile. Under the default `permissive` the
pod still received the whole tenant vault, so the declarations were simply not
honored — except for a secret that lives only in an
[external backend](/operate/external-secrets/), which is fetched by declared name
and so was never requested at all.
{{% /alert %}}

Set `connection:` to a Leoflow connection id. Leoflow delivers the connection to
the pod (encrypted at rest, decrypted in-pod) and the runtime **generates
`profiles.yml`** before dbt runs — **no credential is ever baked into the image**.

```yaml
dbt:
  project: .
  connection: warehouse_pg
```

```console
# create the connection once (UI, or the API)
$ curl -X POST .../api/v2/connections -d '{
    "connection_id":"warehouse_pg","conn_type":"postgres",
    "host":"db.internal","port":5432,"login":"etl","password":"…","schema":"transform"}'
```

{{% alert title="Create the connection before you deploy" color="info" %}}
The compiler **declares** the managed connection on the dbt tasks, so Leoflow
validates it at registration: `leoflow push`/`deploy` is rejected if
`connection:` names a connection that neither exists in the vault nor is covered
by an [external secrets backend](/operate/external-secrets/). Create the
connection first (or configure the backend), then deploy — the same
create-then-deploy order any declared connection follows. This also means the
connection is delivered under every [secret-scoping](/operate/agent-credential-transport/)
mode (enforce included) and resolvable from an external backend, not only under
permissive scoping.
{{% /alert %}}

The compiled command becomes:

```
python -m leoflow_runtime --dbt-profile warehouse_pg <profile> && dbt run --select …
```

{{% alert title="Serverless warehouse cold-start" color="info" %}}
A serverless SQL warehouse that has auto-stopped is woken transparently by the
adapter, but the first task of a run pays the wake delay (~10s on Databricks
serverless) before its query starts. Warehouse behavior, not Leoflow's; pre-warming
is a warehouse-side mitigation if it matters for your SLA.
{{% /alert %}}

### Bring your own `profiles.yml`

Omit `connection:` and ship a `profiles.yml` in the project (it is baked into the
image). Simple for Lite; you own the credential delivery.

> Use **one or the other** — a `connection:` makes Leoflow generate the profile;
> without it, your baked `profiles.yml` is used.

**Adapters:** Postgres, Snowflake, BigQuery, Databricks (the official
`dbt-databricks` adapter, not the community one), and **duckdb** (embedded, for
zero-server local dev) are supported — Leoflow maps the managed connection to each
adapter's profile. Declare the adapter package
(`dbt-snowflake`, `dbt-bigquery`, `dbt-databricks`, …) as a dependency so it lands
in the image.

Each cloud adapter supports modern, service-account auth — Leoflow's recommended
mode for automation — alongside the legacy password/key-file mode. Everything is
driven by the connection's `extra`, so nothing secret is baked into the image, and
the connection form surfaces these fields with inline help:

| Warehouse | Recommended auth | Set in the connection | Legacy fallback |
|---|---|---|---|
| **Snowflake** | key-pair | `private_key_content` (inline PEM) or `private_key_file` (path), optional `private_key_passphrase` | `password` |
| **BigQuery** | keyless (Workload Identity / ADC) | `method: oauth` | `keyfile_dict` |
| **Databricks** | OAuth M2M (service principal) | `client_id` + `client_secret` (or `auth_type: oauth`) | access token (PAT) |

The recommended mode wins when its fields are present; otherwise the legacy mode
is used. Per-warehouse setup — required fields (`account`/`warehouse`, `http_path`,
…), example payloads, and precedence — lives in the connection reference:
[Snowflake](/connections/snowflake/), [BigQuery](/connections/gcpbigquery/),
[Databricks](/connections/databricks/).

---

## 4. Failure isolation & the build parse-gate

**A syntax error in one model does not blow up production.** dbt parses the whole
project on every invocation, so a compilation error in *any* model would, in
naive setups, break *every* task. Leoflow stops that at the **build parse-gate**:

`leoflow compile` runs `dbt parse` on your machine — **both editions**, before it
writes anything. A broken project never produces a `dag.json` and, on Pro, never
produces an image: nothing deploys. So a syntax error fails **loudly and early**,
never at 5am.

### The baked manifest

Leoflow compiles from `dbt parse`'s `target/manifest.json`. **`leoflow compile`
runs `dbt parse` on your machine** — both editions, not inside the image build —
and the resulting manifest is copied into the DAG image alongside the project.
Pin a pre-built one with `dbt.manifest` to skip the parse (see
[No local dbt](#no-local-dbt)).

**Which `dbt` runs the parse is not the same in both editions**, and the
difference decides whether you need dbt installed on your host at all:

- Under **`leoflow dev`** the per-DAG venv is provisioned from `dependencies:`
  *before* the project is compiled, and the parse uses **that venv's `dbt`**. So
  a Lite project that declares `dbt-core` and its adapter in `dependencies:`
  compiles with no dbt on your `PATH` — the version that parses your models is
  the same one that will run them.
- Running **`leoflow compile` by hand** provisions nothing. It falls back to the
  `dbt` on your `PATH`, and fails with a message naming both ways out if there
  is none: install dbt-core plus your adapter, or pin `dbt.manifest`.

The practical consequence is the reverse of what people usually assume: you do
not need to `pip install dbt-core` to develop a dbt DAG in Lite. Declaring it in
`dependencies:` is enough, and is better, because the host and the task then
cannot disagree about the dbt version.

dbt's **Slim CI** (`--select state:modified+ --defer --state`) is not offered as a
turnkey recipe: there is no supported way to export the deployed manifest to diff
against, and the compiler emits node/level/folder selectors only. If you drive dbt
yourself in CI you can supply your own prior `manifest.json` as `--state` today.

**Run-time errors** (a model that compiles but fails against the warehouse) are
isolated by granularity:

- **`node`:** the failing model fails its **own pod**; independent models succeed;
  only its downstream subtree is blocked.
- **fused:** dbt still materializes the group's good models, but the group task
  fails as a unit (coarser blast radius).

### Retrying a fused group re-runs the whole group

A fused group is one `dbt build --select <members>` task. If it fails mid-way, dbt
keeps the models it already built — but **retrying the task re-runs the entire
group from scratch**, including the models that already succeeded. dbt is not
resumed from its failure point here (that would need `dbt retry`, which reads the
previous run's `target/run_results.json` — an artifact Leoflow does not yet persist
across pod attempts). On warehouses billed per compute-second, retrying a
mostly-green group re-bills the green models.

This is the flip side of the fused trade-off. If retry efficiency matters more than
pod count for a given DAG, use **`granularity: node`**: each model is its own task,
so a retry re-runs only the failed model (and its blocked downstream), exactly like
Airflow's per-task retry. Choose per DAG:

- **Expensive warehouse + flaky sources → `node`** — pay in pods, save on recompute.
- **Cheap/idempotent models at scale → `level`/`folder`** — pay a little recompute
  on the rare retry, save on pod startups.

**Planned:** resumable fused retries — persisting `run_results.json` so `dbt retry`
skips the already-built models ([#569](https://github.com/dexadata/leoflow/issues/569)).

---

## 5. Where config lives (two YAML worlds)

| file | owner | describes |
|---|---|---|
| `dbt_project.yml`, `profiles.yml`, `models/**/*.yml` | **dbt** | the transformation (models, materializations, tests, connection) |
| `dexaflow.yaml` | **Leoflow** | the DAG (id, schedule, granularity, packing, managed connection) |

They never overlap: `dbt_project.yml` never mentions schedules/pods; `dexaflow.yaml`
never mentions SQL. Author your **models** in your dbt tooling (VS Code + dbt
Power User, dbt Cloud IDE); Leoflow only adds orchestration and packing.

---

## 6. Adapters and auth

Leoflow generates each warehouse's `profiles.yml` from your managed
`connection:`, so the credential never enters the image or the repository.

| Adapter | Auth modes |
|---|---|
| **postgres** | user/password |
| **duckdb** | local file — the zero-config default on Lite |
| **snowflake** | user/password, key-pair |
| **bigquery** | service-account JSON, ADC / Workload Identity |
| **databricks** | personal access token, service-principal OAuth M2M |

Each emitted profile is checked in CI against the real adapter's own credential
parser — field names, alias resolution, required fields, and every auth mode
above — so a profile Leoflow generates is one the adapter accepts.

That is not the same as a query succeeding against your account. **Postgres and
duckdb are the only adapters exercised against a live warehouse in CI**;
Snowflake, BigQuery and Databricks are contract-tested and hand-verified, because
live-query coverage needs real accounts and CI secrets.

---

## If your DAG is only models

A DAG whose tasks are *only* dbt models has no `dag.py` — and **must not carry
one**, even an empty placeholder: `compile` refuses the pair. Declare the project
under a top-level `dbt:` block and the shape comes from dbt's `ref()`/`source()`
graph. It is one file fewer to start with.

{{% alert title="Know the exit cost before you start here" color="warning" %}}
This is a shortcut, not a smaller version of §1 — it has a lower ceiling, and
leaving it is a migration rather than an edit.

**It cannot express a task that is not a dbt model.** No sensors, no provider
operators, no Python or Bash. And because those live on the DAG object a
`dag.py` builds, it also has nowhere to declare `start_date`, `catchup`,
`max_active_runs` or DAG-level `params`. (`end_date` and `max_active_tasks` are
not author-settable on *either* path yet — [#797](https://github.com/dexadata/leoflow/issues/797).)
A top-level `connections:`/`variables:` is worse than rejected — the schema accepts
it and the compiled DAG silently drops it ([#997](https://github.com/dexadata/leoflow/issues/997)).
`retries` and `resources` can be scoped per task; `alerts` and `staging` are
DAG-wide — all four come from `dexaflow.yaml` and apply to both shapes.

**Adding one Python task means rewriting the DAG.** Delete `dbt:`, add
`dbt_groups:`, write a `dag.py`, and move the schedule from `dbt.schedule` to
`DAG(schedule=…)`. **Delete `dbt:` in the same change.** A project carrying both
a top-level `dbt:` block and a `dag.py` is refused by `compile` and `validate`,
naming which block to remove — the two describe different DAGs and there is no
reading of both at once
([#1001](https://github.com/dexadata/leoflow/issues/1001)).

**Every `task_id` changes**: the shortcut emits bare node ids
(`stg`), a group namespaces them (`transform__stg`). That breaks run-history
continuity and any per-task override in `tasks:` bound by id.
{{% /alert %}}

You write dbt the way you always do, and add one `dexaflow.yaml`:

```
sales/                         # the DAG = a dbt project + dexaflow.yaml
├── dexaflow.yaml               # the only Leoflow file
├── dbt_project.yml            # dbt
├── profiles.yml               # dbt (or use a managed connection — see below)
├── seeds/raw_orders.csv
└── models/
    ├── staging/stg_orders.sql #  select … from {{ ref('raw_orders') }}
    └── marts/orders.sql       #  select … from {{ ref('stg_orders') }}
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: sales
owner: data-team
dependencies:
  - dbt-postgres==1.9.*        # the adapter; the base image ships no dbt
dbt:
  project: .                   # dir containing dbt_project.yml
  granularity: node            # node | level | folder  (see §2)
  schedule: "@daily"           # optional; empty = on-demand (Lite dev loop)
```

Compile it like any DAG:

```console
$ leoflow compile ./sales --image registry.example.com/sales:v1
Compiled sales -> sales/dag.json (image registry.example.com/sales:v1, version 9f3a2c1)
```

`leoflow compile` reads the dbt manifest and emits one task per node:

| task_id | command |
|---|---|
| `raw_orders` | `dbt seed --select raw_orders` |
| `stg_orders` | `dbt run --select stg_orders` (after `raw_orders`) |
| `orders` | `dbt run --select orders` (after `stg_orders`) |
| `unique_orders_id` | `dbt test --select unique_orders_id` (after `orders`) |

You never write task dependencies — `{{ ref('stg_orders') }}` **is** the edge.

> The manifest comes from `dbt parse`, which `leoflow compile` runs on your
> machine. Set `dbt.manifest: target/manifest.json` to point at a pre-built one
> instead.


---

## No local dbt

No dbt on this machine, or no wheel of your adapter for your Python? Pre-build
the manifest in a container and pin it with `dbt.manifest`.

`leoflow compile`/`--build` shells out to `dbt parse` on your machine (the
runtime never needs dbt — only compile-time manifest generation does). If your
host has no dbt installed, or your adapter has no prebuilt wheel for your
Python (a common one: `dbt-databricks` publishes wheels for 3.10–3.12, not the
Python 3.9 that ships as the default `python3` on some LTS distros/older
macOS), `dbt parse` fails or the adapter refuses to install — before Leoflow
ever gets involved.

**Escape hatch: generate the manifest in a throwaway container with a Python
`dbt-databricks` actually supports, then point `dbt.manifest:` at the result.**

`dbt parse` does not open a warehouse connection, so a **`profiles.yml` with
placeholder values** (matching the profile name in `dbt_project.yml`) is
enough — no real credentials need to enter the container:

```console
$ docker run --rm -v "$PWD":/proj -w /proj python:3.11-slim bash -c "
    pip install --no-cache-dir dbt-databricks &&
    dbt parse --profiles-dir . "
$ ls target/manifest.json     # now sitting in your project dir
```

```yaml
# dexaflow.yaml
dbt:
  project: .
  manifest: target/manifest.json   # pinned — leoflow compile skips `dbt parse` entirely
  connection: warehouse_databricks
```

A pinned manifest is used **as-is** (see `loadDbtManifest` — no local dbt
required at all from that point on); regenerate it in the container whenever
you change models. This is the same trick CI runners use when their base image
doesn't carry a compatible Python for every adapter — see [Python on the
runner](/operate/cicd-deploy/#python-on-the-runner) for the CI-side version of
this problem.

---

## Reference

`dexaflow.yaml` `dbt:` (whole-DAG) and each `dbt_groups:` entry (embedded) accept:

| field | meaning |
|---|---|
| `project` | directory containing `dbt_project.yml` |
| `granularity` | `node` \| `level` \| `folder` (default `node`) |
| `manifest` | optional pre-built `manifest.json` path (project-relative); empty runs `dbt parse` |
| `connection` | managed Leoflow connection id; empty = bring-your-own `profiles.yml` |
| `schema` | overrides the dbt target schema in the generated profile |
| `schedule` | *(whole-DAG `dbt:` only)* cron/preset; empty = on-demand. Declared **under `dbt:`** — a `dag.py` DAG takes its schedule from `DAG(schedule=…)` instead. There is no top-level `schedule:` key, and `dexaflow.yaml` rejects one. |

## Cosmos at a glance

| | Cosmos | Leoflow |
|---|---|---|
| Where the translation runs | Python lib at DAG-parse time | Go at compile time |
| Manifest | re-parsed per `DbtDag` init | parsed once at compile time |
| Config | in the `dag.py` (4 config objects) | in `dexaflow.yaml` (declarative) |
| Connection → profile | per-warehouse `profile_mapping` class | one `connection:` line, generated in-pod |
| Pod packing | execution mode + per-model | `granularity` knob (split/fused) |
| Mixing with operators | `DbtTaskGroup` in a DAG | `dbt_group()` in a `dag.py` |
