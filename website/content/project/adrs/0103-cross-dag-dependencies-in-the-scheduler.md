---
title: "ADR 0103: Cross-DAG waits and triggers are evaluated by the scheduler, without a pod"
linkTitle: "0103 · Cross-DAG dependencies in the scheduler"
weight: 1030
description: "ADR 0103: ExternalTaskSensor and TriggerDagRunOperator compile to declarative task types that the scheduler evaluates against its own database, scoped to the tenant, with no pod and no user code in the control plane. A waiting task sits in the reserved deferred state and wakes when the task it waits for settles. This is the one place where deferrable=True is honored, because the trigger is built in."
---

**Status:** Accepted
**Date:** 2026-11-04
**Accepted:** 2026-11-04 (proposed and accepted the same day by the project owner)
**Amends:** ADR 0016 (deferrable tasks, a conscious non-goal) for these operators only: their wait is built in, so honoring `deferrable=True` runs no user code.
**Relates:** ADR 0040 (Airflow operator support), ADR 0043 (TaskGroup), ADR 0048 (no user code in the control plane), ADR 0051 (orchestration vs execution state machines), ADR 0064 (branching and skip)
**Issues:** #2340 (this ADR), #2336 (compile-time refusal until this ships), #2338 (TaskGroup)

## Context

Airflow users express a dependency between DAGs with three standard operators:

- `ExternalTaskSensor` waits until a task (or a whole run) of another DAG
  reaches an allowed state, for the run whose logical date matches its own,
  shifted by `execution_delta`.
- `TriggerDagRunOperator` starts a run of another DAG and, with
  `wait_for_completion=True`, waits for it to finish.
- `ExternalTaskMarker` makes a clear of the upstream task also clear the
  matching downstream task in the other DAG.

