---
title: "ADR 0096: every finished task attempt writes a usage event in the same transaction, read through the service API"
linkTitle: "0096 · Task attempt usage events"
weight: 960
description: "ADR 0096: a trigger on task_instances writes one row to a usage_events table each time an attempt reaches an attempt-ending state, in the transaction that moves it there, with the tenant, the task, the try, the attempt epoch, the state, the size and the times. An operator reads the table in order through a service API route with a cursor, and the retention janitor prunes it. Per-tenant counters are derived from the same rows. Events are delivered at least once and keyed by attempt and revision."
---

**Status:** Proposed
**Date:** 2026-10-26
**Relates:** ADR 0051 (orchestration and execution state machines, the attempt epoch), ADR 0066 (weighted pool slots, a task's size), ADR 0068 (new settings default to off)
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
   change committed. A new path cannot forget to emit.

2. **What an event holds.** A monotonic `id` (a sequence), the tenant, the
   DAG id, the run id, the task id, `map_index`, `try_number`, `attempt_epoch`, the new
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
   the attempt's state.

4. **Delivery is at least once, in id order.** The service API gains
   `GET /api/v2/service/usage?after=<id>&limit=<n>`, service token only. It
   returns events with an id above the cursor, in order, at most `limit`
   (default 500, at most 5000), and only those whose transaction is visible
   to every reader: it stops below the oldest id still in flight
   (`pg_snapshot_xmin`), so a slow transaction never makes the reader skip
   an id. A consumer stores the last id it processed and dedupes on the key
   of point 3.

5. **Retention.** The retention janitor deletes events older than
   `retention.usage_events_days`, default 30, so a consumer that is down for
   a while can catch up and the table does not grow without bound.

6. **Per-tenant counters.** `dexaflow_task_attempts_total{tenant,state}` and
   `dexaflow_task_slot_seconds_total{tenant}` (size times duration) are
   advanced by the leader from the same table, after its own cursor, so they
   and the API never disagree. Their cardinality is bounded by the number of
   tenants. `observability.usage_metrics` turns them off for an install with
   many tenants.

7. **The trigger is always on.** It costs one insert per finished attempt,
   the same order as the `task_instance_history` row a retry or clear already
   writes. A setting that turns the trigger off would need either DDL at run
   time or a lookup in every trigger call, and an install that does not read
   the table pays only the retention delete.

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
  the trigger. The table is indexed by `id` (its primary key) and by
  `recorded_at` for retention.
- Every write that moves a task instance into an attempt-ending state does
  one more insert in its transaction. The settle path benchmark in
  `test/load` measures it before the change is accepted.
- The service API documents the route, the cursor rule and the key; the
  operate docs describe how to consume it.
- The trigger is the one piece of logic in the schema; the migration's
  comment and this record say so, so a reader of the Go code knows it is
  there.
