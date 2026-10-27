---
title: "ADR 0096: every finished task attempt writes a usage event in the same transaction, read through the service API"
linkTitle: "0096 · Task attempt usage events"
weight: 960
description: "ADR 0096: a trigger on task_instances writes one row to a usage_events table each time an attempt reaches an attempt-ending state, in the transaction that moves it there, with the tenant, the task, the try, the attempt epoch, the state, the size and the times. An operator reads the table in order through a service API route with a cursor, and the retention janitor prunes it. Per-tenant counters are derived from the same rows. Events are delivered at least once and keyed by attempt and revision. Recording is off until an operator enables it."
---

**Status:** Proposed
**Date:** 2026-10-26
**Relates:** ADR 0051 (orchestration and execution state machines, the attempt epoch), ADR 0066 (weighted pool slots, a task's size), ADR 0068 (patch safety bar: inert until used, additive migrations)
**Issues:** #2174

## Context

An operator of a shared install needs to know, per tenant, how much work ran:
attempts finished, by state, by size (`pool_slots`), and for how long. Today
the only source is `task_instances` and `task_instance_history`, read
directly. That ties external tooling to the internal schema, which changes
with migrations (051 dropped three tables), and it has no cursor: a reader
that polls by time misses rows written by a transaction that committed late,
and reads the same rows twice.

An attempt ends in many places. The code today has about twenty writes that
move a task instance into an attempt-ending state:

- the agent's report (`ReportTaskResult`) and its reschedule;
- the reapers: dispatch lost, start expired, agent lost, credential ceiling,
  pod lost, warm worker lost, and the run orphan sweep;
- the reconcile and infra-override paths of the executor;
- the scheduler's dispatch failures, upstream failures and skips, and the
  retry relabel (`failed` to `up_for_retry`);
- a user's mark (`success` or `failed`) and a clear (`removed`, and the
  reset that archives the attempt into `task_instance_history`).

Most are single statements. Several are not guarded on the previous state or
return no row count, so their caller cannot tell whether the write applied.
A user can mark the same try `success`, then `failed`, as often as they like.
An infra re-place keeps the try number and moves the attempt epoch, so one
try can have several executions.

## Decision

1. **The database writes the event.** A trigger on `task_instances`,
   `AFTER UPDATE OF state`, inserts one row into `usage_events` when the new
   state is attempt-ending (`success`, `failed`, `skipped`,
   `upstream_failed`, `removed`, `up_for_reschedule`) and differs from the
   old one. It runs in the transaction of the write that moved the state,
   whichever of the paths above it is, so an event exists if and only if the
   change committed. A new path cannot forget to emit. Task instances are
   created in `none` today (`CreateTaskInstance`, the COPY of
   `MaterializeTasks` and the rebind of a clear), so `UPDATE` covers every
   path; a test asserts that no insert path writes an attempt-ending state,
   so a future one fails loudly instead of under-counting.

