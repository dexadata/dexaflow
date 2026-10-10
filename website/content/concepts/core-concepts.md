---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /concepts.html
# --- end AUTO redirect aliases ---
title: Core concepts
linkTitle: Core concepts
weight: 20
description: How a DAG, its runs, and its tasks fit together — pause semantics, and why each DAG is its own container image.
---

Dexaflow keeps Airflow's vocabulary so the UI and mental model are familiar. If you
just need the term definitions, see the [Glossary](/reference/glossary/); this page
covers how the pieces behave at runtime.

## What "paused" means (and what it does **not** mean)

A **paused** DAG is one whose `is_paused` flag is `true` in the metadata database
— typically toggled with the on/off switch next to the DAG name in the UI.

| Action | Paused DAG | Unpaused DAG |
|---|---|---|
| **Scheduler-created runs** (cron, `@daily`, `@hourly`, …) | ❌ Suspended — no new scheduled runs are created | ✅ Created at each interval |
| **Catchup backfills** | ❌ Suspended — backlog accrues silently until you unpause | ✅ Created on each tick (subject to `catchup`/`max_active_runs`) |
| **Manual triggers** (UI **Trigger DAG** button, `POST /api/v2/dags/{id}/dagRuns`) | ✅ **Always run** — pause does not gate manual triggers | ✅ Run |
| **In-flight runs** (already created when the pause flipped) | ✅ Continue to completion — pause is not a kill switch | ✅ Continue |

This is **intentional** and mirrors Apache Airflow's contract: *paused* gates the
scheduler, not the operator. An operator who triggers a DAG manually has already
acknowledged the side effects — pause is for *quieting the cron*, not for
*disabling the pipeline*.

If you want a real "no runs at all" switch, the supported pattern is the same as
in Airflow: pause the DAG **and** instruct operators not to trigger it. There is
no separate "disable" flag.

### Why not block manual triggers too?

Three reasons we kept Airflow's behavior:

1. **Operations parity.** Sites migrating from Airflow have runbooks ("trigger
   `etl_recover` manually if scheduler is paused") that depend on this.
2. **Debugging.** Pausing a flapping DAG and then manually triggering one good
   run to capture a clean trace is a common pattern.
3. **Escape hatch.** During an incident, the on-call may need to force a single
   run without unblocking the entire schedule.

If you triggered a paused DAG by accident, **delete the run** from the run list
— that's the supported undo. Pausing again afterward will not retroactively
suspend it.

## Why "DAG = image"

Airflow's pod-per-task model is right; its Python control plane is the bottleneck.
Dexaflow keeps the model, rewrites the control plane in Go, and makes **each DAG its
own container image** — no shared `/dags` filesystem, no dependency hell. See
[ADR 0001](/project/adrs/0001-why-leoflow/) and
[ADR 0003](/project/adrs/0003-dag-as-image/).

## When you pay for a pod (and when you don't)

Every task runs in **its own fresh pod** — one task, one pod, by default
([ADR 0002](/project/adrs/0002-pod-per-task/)). That is deliberate: each task gets
**failure isolation** (an OOM or crash can't touch a sibling), **its own retry**,
**right-sized CPU/memory**, and **its own secret scope**. For production — and
especially for audit and compliance — those per-task properties are the feature,
not overhead.

The trade is a **cold start per task** (image pull, schedule, container start, the
agent handshake). For a DAG of many short tasks that overhead can dominate. Dexaflow
gives you three levers so you don't over-pay — pick by the situation, not by
reaching for a generic "pack tasks together" switch (there isn't one — see below):

| Situation | Lever | What it does |
|---|---|---|
| **Iterating on DAG logic locally** | `dexaflow lite --executor subprocess` | Runs tasks as host processes — **no pods, no image build** — the fast inner loop. (`--executor auto`, the default, uses a real k3d pod-per-task when Docker is present, for fidelity.) Dev-only, unsandboxed. |
| **A dbt project with many models** | [`dbt_group()`](/author-dags/dbt/) with `granularity: level` or `folder` | Packs the project's models into **grouped tasks** (each group is one `dbt build` = one pod) that dbt orchestrates internally, so *N* models needn't be *N* pods. The default `granularity: node` is one pod per model. |
| **Amortizing cold start across attempts** | [Warm worker pools](/operate/warm-pools/) (Pro, operator-set) | Reuse **one pod across many attempts of the same DAG version** ([ADR 0058](/project/adrs/0058-warm-worker-pools/)). An operator knob, not a DAG attribute — by design; it never groups *different* tasks. |

{{% alert title="There is no generic pack-tasks-into-one-pod knob" color="info" %}}
Fusing arbitrary operators into a single pod (a generic fused `TaskGroup`) is
**designed but deliberately not built** ([ADR 0043](/project/adrs/0043-taskgroup-split-fused-execution/)):
the levers above already cover the real pod-cost cases, and fusing would trade away
the per-task isolation, retry, and secret scoping that pod-per-task exists to give.
If you hit genuine pod-startup pain on a **non-dbt** DAG of many tiny tasks, that's
the signal to revisit ADR 0043 — not a gap to work around.
{{% /alert %}}

See also: [Architecture](/concepts/architecture/) ·
[DAG authoring](/author-dags/dag-authoring/) · [Glossary](/reference/glossary/).
