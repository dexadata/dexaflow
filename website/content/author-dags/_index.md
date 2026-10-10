---
title: Author DAGs
linkTitle: Author DAGs
weight: 20
description: Author DAGs on the Airflow SDK — operators, dbt, variables & connections, alerting, map-reduce, and worked examples.
cascade: { type: docs }
menu:
  main:
    weight: 20
---

Everything about writing DAGs for Dexaflow. A DAG is a `dag.py` on the Airflow Task
SDK plus a `dexaflow.yaml` for packaging and bindings, compiled to one immutable
artifact.

New to authoring? Start with **[DAG authoring](/author-dags/dag-authoring/)** for
the project layout and compile model, then reach for the guide that matches your
task below.

<div class="lf-cards">
  <a class="lf-card lf-card--hero" href="/author-dags/dag-authoring/">
    <span class="lf-card__badge">Start here</span>
    <span class="lf-card__icon"><i class="fa-solid fa-pen-ruler"></i></span>
    <span class="lf-card__title">DAG authoring</span>
    <span class="lf-card__desc">The project layout, the two files (<code>dag.py</code> + <code>dexaflow.yaml</code>), and the compile model.</span>
    <span class="lf-card__more">Learn the model →</span>
  </a>
  <a class="lf-card" href="/author-dags/dbt/">
    <span class="lf-card__icon"><i class="fa-solid fa-cubes-stacked"></i></span>
    <span class="lf-card__title">dbt in your DAGs</span>
    <span class="lf-card__desc">Drop <code>dbt_group()</code> into a <code>dag.py</code> and your models become tasks in the same graph — pod-per-model, no Cosmos.</span>
    <span class="lf-card__more">Put dbt in a DAG →</span>
  </a>
  <a class="lf-card" href="/author-dags/airflow-compatibility/">
    <span class="lf-card__icon"><i class="fa-brands fa-python"></i></span>
    <span class="lf-card__title">Airflow compatibility</span>
    <span class="lf-card__desc">You write standard Airflow Task SDK code; Dexaflow adds a thin runtime and <code>dexaflow.yaml</code> — it never re-implements Airflow's Python API.</span>
    <span class="lf-card__more">See the model →</span>
  </a>
  <a class="lf-card" href="/author-dags/map-reduce/">
    <span class="lf-card__icon"><i class="fa-solid fa-diagram-project"></i></span>
    <span class="lf-card__title">Map-reduce for ML</span>
    <span class="lf-card__desc">Fan-out + reduce expressed as a Python list comprehension — native, no extra operators.</span>
    <span class="lf-card__more">Fan out & reduce →</span>
  </a>
  <a class="lf-card" href="/author-dags/operators-sensors/">
    <span class="lf-card__icon"><i class="fa-solid fa-plug-circle-bolt"></i></span>
    <span class="lf-card__title">Operators &amp; sensors</span>
    <span class="lf-card__desc">Use Airflow operators and sensors from your tasks.</span>
    <span class="lf-card__more">Use operators →</span>
  </a>
  <a class="lf-card" href="/author-dags/variables-connections/">
    <span class="lf-card__icon"><i class="fa-solid fa-key"></i></span>
    <span class="lf-card__title">Variables &amp; Connections</span>
    <span class="lf-card__desc">Expose Variables and Connections to your task pods, delivered by the control plane.</span>
    <span class="lf-card__more">Wire secrets in →</span>
  </a>
  <a class="lf-card" href="/author-dags/alerting/">
    <span class="lf-card__icon"><i class="fa-solid fa-bell"></i></span>
    <span class="lf-card__title">On-failure alerting</span>
    <span class="lf-card__desc">Notify on run failure straight from <code>dexaflow.yaml</code> — no extra task.</span>
    <span class="lf-card__more">Set up alerts →</span>
  </a>
  <a class="lf-card" href="/author-dags/lite-web-editor/">
    <span class="lf-card__icon"><i class="fa-solid fa-window-maximize"></i></span>
    <span class="lf-card__title">The Lite web editor</span>
    <span class="lf-card__desc">Edit and run DAGs from the browser — the fastest inner loop for local dev.</span>
    <span class="lf-card__more">Edit in the browser →</span>
  </a>
  <a class="lf-card" href="/author-dags/examples/">
    <span class="lf-card__icon"><i class="fa-solid fa-flask"></i></span>
    <span class="lf-card__title">Examples</span>
    <span class="lf-card__desc">Runnable example DAGs you can copy and adapt.</span>
    <span class="lf-card__more">Browse examples →</span>
  </a>
  <a class="lf-card" href="/author-dags/etl-case-study/">
    <span class="lf-card__icon"><i class="fa-solid fa-database"></i></span>
    <span class="lf-card__title">ETL case study</span>
    <span class="lf-card__desc">A worked 1&nbsp;GB ETL on the ephemeral per-run staging volume.</span>
    <span class="lf-card__more">Read the case study →</span>
  </a>
</div>
