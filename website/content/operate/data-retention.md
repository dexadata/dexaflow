---
title: Data retention
linkTitle: Data retention
weight: 84
description: What the retention janitor deletes, why runs and the audit log are kept by default, how to turn it on safely with a dry run, and how to see what it removed.
---

Dexaflow keeps every finished run and every audit entry until you tell it
otherwise. On a busy install the `dag_runs`, `task_instances` and `audit_log`
tables then grow without bound, and every query that reads them grows with
them. The retention janitor deletes those rows once they are older than a
window you choose, in small batches, on the scheduler leader only.

## What each class deletes

| Setting | Default | Deletes |
|---|---|---|
| `retention.dag_runs_days` | `0`, off | Settled runs that ended more than this many days ago, with their task instances, attempt history and XCom index rows. |
| `retention.audit_log_days` | `0`, off | Audit log entries older than this many days, in every tenant and for the entries that belong to no tenant. |
| `retention.usage_events_days` | `30` | Usage events ([ADR 0096](/project/adrs/0096-task-attempt-usage-events/)) recorded more than this many days ago. Applies only while `usage.events.enabled` is on. |

A run is deleted only when all of these hold:

- it settled (`success` or `failed`) more than `dag_runs_days` ago;
- every one of its task instances is settled (`success`, `failed`,
  `skipped` or `upstream_failed`); a task in `none` is waiting to be
  scheduled, for example after a clear, so its run is kept;
- no staging volume of it is still live;
- it is not the newest run of its DAG.

The newest run of each DAG (by logical date) is always kept, however old,
because the scheduler reads the DAG's last logical date from it. Without it a
scheduled DAG would run its latest slot again, or its whole history with
`catchup`. Running, queued and recent runs are never touched.

A run goes with all its task instances or not at all, so a clear that lands
between two batches never leaves a run with some of its tasks missing.

### What it does not delete

- **Task logs.** The `disk` log backend prunes them after 30 days on its own;
  an object store keeps them by its bucket lifecycle policy.
- The bookkeeping rows of staging volumes already marked deleted.

## Why every class ships off

Deleting history cannot be undone, and how long you need it depends on you:
an audit trail kept for a compliance rule, run history someone reports on, a
backfill that compares against last year. So a default install never deletes
a row, and the server does not start the janitor at all until a class is set.
The one exception is usage events, which are only recorded once you turn them
on, and whose 30 day window exists so a consumer that was down can catch up.

## Turn it on

The settings are server environment variables, `DEXAFLOW_RETENTION_*`, on
Lite and on Cluster. The Helm chart has no values for them; set them through
`extraEnv`:

```yaml
extraEnv:
  - name: DEXAFLOW_RETENTION_DAG_RUNS_DAYS
    value: "90"
  - name: DEXAFLOW_RETENTION_AUDIT_LOG_DAYS
    value: "365"
  # First rollout: count only, delete nothing.
  - name: DEXAFLOW_RETENTION_DRY_RUN
    value: "true"
```

Then:

1. **Start with a dry run.** With `retention.dry_run: true` a cycle only
   counts the rows it would delete, summed across all tenants, logs the counts
   (`retention dry run: rows eligible for deletion`) and sets the
   `dexaflow_retention_rows_eligible{table}` gauge. To keep its own cost
   bounded it counts at most 10000 runs and 10000 audit rows per tenant; the
   log says `lower_bound=true` when a tenant reached that cap.
2. **Check the numbers** against what you expect to lose. A first cycle on a
   large install that has never pruned can find millions of rows; that is
   fine, the pacing below spreads them over many cycles.
3. **Turn the dry run off.** Remove `DEXAFLOW_RETENTION_DRY_RUN` and roll the
   control plane. The first deleting cycle resets the eligible gauge to zero.

A server refuses to start with a negative window, or with pacing that would
make the janitor unbounded or a busy loop (a `batch_size` outside 1 to 50000,
a negative `batch_pause`, a zero `interval` or `max_rows_per_cycle`).

## How it runs

- **Leader only.** The janitor runs on the scheduler leader and checks
  leadership again before every batch, so in an HA or split install exactly
  one replica deletes, and a replica that loses leadership stops at its next
  batch.
- **Every `retention.interval`** (default `1h`). The first cycle comes one
  interval after the server starts.
- **Small batches.** Each batch is one short transaction of at most
  `batch_size` rows (default 1000), run rows included, and at most 100 runs.
  The only batch larger than that is the task instances of a single run with
  more tasks than `batch_size`, which go together.
- **Paced.** The janitor sleeps `batch_pause` (default `100ms`) between
  batches, and a cycle starts no new batch once it deleted
  `max_rows_per_cycle` rows (default 100000); the rest waits for the next
  cycle.
- **Fair across tenants.** Each tenant's runs and each tenant's audit rows
  take turns, one batch each, so no tenant and neither class waits behind
  another's backlog.

A cycle that fails, for example on a database error, logs it and is retried
at the next interval.

## See what it deleted

- **Logs.** A deleting cycle that removed rows logs `retention cycle` with the
  rows per table and `rows_deleted`. At boot the server logs
  `retention janitor enabled` with the windows and pacing it runs with.
- **Metrics.**

  | Metric | Meaning |
  |---|---|
  | `dexaflow_retention_rows_deleted_total{table}` | Rows deleted, by table (`dag_runs`, `task_instances`, `task_instance_history`, `xcom_index`, `audit_log`, `usage_events`). |
  | `dexaflow_retention_rows_eligible{table}` | What the last dry run found, by table. |
  | `dexaflow_retention_cycle_duration_seconds` | How long one cycle took, pauses included. |

- **The audit log.** Every cycle that deletes audit entries writes a
  `retention.purge` entry in the same scope with the number of rows and the
  cutoff, so the purge itself stays on record. See
  [Audit log](/operate/audit-log/#how-long-it-is-kept).

## A starting configuration

For a production install with no rule of its own:

| Setting | Value | Why |
|---|---|---|
| `retention.dag_runs_days` | `90` | A quarter of history in the UI and the API, which covers most reruns and comparisons. |
| `retention.audit_log_days` | `365` | A year of who changed what. Export it first if a rule asks for longer. |
| `retention.dry_run` | `true` for the first rollout | See what would go before anything does. |
| pacing | defaults | 1000 rows a batch, 100ms apart, 100000 rows an hour at most. |

If a backlog takes too many cycles to clear, raise `max_rows_per_cycle`
before `batch_size`: more short transactions are kinder to the database than
fewer long ones.

Every setting, with its environment variable, is in the
[configuration reference](/reference/configuration/#retention-retention).