On Dexaflow they compile as `airflow_operator` and fail on their first attempt
(documented in #2319, refused at compile by #2336). In Airflow 3 each of them
reads or writes the metadata database through the task runner's supervisor
(`ti.get_ti_count`, `ti.get_dr_count`, `ti.get_task_states`, and a
`DagRunTriggerException` the runner turns into a new run). A Dexaflow task pod
has no such channel by design.

Airflow itself already treats this wait as control-plane work. With
`deferrable=True`, `ExternalTaskSensor` hands the wait to the triggerer as a
built-in `WorkflowTrigger` that does nothing but count task instances in the
metadata database every `poke_interval`; `TriggerDagRunOperator` uses a
built-in `DagStateTrigger` the same way. No user code runs in either trigger.

Two designs were weighed:

- **A. Run the real operator in the pod, and give the runtime the missing
  reads.** The agent would answer `get_ti_count` and its siblings by asking
  the control plane. The provider class runs unchanged, `execution_date_fn`
  works. But every poke costs a pod start (on GKE, scheduling, image pull and
  start, typically 5 to 20 seconds, more on Autopilot), a sensor waiting for
  hours holds node capacity and quota in poke mode, and the task pod gains a
  new read path into the control plane's state, which widens what a
  compromised task can learn.
- **B. Compile the operator to a declarative task the scheduler evaluates.**
  No pod, no new pod-to-control-plane channel, no user code: the scheduler
  answers the question from the tables it already owns, as Airflow's
  triggerer does.

## Decision

Take **B**.

1. **Two new task types in `dag.json`.** The compiler maps the operators to
   them instead of `airflow_operator`:

   | Operator | `type` | Captured fields |
   | --- | --- | --- |
   | `ExternalTaskSensor` | `external_wait` | `external_dag_id`, `external_task_ids` (from `external_task_id` or `external_task_ids`; empty means the whole run), `allowed_states`, `failed_states`, `skipped_states`, `execution_delta` (seconds), `check_existence`, `poke_interval`, `timeout`, `soft_fail` |
   | `TriggerDagRunOperator` | `trigger_dag_run` | `trigger_dag_id`, `conf` (JSON literal), `logical_date` (literal or absent), `trigger_run_id` (literal or absent), `reset_dag_run`, `wait_for_completion`, `allowed_states`, `failed_states`, `poke_interval` |

   `mode` and `deferrable` are accepted and ignored: every mode takes the same
   path. Every captured value must be a JSON literal. The compiler refuses,
   with a message naming the field: `execution_date_fn` and any other
   callable; a value containing a Jinja expression (rendering an author's
   template in the control plane would run the author's code); and
   `external_task_group_id` until TaskGroup exists (#2338).

2. **The scheduler evaluates them; nothing is dispatched.** When an
   `external_wait` task's own dependencies are met, the planner does not hand
   it to the dispatcher. It moves the task to the reserved `deferred` state
   (migration 006), which the Airflow UI already renders, and evaluates the
   condition. The state machine (ADR 0051) gains `none` to `deferred`, and
   `deferred` to `success`, `failed`, `none` (a clear) and `removed`. A
   deferred task holds no pool slot, as pools already count only queued and
   running tasks.

   - the target run is the one of `external_dag_id` in the **same tenant**
     whose logical date equals this run's logical date minus
     `execution_delta`, an exact match as in Airflow (a manual run therefore
     matches only a target run with the same logical date). When several
     target runs share that logical date, the most recently created one
     counts;
   - **met**: every target task (or the run, when no task is named) is in
     `allowed_states` (default `success`). The task succeeds.
   - **failed**: any target is in `failed_states`. The task fails, or is
     skipped when `soft_fail` is set; the skip waits for the skip support of
     ADR 0064, and until then `soft_fail=True` is refused at compile.
   - **not yet**: the task stays `deferred` with `reschedule_at` set to now
     plus `poke_interval`. This reuses the column the reschedule rail added
     (#380), and like a reschedule it consumes no try.
   - **timeout**: when `timeout` (Airflow's default, seven days) has passed
     since the task first deferred, it fails with a reason that names the
     target. Two DAGs that wait on each other for the same date therefore end
     in two timeouts, not in a stuck scheduler.
   - `check_existence=True` fails the task at once when the target DAG or task
     does not exist in the tenant.

3. **Wake on the event, poll as a fallback.** When a task instance or run
   settles, the scheduler sets `reschedule_at` to now for the waiters on that
   (tenant, DAG, logical date), so a wait ends on the next tick instead of
   after a full `poke_interval`. The periodic check stays as the fallback,
   and is batched: one query per tick for all due waiters, served by an index
   on `(tenant_id, dag_id, logical_date)` for runs and the existing task
   instance keys. All state is in the database, so a waiter survives a leader
   change and a server restart.

4. **Triggering a run is a control-plane write with a deterministic id.** A
   `trigger_dag_run` task creates the run of `trigger_dag_id` in the same
   tenant, with `conf`, the given or the current logical date, and run id
   `trigger_run_id` or, when absent, a deterministic id derived from the
   source run and task. The insert is idempotent on that id, so a retry or a
   leader change never starts a second run. `reset_dag_run=True` clears the
   existing run instead of failing on the duplicate. The run honors the
   target DAG's `max_active_runs` and paused state, records the source DAG,
   run and task, and writes an audit event. With `wait_for_completion=True`
   the task then waits on that run exactly as in item 2; otherwise it
   succeeds once the run exists.

5. **Tenant scope is absolute.** Both types resolve the target DAG inside the
   task's tenant only. A target in another tenant is indistinguishable from a
   missing one. No pod gains any read or write path it does not have today.
   Roles are per tenant today, so a DAG that may run in a tenant may trigger
   any DAG of that tenant, as an author with the trigger permission could by
   hand. If per-DAG permissions are ever added, `trigger_dag_run` is checked
   against them.

6. **`ExternalTaskMarker` comes in a second phase.** It compiles to a no-op
   marker carrying `external_dag_id`, `external_task_id` and
   `recursion_depth`, and the clear API follows it into the downstream DAG
   (same tenant, depth bounded by `recursion_depth`, default 10). Until that
   phase it stays refused at compile.

7. **Lite gets it for free.** The scheduler is the same on Lite, and the
   design needs no pod, so both types work there unchanged.

## Consequences

- A cross-DAG wait costs one indexed query per tick for all waiters and no
  pod, instead of a pod per poke. On a busy cluster this is the difference
  between a wait that is free and one that competes with real work for nodes.
- The scheduler now carries sensor semantics that Airflow keeps in the
  provider (`allowed_states`, `execution_delta`, the run match). They are a
  small, stable surface, and a contract test runs the same cases against the
  provider's own `ExternalTaskSensor` logic so the two do not drift.
- `execution_date_fn`, templated fields and `soft_fail` (until ADR 0064) are
  refused. These are the cases where the operator would need author code or
  a state Dexaflow does not have yet; the refusal is loud, at compile.
- ADR 0016 stays the rule for every other operator: `deferrable=True` is
  honored here only because the trigger is built in.
- The UI's DAG dependencies view (`/ui/dependencies`, a stub today) can be
  filled from the new task types, since the compiler records every
  cross-DAG edge.

## Rollout

1. `external_wait` for `ExternalTaskSensor`: compiler, scheduler, `deferred`
   state wiring, event wake, docs. Removes the compile refusal of #2336 for
   that class.
2. `trigger_dag_run` for `TriggerDagRunOperator`, including
   `wait_for_completion`.
3. `ExternalTaskMarker` and clear propagation; `/ui/dependencies`.
4. After #2338: `external_task_group_id`. After ADR 0064: `soft_fail`.

## Alternatives rejected

- **A, the agent answers the metastore reads** (see Context): faithful to the
  provider class, but it costs a pod per poke and opens a new read path from
  every task pod into the control plane's state.
- **Assets** (Airflow 3's data-aware scheduling) express "run when this data
  is ready" rather than "run after that task", and do not cover existing DAGs
  written against `ExternalTaskSensor`. They are complementary and stay a
  separate design.
- **Ask authors to rewrite the wait as a sensor on the data** (for example
  `S3KeySensor`): this is the documented workaround today, not a migration
  path for DAGs that already use the standard operators.
