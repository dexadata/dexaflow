---
title: "ADR 0086: a clear on the latest version re-binds a finished run with its task set, and leaves an unfinished run on its own version"
linkTitle: "0086 · Clear re-binds a finished run with its task set"
weight: 860
description: "ADR 0086: run_on_latest_version moves a run to the DAG's current version only when the run has finished and none of its task instances is queued or running, as in Airflow 3.2. The same transaction creates a task instance for every task the version adds, marks removed the task instances of the tasks it dropped (a new terminal state) and brings back a removed one whose task returns. A run that has not finished keeps its version, the clear applies on it, and the response says the option was not applied and why. Replaces the refusal added by #1876."
---

**Status:** Accepted
**Date:** 2026-10-16
**Deciders:** Project owner (option A of #1894, Airflow parity)
**Relates:** ADR 0020 (clear and its version; this ADR adds to the 2026-05-24 and 2026-09-15 amendments and keeps them), ADR 0003 (DAGs are immutable artifacts), ADR 0051 (orchestration and execution state machines), ADR 0068 (patch content gated by safety)
**Issues:** #1894, #1702, #1876

## Context

A clear with `run_on_latest_version` re-binds the run to the DAG's current
version (ADR 0020). Until #1876 the re-bind kept only the task instances the
run had:

- a task the new version added got no task instance. The scheduler creates a
  run's task instances only when the run has none, and `FinalizeRun` waits for
  every task of the spec, so the run never finished;
- a task the new version dropped kept its task instance, with no task to run
  it;
- a run still running was re-bound too: its live attempts kept the old image
  while the agent RPCs read the new version's task spec.

#1876 refused the re-bind with `409` whenever the task sets differed. The
re-bind of a running run with the same tasks stayed as it was.

Airflow 3.2.1 (`clear_task_instances` in `airflow/models/taskinstance.py`)
re-binds a run only when it has finished (`dr.state in finished_dr_states`).
It then calls `DagRun.verify_integrity`, which creates the task instances of
added tasks, marks `removed` the task instances of dropped tasks, and sets a
`removed` task instance back to no state when its task is in the DAG again. A
queued or running run keeps its version. Airflow's "mark run failed" also ends
the run's unfinished task instances, so a finished Airflow run has no live
attempt.

## Decision

### D1. Only a finished run with no live attempt moves to the current version

A clear with `reset_dag_runs` and `run_on_latest_version` re-binds the run
only when both of these hold, read inside the clear's transaction with the run
row locked (`FOR NO KEY UPDATE`, and only while the run is finished, so an
unfinished run is never held):

- the run is `success` or `failed`;
- none of its task instances is `queued` or `running`.

The second condition is ours. Dexaflow's "mark run failed" or "mark run
success" leaves the run's task instances as they are, so a finished run can
still have an attempt in a pod. That attempt keeps its pinned version, which
is what #1702 asked for ("keep running attempts on their pinned version").

### D2. The re-bind brings the run's task instances to the new version's task set

In the same transaction as the re-bind:

1. **Added tasks:** every task of the current version without a task instance
   gets one, as materialization creates it: state `none`, try 1, and the
   retries, pool and size of that version. A finished run that has no task
   instances at all gets all of them here. The scheduler only materializes a
   run that has none, so it never creates them from the old version.
2. **Dropped tasks:** every task instance whose task the current version does
   not declare is set to `removed`. Its history, logs and XCom stay.
3. **Returning tasks:** a `removed` task instance whose task the current
   version declares again is reset to `none` as a new try, the same reset a
   clear applies, so it runs again in the re-bound run.

The clear's own resets then apply to the tasks of the current version only. A
named task the version dropped is not reset. It stays `removed`, also under a
later clear that keeps the run's version: every clear resets only tasks the
version the re-run executes declares, so a finished run never ends with a
task instance in `none` that nothing will plan. The dry run and the audit
log list only the task instances the clear resets.

### D3. `removed` is a terminal task state

Migration 047 adds `removed` to the `task_state` enum. The down migration does
nothing: Postgres cannot drop an enum value without rebuilding the type, and
`ADD VALUE IF NOT EXISTS` makes the up safe to apply again (as for 006 and
017). The previous release reads the value and never schedules it: the
scheduler plans and finalizes from the spec's tasks, never from a task
instance whose task the spec lacks. Its settled predicates do not list
`removed`, though, so after a rollback a run holding a removed task instance
is never deleted by retention and its pods are never collected, and its
orphan reaper counts `removed` as live. Upgrading again restores both. The
down file says so, and how to settle such runs before a rollback.

`removed` is terminal (`TaskState.IsTerminal`), as in Airflow, where it is a
finished state. It is never counted in the run's outcome, because
`FinalizeRun` reads only the tasks of the run's version. In the transition
table, a clear moves any task instance that is not queued or running to
`removed`, and `removed` back to `none`. The scheduler never writes it.

### D4. A run that has not finished keeps its version, and the response says so

A queued or running run, or a finished run with a live task instance, keeps
the version it is bound to. The clear still applies to it, on that version,
with the retry budget of that version. Airflow takes the latest task's retries
for such a run; here the attempt runs the pinned version's image, so its
budget comes from the same version.

The option is not silently dropped. Both clear routes (`clearTaskInstances`
and `dagRuns/{dag_run_id}/clear`), dry run included, add the field
`run_on_latest_version_not_applied` to their response. It lists each such run
with the reason, for example "the run is running". The reason is read before
the clear, and the clear decides again inside its transaction, so a run that
finishes in between can be re-bound although the response listed it, and a
run another clear re-opens in between keeps its version although the
response does not list it. To move
such a run to the current version: wait for it to finish, or mark it failed or
success, then clear again. The user docs explain this in
[Clearing a run that is still running](/author-dags/dag-authoring/#clearing-a-run-that-is-still-running).

### D5. The refusal of #1876 goes

A clear onto a version with other tasks no longer answers `409`. An unknown
run still answers `404` before anything is cleared. The `409` stays declared
in the OpenAPI document for compatibility, and no longer has a cause.

## Consequences

- "Fix the DAG, clear with run on latest version" works when the fix adds or
  drops a task: the added task runs, and the dropped one shows `removed`.
- A clear with the option on a run that is still running behaves as in
  Airflow: the run keeps its version. Before this ADR, Dexaflow re-bound such a
  run when the task sets matched. That behaviour changes, and the response says
  so.
- A live attempt is never moved to another version, so the agent RPCs of an
  attempt always read the version its image was built for.
- The vendored Airflow UI does not show `run_on_latest_version_not_applied`.
  After the clear, the run still shows the version it keeps.
- The re-bind writes more rows in one transaction: one task instance per added
  task, plus one update for the dropped and returning ones. A clear is a user
  action, not a scheduler tick, so this does not change the tick's cost. The
  algorithm review and a soak run are recorded for the release candidate that
  carries it (AGENTS.md, reviews 2 and 8).