2. **What an event holds.** A sequence `id`, the writing transaction's id
   (`xid8`, from `pg_current_xact_id()`), the tenant, the run id
   (`dag_run_id`; the DAG id is resolved when the row is served, so the
   trigger reads only the row it fires on), the task instance id, the task
   id, `map_index`, `try_number`, `attempt_epoch`, the revision, the new
   state, the previous state, `pool_slots`, `started_at`, `ended_at`,
   `last_failure_kind` (an infra failure is told apart from the task's own),
   and `recorded_at`. The duration is `ended_at` minus
   `started_at` when both are set; a task skipped or failed upstream has none.

3. **Identity and revisions.** An attempt is
   `(task_instance_id, try_number, attempt_epoch)`. Its first event has
   revision 0; a later change of the same attempt into another
   attempt-ending state (a user's mark over the agent's result, an infra mark
   overridden to success) has the next revision. `failed` to `up_for_retry`
   is not attempt-ending and writes nothing. The idempotency key a consumer
   dedupes on is the attempt plus the revision, and the latest revision is
   the attempt's state. The trigger finds the previous revision through an
   index on `(task_instance_id, try_number, attempt_epoch)`; the row lock the
   state change already holds on `task_instances` serializes two writers of
   the same attempt, so revisions do not collide.
   A reschedule bumps `attempt_epoch` (`RedispatchRescheduledTaskInstance`),
   so each sensor poke that ends in `up_for_reschedule` is its own attempt
   with revision 0. This differs from #2174, which asked for no event on a
   reschedule: a poke holds a slot and runs a pod, so it is usage. A consumer
   that bills by try groups on `(task_instance_id, try_number)`.

4. **Delivery is at least once, in id order.** The service API gains
   `GET /api/v2/service/usage?after=<id>&limit=<n>`, service token only. It
   returns events with an id above the cursor, in order, at most `limit`
   (default 500, at most 5000), and only those whose transaction is visible
   to every reader. A sequence value is taken at insert and the row becomes
   visible at commit, so ids commit out of order: a reader that saw id 11
   and moved its cursor past it would never see id 10 committing later, and
   `pg_snapshot_xmin` is a transaction id, not a sequence value, so it cannot
   bound ids. The route therefore serves only rows whose `xid` is below
   `pg_snapshot_xmin(pg_current_snapshot())`, so every transaction that
   wrote them has finished, in `(xid, id)` order, and the cursor is that
   pair, encoded as an opaque string. Every row written later has an xid at
   or above that horizon, so it sorts after the cursor. A consumer stores
   the last cursor it processed and dedupes on the key of point 3. A long
   write transaction anywhere in the database holds the horizon back: events
   are delayed, never lost, and `dexaflow_usage_events_lag_seconds` (age of
   the oldest unserved row) shows it.

5. **Retention.** The retention janitor deletes events older than
   `retention.usage_events_days`, default 30, so a consumer that is down for
   a while can catch up and the table does not grow without bound. A cursor
   older than the oldest kept row gets `410 Gone` with the oldest available
   cursor, so a consumer learns that it lost events instead of resuming past
   the gap silently.

6. **Per-tenant counters.** `dexaflow_task_attempts_total{tenant,state}` and
   `dexaflow_task_slot_seconds_total{tenant}` (size times duration) are
   advanced by the leader from the same table, after its own cursor, so they
   and the API never disagree. A leader that takes over starts its cursor at
   the current horizon, not at the oldest row, so a failover is a counter
   reset and never replays 30 days. Their cardinality is the number of
   tenants, which on a shared install is unbounded, so they are off by
   default and `observability.usage_metrics` turns them on.

7. **Off until enabled.** ADR 0068 section 2 point 2 asks a patch to be
   inert for a workload that does not use it, and an insert on every settle
   path is not inert. The trigger is declared with
   `WHEN (NEW.state IS DISTINCT FROM OLD.state AND NEW.state IN (...))`, so
   it is not even called for the other writes, and the function returns at
   once unless the one row of `usage_events_switch` says `enabled`. The
   leader writes that row at startup from `usage.events.enabled` (default
   `false`). A row read, not a session variable, because PgBouncer in
   transaction mode (a supported setup) drops custom startup parameters, and
   not DDL at run time, because creating or dropping a trigger locks
   `task_instances` (see Consequences). The leader's setting wins when replicas disagree,
   and the startup log says which value it wrote. With the switch off the
   service route answers `404`, so a consumer cannot mistake an install that
   records nothing for one with no usage.

## Alternatives considered

- **Emit from Go at each settle path.** Twenty paths, most single statements
  with no transaction, several unguarded or without a row count. Each would
  need a transaction or a CTE, and the next path added would need the same
  care; one forgotten path is silent under-counting.
- **An in-process `Sink` interface with typed events.** Simple to call, but an
  event emitted after commit is lost on a crash, one emitted before it can
  describe a change that rolled back, and it serves only the process that
  made the change, not the replicas that did not.
- **Logical replication or `LISTEN/NOTIFY`.** Replication needs a slot the
  operator must manage and that holds WAL while the consumer is down.
  `NOTIFY` is not durable.
- **A reader that polls `task_instances` by `ended_at`.** It misses late
  commits and re-reads rows, has no revision for a re-mark, and keeps
  tooling on the internal schema.

## Consequences

- A migration creates `usage_events`, its sequence, the trigger function and
  the trigger. The table is indexed by `id` (its primary key), by
  `(xid, id)` for the route, by the attempt key for revisions, and by
  `recorded_at` for retention. `CREATE TRIGGER` takes a `SHARE ROW EXCLUSIVE`
  lock on `task_instances`, so the migration sets `lock_timeout` as 051
  does: the statement is instant, but it waits behind every open
  transaction on the table, and writes queue behind the wait.
- With the setting on, every write that moves a task instance into an
  attempt-ending state does one more insert in its transaction. Bulk writes
  (an upstream failure cascade, a clear of a large run) fire the row trigger
  once per row; the settle path benchmark in `test/load` measures a single
  settle and a 1,000-row cascade, on and off, before the change is
  accepted, and a statement-level trigger with transition tables is the
  fallback if the row trigger misses the budget.
- The service API documents the route, the cursor rule and the key; the
  operate docs describe how to consume it.
- The trigger is the one piece of logic in the schema; the migration's
  comment and this record say so, so a reader of the Go code knows it is
  there.
