---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /python-api.html
# --- end AUTO redirect aliases ---
title: Python runtime API
linkTitle: Python runtime
weight: 40
description: The leoflow_runtime package that runs your task callable inside the container and bridges its return value to XCom.
---

The `leoflow_runtime` package runs **your task callable** inside the container and
bridges its return value to XCom. It is installed in the DAG image (and the dev
venv); your `dag.py` uses the **Apache Airflow Task SDK** (`from airflow.sdk import
DAG, task`), and the agent invokes `leoflow_runtime` to execute the callable.

<div class="lf-cards">
  <a class="lf-card lf-card--hero" href="/python-api/leoflow_runtime.html">
    <span class="lf-card__badge">Docstrings</span>
    <span class="lf-card__icon"><i class="fa-brands fa-python"></i></span>
    <span class="lf-card__title">Open the Python API reference (docstrings)</span>
    <span class="lf-card__desc">The full generated docstring reference — every module, class, and function in <code>leoflow_runtime</code>, rendered from source by <a href="https://pdoc.dev/">pdoc</a>. Opens the self-contained reference subsite.</span>
    <span class="lf-card__more">Open the reference →</span>
  </a>
</div>

It is a **sidecar**: pdoc's output does not share this site's Docsy theming, so it
is served verbatim under `/python-api/` and linked (not embedded) from here. The
generator is `website/scripts/gen-python.sh`, run on every build so the reference
never drifts from the package.

Modules documented there:

- `dexaflow`: the authoring primitives a `dag.py` imports, today
  `dbt_group(name)`, which embeds a dbt project declared under `dbt_groups:` in
  `dexaflow.yaml` as a task group (see [dbt](/author-dags/dbt/)). DAGs written
  before the rename import the same names from `leoflow`.
- `leoflow_runtime` — the package entry points (`run`, `xcom_pull`).
- `leoflow_runtime.runner` — executes the task callable and captures its result.
- `leoflow_runtime.xcom` — reads and writes XCom from inside the task container.
- `leoflow_runtime.dbt` — the dbt adapter bridge.
