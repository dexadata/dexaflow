---
title: Glossary
linkTitle: Glossary
weight: 70
description: The core Leoflow vocabulary — DAG, Task, TaskInstance, DagRun, XCom, Executor, Agent — kept compatible with Airflow.
---

Leoflow keeps Airflow's vocabulary so the UI and mental model are familiar. For how
these pieces fit together at runtime, see [Core concepts](/concepts/core-concepts/).

| Term | Meaning |
|---|---|
| **DAG** | Directed Acyclic Graph of tasks. An **immutable artifact**: a `dag.json` + a container image, versioned together (ADR 0003). |
| **Task** | A unit of work in a DAG — a `task_id`, a type (`python`/`bash`/`airflow_operator`), and config. |
| **TaskInstance** | One execution of a Task within a DagRun. Has state. |
| **DagRun** | One execution of a DAG, identified by `dag_id` + `logical_date`. |
| **Logical date** | The "business" date of a run (Airflow 3's rename of `execution_date`). |
| **Trigger rule** | When a task runs based on upstream states (`all_success`, `one_failed`, …). |
| **XCom** | Small (≤256 KB) typed value passed between tasks. Stored in **Postgres** on Lite (no Redis, see [ADR 0026](/project/adrs/0026-lite-datastore-no-redis/)) and in **Redis** on Pro. Writes are **last-write-wins** — two parallel tasks pushing the same key produce the later write with no conflict detection ([#198](https://github.com/dexadata/leoflow/issues/198)). |
| **Executor** | Runs a task physically: Kubernetes (pod-per-task) or subprocess (dev). With warm pools on (Pro), one pod is reused across many attempts of the same DAG version (N:1) instead of one pod per attempt — ADR 0058. HTTP runs in a pod via the provider (ADR 0047, superseding the inline path). |
| **Agent** | Small Go binary (PID 1) inside the task container that talks gRPC to the control plane. |
