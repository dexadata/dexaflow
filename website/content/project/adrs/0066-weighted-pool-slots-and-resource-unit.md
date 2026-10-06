---
title: "ADR 0066: Weighted pool slots and an operator resource unit"
linkTitle: "0066 · Weighted pool slots and a resource unit"
weight: 660
description: "ADR 0066: a task takes pool_slots slots of its pool (Airflow semantics), dexaflow.yaml sets it as size, and an optional operator unit turns slots into pod resources, so a pool can be sized in compute instead of in task count."
---

**Status:** Accepted
**Date:** 2026-10-06 (proposed and accepted the same day by the project owner)
**Relates:** ADR 0053 (admission and placement; this ADR changes what the Stage 3 pool gate counts), ADR 0023 (dexaflow.yaml config binding; adds one per-task and one DAG-default knob), ADR 0063 (operator executor policy, proposed; its resource ceiling composes with the unit below), ADR 0011 (strict TDD).

> **Numbering.** 0063 (#1365) and 0064 (#1368) are open PRs. If another ADR
> merges first under 0066, this file is renumbered before merge.

## Context

The pool gate of ADR 0053 Stage 3 admits a task only while its pool has a free
slot, and every task takes exactly one slot (`poolHasSlot` and
`activePoolCounts` in `internal/scheduler/`; the API reports `pool_slots: 1` for
every task instance). A pool therefore caps **how many tasks** run, not **how
much compute** they hold: a pool of 4 admits four tasks of 250m / 512Mi or four
tasks of 8 CPU / 32Gi alike.

That is the wrong axis for anyone who shares a cluster between teams or
tenants and wants to size a team's share in compute:

- A team's budget is CPU and memory. A slot that means "one task of any size"
  makes the budget meaningless as soon as one DAG declares large `resources`.
- Airflow already has the knob for this. A task's `pool_slots` (default 1) is how
  many slots of its pool it takes. Dexaflow ignores it: the parser does not
  capture it and the gate counts 1.
- Nothing ties a slot to resources, so an operator who wants "one slot = N CPU
  and M memory" has to police every DAG's `resources` by hand.

## Decision

### 1. `pool_slots` weights the pool gate

`TaskSpec` gains `pool_slots` (JSON `pool_slots`, integer, 0 or absent means 1),
with Airflow's meaning: the number of slots the task takes in its pool while it
is queued or running.

- The gate admits a task only when `occupied + promoted_this_tick + pool_slots <=
  slots`. Occupancy is the **sum** of `pool_slots` of the pool's queued and
  running task instances, not their count; the within-tick folding across runs
  (ADR 0053) sums the same way.
- A task that does not fit stays `scheduled`, exactly like a task that finds the
  pool full today. Nothing fails.
- `max_active_tasks` (Stage 1) keeps counting tasks: it is a per-DAG fan-out cap,
  not a compute budget.
- **No behavior change for existing DAGs**: none sets `pool_slots` today, so every
  task weighs 1, as now. Lite has no pools and ignores the field.
- The API and the UI report the real `pool_slots`, and pool occupancy
  (`PoolSlotUsage`) sums it.

### 2. Authoring

- `dag.py`: `@task(pool_slots=N)` and an operator's `pool_slots=N` are captured
  by the parser, the same way `retries` is.
- `dexaflow.yaml`: `tasks.<task_id>.size: N` and `defaults.size: N`. The YAML
  name is `size` because, once a unit is configured (section 3), the number is
  the size of the task, and that is how an author thinks about it. It compiles to
  `pool_slots`. Precedence follows ADR 0023, most specific wins: the YAML task
  override > the task's own `pool_slots` in `dag.py` > the YAML `defaults.size` > 1.
- Registration refuses `pool_slots < 1` or above a ceiling of 1024.

### 3. Optional operator resource unit

Server config `executor.unit.cpu` and `executor.unit.memory` (Kubernetes
quantities; both or neither). Unset is the default and changes nothing. When
set:

- **Dispatch.** A task that declares no `resources` gets
  `requests = limits = pool_slots x unit` (Guaranteed QoS). This replaces the L0
  `executor.defaults.resources` for that task; the default stays in force while
  no unit is configured.
- **Registration.** A task that declares its own `resources` must fit in its
  size: every requested or limited cpu and memory must be at most
  `pool_slots x unit`. Otherwise registration fails with the size the task
  would need, for example `task "train" asks for 2 CPU, which is 8 units of
  250m; set size: 8`. The engine never clamps or rewrites the author's
  resources (the rule ADR 0063 also follows).
- **Dispatch, again.** A DAG registered before the unit was configured is
  checked at dispatch with the same rule, and a task that does not fit fails
  with the same message instead of running larger than it is charged.
- Ephemeral storage and DRA claims are outside the unit; ADR 0063's ceiling
  covers them.
- With ADR 0063: the policy's `resources.max` stays the per-task ceiling in
  absolute quantities, and the unit makes the pool a ceiling on the sum.

### 4. A large task is not starved

Weighted admission has a failure mode that unweighted admission did not: a task
of 4 slots waiting in a pool of 6 never fits while tasks of 1 slot keep taking
the space as it frees. The scheduler keeps an in-memory **reservation** per pool:

- When a scheduled task does not fit and has been waiting longer than
  `scheduler.pool_starvation_threshold` (default 60s, 0 disables), the pool is
  reserved for that task. While reserved, the pool admits no other task; the
  reserving task is admitted as soon as it fits, which releases the reservation.
- One reservation per pool at a time: the oldest waiting task wins, by the time
  the scheduler first saw it blocked.
- A reservation whose task is no longer `scheduled` (run cancelled, task
  skipped) is dropped on the next tick.
- A task larger than its pool's total slots is never reserved for (it would
  freeze the pool); it waits, and the scheduler logs a warning naming the pool
  and the sizes once per task. Registration-time limits (section 5) are the way
  to refuse it up front.
- The state lives only in the leader. A failover forgets the waiting times, so
  the worst case is one extra threshold of waiting; no new table or query.

### 5. Per-tenant ceiling on a task's size

The service tenant limits (migration 040) gain `max_task_pool_slots`: 0 is
unlimited; otherwise registration refuses a DAG with a task whose `pool_slots`
is above it, naming the task and both numbers. A platform that sizes each
tenant's default pool sets it to that pool's slots, so a task that could never
fit is refused when it is pushed instead of waiting forever.

## Consequences

- An operator can size a team's or tenant's share in compute: unit = 250m /
  512Mi and a default pool of 8 slots means 2 CPU and 4Gi at once, whatever the
  mix of task sizes.
- Pools stay task-count pools for every install that sets no unit and no
  `pool_slots`.
- The pool gate's cost does not change: weights come from the specs the tick
  already loads, the reservation map is O(pools), and no query is added.
- Delivered in independent PRs, each with its tests first: (1) `pool_slots` in
  the spec, parser, YAML and the weighted gate; (2) the unit at dispatch and
  registration; (3) the starvation reservation; (4) `max_task_pool_slots`.
