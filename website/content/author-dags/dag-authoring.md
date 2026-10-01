---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /dag-authoring.html
# --- end AUTO redirect aliases ---
title: DAG authoring
weight: 10
description: "Author a DAG: leoflow.yaml plus dag.py compiled to one immutable artifact."
---

A Leoflow DAG is two files in a project directory, compiled into an **immutable
artifact** (`dag.json` + a container image, versioned together — ADR 0003).

```
dags/my_pipeline/
  dag.py         # real Apache Airflow SDK 3.2.x code
  leoflow.yaml   # Leoflow deploy config (not an Airflow file)
```

Scaffold one with `leoflow init dags/my_pipeline`.

## Workspace layout (multi-DAG)

> **Lite-specific.** The hot-reload watcher exists only in `leoflow lite` — the
> developer-mode loop. At **runtime** Pro has no "workspace": every DAG ships as
> its own image-and-`dag.json` pair, built by CI and registered via
> `leoflow push dag.json`, and the control plane never sees a directory of
> siblings. See [The development → deploy lifecycle](#the-development--deploy-lifecycle).
>
> The same *discovery* does run at **build** time in Pro, in one place:
> [`leoflow build [workspace]`](/operate/cicd-deploy/) walks a directory of
> projects and builds each with its own `registry:`. That is a convenience for
> the person at the keyboard — it produces the same one-image-per-DAG artifacts
> and changes nothing about how Pro runs them.

`leoflow lite` watches a **workspace** that can hold many DAGs as sibling
subdirectories. The default workspace is `~/leoflow/` (set by `leoflow setup`).

```
~/leoflow/                       # workspace root
  recurring_print/               # one DAG project per subdir
    leoflow.yaml
    dag.py
  recurring_parallel/
    leoflow.yaml
    dag.py
  ml/
    train/                       # nested subdirs are scanned too
      leoflow.yaml
      dag.py
```

**Recommended layout (best practice, not enforced)**: name the subdirectory the
same as the `dag_id`. The binding is by `dag_id` (yaml field, or the subdir
basename when no yaml is present — see below), but matching names make the
workspace navigable by humans and grep-friendly. `~/leoflow/sales_etl/` for a
DAG named `sales_etl`.

### Discovery rules

`leoflow lite` — and `leoflow build`, which shares the same discovery — walks
the workspace and treats every subdirectory containing a
`dag.py` (with or without a `leoflow.yaml`) as a project. The scan:

- Goes **at most 5 levels deep** from the workspace root. A DAG at
  `<ws>/a/b/c/d/dag.py` is the deepest valid case. Deeper paths are skipped.
- Skips `.git`, `__pycache__`, `.venv`, `venv`, and any hidden directory
  (`.*`). That list *mirrors* the `exclude_paths` default so the scan and an
  image build agree on what counts as noise — but discovery has its own copy
  and never reads your `exclude_paths`, so changing that value does not change
  what the scan walks.
- Fails **loud** on a duplicate `dag_id`: if two subdirs resolve to the same
  id, lite refuses to compile any of them and prints both paths so you can
  rename one. There is no last-write-wins.

Every compile log line names the resolved config source — either the absolute
path of the `leoflow.yaml` that was loaded, or `auto-defaults: <subdir>` when
none exists. This is the one line to grep when "which config did it pick up?"
is the debugging question.

### `leoflow.yaml` is optional

A subdir with just a `dag.py` is a valid project. Leoflow synthesizes a config
with `dag_id = <subdir-basename>` and every other field filled from the schema
defaults (see [Configuration → Defaults](/reference/configuration/#defaults)). Add a
`leoflow.yaml` when you need to pin a Python version, declare dependencies, set
per-task overrides, or change the `dag_id` to something other than the subdir
name.

## dag.py — the Airflow dialect

`dag.py` is **real Airflow SDK 3.2.x** code, imported by the Python parser via the
Airflow `DagBag`. TaskFlow and classic operators both work:

```python
from airflow.sdk import DAG, task

@task
def extract() -> dict:
    return {"rows": 100}

@task
def transform(data: dict) -> dict:
    return {"rows": data["rows"], "doubled": data["rows"] * 2}

with DAG("my_pipeline", schedule="@daily", catchup=False, tags=["etl"]):
    transform(extract())
```

`from airflow.sdk import …` is the canonical Airflow 3 spelling and what we
recommend. The deprecated-but-still-valid Airflow 3 aliases resolve to the same
objects, so a migrated DAG compiles unchanged:

- `from airflow.decorators import task, dag` → the `airflow.sdk` decorators.
- `from airflow import DAG` → `airflow.sdk.DAG`.

The core `airflow.operators.*` operators, however, were **removed** from Airflow
in 3.0 and relocated to `apache-airflow-providers-standard`. Importing one (e.g.
`from airflow.operators.bash import BashOperator`) fails the compile with a
message pointing at the replacement — use
`from airflow.providers.standard.operators.bash import BashOperator` instead.

### Supported task types

Leoflow runs the common types on a **native fast path** and everything else — any
of Airflow's ~1,500 provider operators and poke-mode sensors — through a **generic
executor**, native-first by type. Nothing is silently dropped or mistranslated.

| Task type | Airflow operator | Where it runs | Notes |
|---|---|---|---|
| `python` | `@task` (TaskFlow) **or** `PythonOperator` | agent in a pod (Pro) / subprocess (Lite) | The general-purpose escape hatch — any provider library you `pip install` is callable from inside a `@task`. |
| `bash` | `BashOperator` | agent | Executes a shell command. |
| ~~`http_api`~~ (removed) | — | — | **Removed (ADR 0047/0048, #512).** `HttpOperator` now compiles to `airflow_operator` and runs in a **pod**, like any other provider operator — declare `connectors: [http]`. The old inline path ran the request in the control-plane process (an SSRF surface) and is gone. |
| `airflow_operator` | **any provider operator/sensor** (Snowflake, S3, Postgres, BigQuery, …) | agent in a pod | The generic executor (ADR 0040): the runtime imports the class, instantiates it with your args, and calls `execute()`. Declare the provider or compile fails. |

{{% alert title="Bash templating: values are shell-quoted, the template text is not" color="warning" %}}
A `bash_command` is Jinja-rendered with the run context (`{{ ds }}`,
`{{ params.x }}`, `{{ var.value.x }}`). The **command text you write** is the
shell structure and is used verbatim; every **interpolated value** is
automatically shell-quoted (`shlex.quote`) before it reaches `bash -c`. This is
a security boundary: `params` is the run's `conf`, which anyone who can
*trigger* the DAG supplies (`execute:dag`, a lower bar than authoring it), so an
un-quoted value would let a trigger inject arbitrary shell (issue #489).

Practical consequence — **write interpolations unquoted**, and do not wrap them
in your own quotes:

- ✅ `process --name {{ params.name }}` — the value is quoted for you.
- ❌ `process --name "{{ params.name }}"` — your quotes fight the auto-quoting.
- A value can no longer expand into multiple shell words/flags; if you need
  that, pass the pieces as separate `params` and interpolate each. For secrets,
  reference `$AIRFLOW_VAR_*` / `$AIRFLOW_CONN_*` env vars instead of rendering
  them into the command.
{{% /alert %}}

Also supported:

- **Trigger rules**: `all_success`, `all_failed`, `all_done`, `one_success`, `one_failed`.
- **XCom**: TaskFlow data-flow (`transform(extract())`) is resolved automatically into typed inputs (`xcom_input`) — for `@task` **and** operator args.
- **Schedule**: cron strings and presets (`@daily`, `0 * * * *`).
- **Dependencies**: linear ordering via TaskFlow calls or `a >> b`.

> Connector cookbooks (postgres, mysql, sqlite, redis, http) are `python`
> tasks that read a managed Connection (`AIRFLOW_CONN_*`) — that is the
> hook-inside-`@task` style. The `airflow_operator` type below is the
> operator-class style; both resolve the same managed connections.

### Provider operators & sensors (`airflow_operator`)

Write the operator exactly as you would in Airflow — Leoflow captures it and runs
it in its own task pod:

```python
from airflow.sdk import DAG, task
from airflow.providers.snowflake.operators.snowflake import SQLExecuteQueryOperator

@task
def build_sql() -> str:
    return "SELECT count(*) FROM events"

with DAG("rollup", schedule="@daily"):
    # an upstream task's output flows straight into an operator arg
    SQLExecuteQueryOperator(task_id="rollup", conn_id="snowflake_default",
                            sql=build_sql())
```

```yaml
# leoflow.yaml — installs apache-airflow-providers-snowflake into the image
connectors: [snowflake]      # or: dependencies: [apache-airflow-providers-snowflake]
```

How it works, and the Phase-A limits:

- The operator runs in its **own task pod**; its hooks resolve connections from
  `AIRFLOW_CONN_*` exactly as inside a `@task`.
- Constructor args must be **JSON-serializable** or an **upstream task's output**
  (wired as XCom, like `sql=build_sql()` above). A non-serializable arg — a
  callable, a `datetime`, an arbitrary object — is a **loud compile error**; move
  that logic into a `@task`.
- Literal args are for **small constants**. A task's literal args (`@task`
  `call_args` and operator `operator_args`) ride as a **single environment
  variable** at dispatch, which POSIX caps at ~128 KiB; `leoflow compile` rejects a
  payload over **100 KiB** with a clear error naming the task. For large data, pass
  a **Connection** or an **external-storage** reference (S3/GCS) and fetch it inside
  the task — never a big dict/list literal.
- The provider must be declared in `leoflow.yaml` (`connectors:` or
  `dependencies:`). If it isn't, `leoflow compile` fails and prints the exact line
  to add — no surprise `ModuleNotFoundError` in the pod.
- **Sensors** run in **poke mode** by default (the pod holds until the
  condition is met), or `mode="reschedule"` (ADR 0040 Phase B, #380/#389) — the
  pod is released between pokes and the scheduler re-dispatches the sensor,
  preserving its `try_number`, once its next-poke time passes.
- **Jinja templating** is best-effort (`render_template_fields` runs with a
  minimal context); for rich macros (`{{ ds }}`, `{{ ti }}`, custom params),
  compute the value in a `@task` and pass it in.
- A native task type (`bash`/`python`) always wins when it matches, so
  `BashOperator`/`HttpOperator`/`PythonOperator` keep their fast path.

### Not supported — `leoflow compile` rejects these

{{% alert title="If your DAG uses any of these, compile fails — by design" color="danger" %}}
The contract is **loud rejection, not silent mistranslation**: every
"skipped" branch would otherwise actually execute at runtime, so a
DAG that imports a sensor or interpolates a Jinja template is
refused at compile with a clear error naming the construct
([#225](https://github.com/dexadata/leoflow/issues/225)).
{{% /alert %}}

The unsupported set, with the things Airflow users most often expect to
"just work" called out first:

- **Deferrable operators / sensors** — anything with `deferrable=True` (it suspends
  the task onto a *trigger*) is not supported yet: Leoflow has no triggerer (ADR
  0040 Phase C). It fails at runtime with a clear message. Pass `deferrable=False`
  — the operator runs synchronously in the pod (poke-style).
- **Jinja templating** in `@task` — `{{ ds }}`, `{{ ti }}`, `{{ var.value.x }}`,
  every `templates_dict=` knob. The control plane never re-parses
  Python and the templating step is intentionally not implemented.
  Workaround: build the values inside the `@task` from `airflow.sdk`
  context.
- **Branching** (`BranchPythonOperator`, `@task.branch`) and
  **short-circuit** (`@task.short_circuit`, `ShortCircuitOperator`) —
  refused for now: the current scheduler does not model
  skipped-vs-executed downstream paths. On the backlog if
  user demand justifies the scheduler change.
- **Virtualenv operators** (`PythonVirtualenvOperator`,
  `@task.virtualenv`) — refused because each DAG already ships as its
  own image with its own dependencies; spinning up a venv at runtime
  is the problem Leoflow's one-image-per-DAG model already solved.
- **Dynamic task mapping** (`.expand` / `.partial`) — refused. Static
  fan-**in** (collecting a list of task calls into a downstream task) works
  today; what does **not** work is *dynamic* fan-**out** — expanding one task
  into N parallel instances at runtime from an upstream result. That is
  tracked separately.
- **KubernetesPodOperator** — refused; the pod is the *runtime
  substrate* for every Leoflow task already, so wrapping a user task
  in another pod is redundant and adds an isolation hole.
- **Datasets / Assets triggers** — not implemented yet; the asset
  graph is a 3.x Airflow feature on the backlog.
- **Per-task `default_args` in `dag.py`** are ignored at the parser
  level — use `leoflow.yaml`'s `tasks.<id>:` override block instead,
  which is checked at compile time.

## leoflow.yaml — deploy config

These are Leoflow concerns, **not** Airflow operator attributes (you cannot invent
kwargs on an operator — the parser imports real Airflow and would raise).

```yaml
schema_version: "1.0"
dag_id: my_pipeline
description: Daily ETL.
owner: data-eng
tags: [etl]
python_version: "3.11"
dependencies:           # pip packages baked into the image
  - pandas==2.1.0
defaults:               # DAG-level defaults (applied to every task)
  retries: 1
  retry_delay_seconds: 30
staging:                # opt-in shared per-run RWX volume (ADR 0022)
  enabled: false
tasks:                  # per-task overrides, keyed by task_id (ADR 0023)
  transform:
    retries: 3
    resources:
      requests: { cpu: "2", memory: 4Gi }
```

### Binding + override layers (ADR 0023)

Config binds to the DAG by `dag_id` and to tasks by `task_id`. Three layers,
**most specific wins**:

```
task override (tasks.<id>)  >  DAG default (defaults)  >  platform default (server)
```

- `tasks.<id>` is merged at **compile** time onto the task in `dag.json`.
- Platform defaults are applied at **dispatch** time, filling gaps the artifact
  left empty (keeps the artifact portable across clusters) — but **wholesale**:
  if a task declares any `resources` at all, even only `ephemeral_storage`, the
  platform cpu/memory default does not apply to it
  ([#802](https://github.com/dexadata/leoflow/issues/802)).
- **`staging` is DAG-level only** — one RWX volume is shared atomically by the
  whole run, so it cannot be per-task.

### Guardrails (fail loudly, never silently)

- A `tasks:` entry naming a `task_id` absent from the DAG → **compile error**.
- A duplicate `task_id` key in the YAML → **parse error**.
- Across a monorepo, a duplicate `dag_id` is a CI-gate concern (one image per DAG).

## The development → deploy lifecycle

```mermaid
flowchart LR
  I[leoflow init] --> D[leoflow lite<br/>hot-reload loop]
  D -->|save & iterate| D
  D --> G[git push]
  G --> CI[CI: leoflow compile --build --push]
  CI --> REG[leoflow push dag.json]
  REG --> PROD[(control plane<br/>immutable artifact)]
```

### 1 · Develop (fast, isolated)

```bash
leoflow init dags/my_pipeline          # scaffold dag.py + leoflow.yaml
leoflow lite dags/my_pipeline           # cluster-mode: real pods on an isolated k3d
# or: leoflow lite --executor=subprocess dags/my_pipeline   (fastest, host venv)
```

Open <http://localhost:8088> — the UI is marked **Leoflow Lite** (silver
edition badge); log in with the admin password generated by `leoflow setup`
(or run `leoflow lite reset-password` if you misplaced it). Edit
`dag.py`/`leoflow.yaml` and **save**; Leoflow recompiles, re-runs the guardrails,
and re-registers. A bad binding prints an error in the terminal **immediately**:

```text
✗ leoflow.yaml tasks: unknown task_id "transfrom"; the DAG defines [extract load transform]
```

Lite is fully isolated from Demo/Pro (own database, cluster, and ports) —
**no split brain**. See [Operating modes](/concepts/editions/).

### 2 · Deploy (authoritative, immutable)

On `git push`, CI compiles + builds + pushes the artifact. The **same** parser,
overlay, and guardrails run as a gate, so what you tested in Lite is what ships:

```bash
leoflow compile dags/my_pipeline --image ghcr.io/org/my_pipeline:$GIT_SHA --build --push -o dag.json
leoflow push dag.json
```

Without `-o` the artifact lands **next to the project**
(`dags/my_pipeline/dag.json`), not in the directory you ran the command from.
The pipelines below pass `-o` so the two lines agree wherever the job happens to
run.

Full, copy-pasteable pipelines for **GitHub Actions, GitLab CI, Google Cloud
Build/Run, and generic runners** are in **[CI/CD & deploy examples](/operate/cicd-deploy/)**.

#### What ends up in the image

`--build` bakes the build context into the image, and with the default
`project: "."` that context is your whole DAG directory. The image is pushed to
a registry and pulled by every pod that runs the DAG, so anything in it is
shared with everyone who can pull it.

`exclude_paths` in `leoflow.yaml` decides what stays out. During the build it is
materialized as a `.dockerignore` in the context — merged with your own if you
have one, and removed afterwards, so the workspace is unchanged when the build
ends. Your rules come first and Leoflow's last, which means `exclude_paths` has
the final word: a `!` re-include in your `.dockerignore` cannot silently defeat
an exclusion you declared in `leoflow.yaml`.

Each entry is written out in the forms Docker honours rather than verbatim.
`.dockerignore` is **not** `.gitignore`: a pattern with no slash matches only at
the context root, so `__pycache__` alone would leave every nested one in the
image. Leoflow emits `p`, `**/p` and `p/**` for a bare name — the last of those
is also what lets `exclude_paths` beat an earlier `!` rule, which re-stating the
plain pattern does not do.

For a dbt project, two things a host-side `dbt parse` leaves behind are
excluded automatically, scoped to each project directory: `logs/` and
`.user.yml`. Both matter beyond image size — `.user.yml` is dbt's
anonymous-usage cookie identifying *your* machine, and it is read by the in-pod
dbt, so every pod from that image reports as you; `logs/dbt.log` carries
absolute paths from the build host.

Nothing else in a dbt project is excluded, deliberately. `target/` holds the
manifest `dbt.manifest` points at, `dbt_packages/` is where `dbt deps`
installs, and `profiles.yml` may be a BYO profile you point `DBT_PROFILES_DIR`
at — each is a real input in a configuration people use, so excluding them
would break working projects.

Credentials are **not** excluded for you either. A `.env` can be a legitimate
input — a DAG calling `load_dotenv()` reads it at run time — and so can a BYO
`profiles.yml`, so dropping either silently would break that project far from
the cause. Instead the build warns:

```console
warning: .env is in the build context and will be baked into the image, which is
pushed to a registry and pulled by every pod that runs this DAG. If it holds
credentials, add ".env" to exclude_paths in leoflow.yaml.
```

Act on it or declare it deliberately; the warning stops once the file is in
`exclude_paths`.

---

See also: [Concepts & glossary](/concepts/core-concepts/) · [Operating modes](/concepts/editions/)
· [HTTP API](/api-reference.html) · [ADR 0023 — binding & overrides](/project/adrs/0023-dag-authoring-config-binding/).
