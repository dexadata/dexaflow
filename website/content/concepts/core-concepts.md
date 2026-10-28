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
| **Scheduler-created runs** (cron, `@daily`, `@hourly`, …) | ❌ Suspended: no new scheduled runs are created | ✅ Created at each interval |
| **Catchup backfills** | ❌ Suspended: backlog accrues silently until you unpause | ✅ Created on each tick (subject to `catchup`/`max_active_runs`) |
| **Manual triggers** (UI **Trigger DAG** button, `POST /api/v2/dags/{id}/dagRuns`, `dexaflow deploy --trigger`) | ⏸ Accepted and **queued**: the run is created, but it does not start until you unpause | ✅ Run |
| **Queued runs** (created before the pause, or re-queued by a clear) | ⏸ Wait in `queued`: no new task is started or dispatched; tasks of a cleared run that were already running finish | ✅ Start on the next scheduler tick, oldest first, up to `max_active_runs` running |
| **Running runs** (in state `running` when the pause flipped) | ✅ Continue to completion: pause is not a kill switch | ✅ Continue |

This mirrors Apache Airflow 3: *paused* stops the scheduler from starting
work. A trigger on a paused DAG is still accepted (the API answers with the
new run in state `queued`, as Airflow does), and the run waits until someone
unpauses the DAG with the switch in the UI or
`dexaflow admin dags unpause <dag_id>`. On the next scheduler tick after the
unpause, the waiting runs start, as many as `max_active_runs` allows.

**`max_active_runs` caps runs twice.** Neither the schedule nor a trigger
creates a run while the DAG has `max_active_runs` runs queued or running. And,
as in Airflow, the scheduler moves a queued run to `running` only while fewer
than `max_active_runs` runs of the DAG are running, oldest logical date first
(close to Airflow's `run_after` order); the others stay `queued`, with nothing
dispatched, until a running run finishes. A clear can leave more queued runs
than the cap, since it re-queues a finished run whatever the count: they start
one free slot at a time, after an unpause too, not all at once. A run that a
clear re-queued goes back to `running` the same way, with a new start time.
Only the scheduler starts a run: `PATCH /api/v2/dags/{dag_id}/dagRuns/{run_id}`
sets a run to `queued`, `success` or `failed`, as in Airflow, and a PATCH to
`running` stores `queued` (on a run already running it changes nothing), so
the scheduler starts the run under the pause and the cap.

Before 0.5.4 a manual trigger on a paused DAG started at once. With new DAGs
paused at creation (below), a first `dexaflow deploy --trigger` now leaves its
run queued: unpause the DAG and the run starts.

**Running runs.** Pausing a DAG does not touch a run in state `running`: its
tasks keep being scheduled and dispatched until the run finishes. A run that a
clear put back in `queued` is a queued run, even while some of its tasks are
still running: while its DAG is paused those tasks finish, and nothing
downstream or retried starts until the unpause.
This is a deliberate, conservative difference from Airflow 3, whose scheduler
also stops scheduling new tasks of a running run while its DAG is paused
(the tasks already running finish, and the rest wait for the unpause). To stop
a running run, mark it failed or delete it.

If you want a real "no runs at all" switch, the supported pattern is the same as
in Airflow: pause the DAG. Runs triggered while it is paused queue up instead of
running; delete them from the run list if they should never run.

### New DAGs start paused

As in Airflow, a DAG registered for the first time starts **paused**: its
schedule creates no run, catchup included, until someone unpauses it with the
switch in the UI or `dexaflow admin dags unpause <dag_id>`. Manual triggers
are accepted but wait in `queued` until then, as the table above says. Two
settings decide the first state, in this order:

1. The DAG's own `is_paused_upon_creation` argument, when it sets one:
   `DAG("etl", is_paused_upon_creation=False)` registers active,
   `True` registers paused.
2. The server setting `DEXAFLOW_DAGS_ARE_PAUSED_AT_CREATION` (default `true`;
   on Helm set the chart value `config.dagsArePausedAtCreation`, not
   `extraEnv`). Set it to `false` to register new
   DAGs active, as releases before 0.5.4 did.

Both apply **only when the DAG is created**. Deploying a new version of an
existing DAG never pauses or unpauses it: an active DAG stays active, and its
next scheduled run uses the new version; a paused DAG stays paused. The one
exception is asking for it: `dexaflow deploy --paused` or `--active` leaves
the DAG in that state after this deploy, new or not, and the change is
recorded in the audit log (`dag.pause` or `dag.unpause`), as a pause from the
UI is. A DAG that
is deleted (`dexaflow dags delete --deregister`) and deployed again is created
again, so it comes back paused.
`dexaflow lite` registers new DAGs active, so a DAG you run locally keeps its
schedule from the first save.

### Why queue manual triggers instead of refusing them?

Refusing a trigger on a paused DAG would break the Airflow API contract that
the UI and existing clients rely on: `POST /api/v2/dags/{id}/dagRuns` answers
with the created run whatever the pause state. Queueing it keeps that
contract and still gives pause one meaning: nothing of this DAG starts until
it is unpaused. To force a single run of a paused DAG during an incident,
trigger it and unpause the DAG; pause it again once the run has started, and
the run keeps going (see **Running runs** above).

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
