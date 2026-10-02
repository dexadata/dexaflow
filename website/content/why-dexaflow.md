---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /why-leoflow.html
# --- end AUTO redirect aliases ---
  # The page was /why-leoflow/ before the rename.
  - /why-leoflow/
title: Why Dexaflow
weight: 5
description: "The five wounds Airflow won't heal, and how Dexaflow heals them."
type: docs
menu: { main: { weight: 5 } }
---

Apache Airflow is the most deployed workflow orchestrator on earth — and the one
that bleeds most in production. Dexaflow keeps everything engineers love (the UI,
the vocabulary, the pod-per-task model) and cuts out the part that hurts: the
**Python control plane**.

## The five wounds Airflow won't heal — and how Dexaflow heals them

| Airflow wound | Dexaflow cure |
|---|---|
| **The scheduler that stalls** — seconds between tasks; minutes-long pipelines that should take seconds. | A **Go scheduler** (goroutines, no GIL): a tight state-machine loop, no Python re-parse tax. |
| **The triggerer that suffocates** — past ~500 sensors the asyncio loop chokes and SLAs miss. | Go concurrency, not a single asyncio loop — backpressure-friendly by design. |
| **The DAG file that re-parses itself to death** — every loop opens every `.py` in `/dags`; CPU spikes for nothing. | **DAGs are immutable artifacts** (`dag.json` + image). Parsed once, at compile. The control plane never re-parses Python. |
| **The worker that leaks until it dies** — long-lived Celery workers accumulate FDs/connections, OOMKilled at 3 a.m. | **Pod-per-task**: each task is a fresh, ephemeral pod. Nothing to leak. |
| **The dependency hell with no door** — `pandas==1.0` vs `2.0`, one image, pick a side. | **One image per DAG**: each DAG carries its own dependencies. No shared `/dags`, no conflicts. |

## What you keep
- The **Airflow 3.2.x UI** (grid, graph, dashboard) — unmodified. ([UI compatibility](/concepts/ui-compatibility/))
- The **vocabulary**: DAGs, tasks, runs, XCom, trigger rules. ([Concepts](/concepts/core-concepts/))
- The **TaskFlow** authoring you already know. ([DAG authoring](/author-dags/dag-authoring/))

## What you gain
- A **real dev loop** (`dexaflow lite` — isolated cluster, hot reload). ([Operating modes](/concepts/editions/))
- **GitOps**: every DAG is a versioned, immutable artifact built in CI. ([Deploy](/operate/cicd-deploy/))
- A control plane you can actually **operate** — Go, observable, no GIL.

<a class="btn btn-lg btn-primary me-3 mb-3" href="/get-started/quickstart/">Get started</a> <a class="btn btn-lg btn-secondary me-3 mb-3" href="/concepts/architecture/">See the architecture</a>
