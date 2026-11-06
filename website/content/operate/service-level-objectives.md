---
title: Service level objectives
linkTitle: Service level objectives
weight: 32
description: "What the production profile promises: each service level indicator, its objective, the query that measures it, the alerts on its error budget, and the release rule that holds a candidate to them."
---

The control plane exports the signals; this page states the bar they are held
to on the [production profile](/operate/production-profile/). The objectives
are starting values ([#2314](https://github.com/dexadata/dexaflow/issues/2314)):
they will be tuned after the long soak and the capacity test of 0.6.0, and a
change to one is a changelog entry.

Every objective is measured over a rolling 30 days. Its **error budget** is
what the objective leaves: 99% of scheduled runs on time allows 1% late, about
432 of 43,200 minutely runs a month.

## Objectives

| Indicator | Objective | Good event | Measured by |
|---|---|---|---|
| Schedule punctuality | 99% | A scheduled run created within 60 s of its slot | `dexaflow_scheduler_schedule_delay_seconds` |
| Task start | 99% | An attempt running within 120 s of `queued` | `dexaflow_task_queue_wait_seconds` |
| Platform-caused failures | under 0.1% of finished attempts | An attempt not failed as `agent_lost`, `pod_lost`, `dispatch_lost` or `warm_worker_lost` | `dexaflow_scheduler_decisions_total` over `dexaflow_task_attempts_total` |
| API availability | 99.9% | A request answered without a 5xx | `dexaflow_http_requests_total` |
| Leadership | under 15 minutes in 30 days without a leader | A minute in which a scheduler replica is up and a leader ticks | `dexaflow_scheduler_loop_duration_seconds_count` with `dexaflow_build_info` |
| Outcome durability | no successful task run again | A successful attempt that stays the attempt of record | the soak's `success_archived` check |

What each one leaves out, on purpose:

- **Schedule punctuality** counts the latest slot a tick created for a DAG.
  Catchup of a long pause or a backfill is not a schedule and is not counted.
- **Task start** counts the attempts that reached `running`. An attempt that
  never starts has no sample; the [DexaflowOldestQueuedTask](/operate/runbooks/oldest-queued-task/)
  alert covers it. It counts warm workers and dedicated pods alike, so an
  install that pulls a large image on a cold node spends budget on the pull.
- **Platform-caused failures** need `observability.usage_metrics` for the
  attempts count; without it the indicator has no denominator and is not
  measured. A re-placement that does not fail the attempt (a pod lost before
  it started, re-placed off the retry budget) is not counted.
- **API availability** counts every request the control plane served. A
  request that never reached it (an ingress outage) is not seen; measure that
  at the load balancer.
- **Outcome durability** has no metric: a success that is lost looks like a
  normal retry from outside. The soak monitor checks it in the database
  (`task_instance_history` holds no `success` row), and a release cannot ship
  with a violation.

## Queries

The chart records each indicator's error ratio over 5m, 30m, 1h and 6h when
`metrics.prometheusRule.enabled` is on (the `dexaflow-slo` group,
`metrics.prometheusRule.slo.enabled`, on by default):

| Series | Meaning |
|---|---|
| `dexaflow:slo_errors:ratio_rate<window>{slo}` | Share of bad events in the window; `slo` is `schedule_punctuality`, `task_start`, `platform_failures` or `api_availability` |
| `dexaflow:slo_objective{slo}` | The objective, from `metrics.prometheusRule.slo.objectives` |
| `dexaflow:scheduler_leaderless:bool` | 1 while a scheduler replica is up and no leader has ticked for 2 minutes |

The budget left over 30 days, per objective:

```promql
1 - avg_over_time(dexaflow:slo_errors:ratio_rate1h[30d])
    / on (namespace, slo) (1 - dexaflow:slo_objective)
```

Minutes without a leader in the last 30 days:

```promql
sum_over_time((dexaflow:scheduler_leaderless:bool == 1)[30d:1m])
```

The average of the hourly ratios weights each hour alike, so a quiet hour with
a bad minute counts as much as a busy one. It is close enough to steer by;
compute the exact ratio from the counters for a report.

## Alerts on the error budget

Two alerts in the `dexaflow-slo` group follow the multiwindow burn rate
practice: a short window confirms that a long window's burn is still going
on, so an alert stops soon after the cause does.

| Alert | Fires when | Means |
|---|---|---|
| [DexaflowErrorBudgetFastBurn](/operate/runbooks/error-budget-burn/) (critical) | the error ratio is above 14.4 times the budget's rate over 1 hour and over 5 minutes, for 2 minutes | 2% of the month's budget an hour: gone in two days |
| [DexaflowErrorBudgetSlowBurn](/operate/runbooks/error-budget-burn/) (warning) | above 6 times over 6 hours and over 30 minutes, for 15 minutes | 5% of the month's budget in 6 hours: gone in five days |

Leadership has no burn rate alert: [DexaflowNoSchedulerLeader](/operate/runbooks/no-scheduler-leader/)
pages after 5 minutes, a third of the month's budget. Set an objective tighter
or looser with

```yaml
metrics:
  prometheusRule:
    enabled: true
    slo:
      objectives:
        apiAvailability: 0.9995
```

## Release rule

A GA candidate whose soak or failure injection breaks an objective does not
ship. The [release checklist](https://github.com/dexadata/dexaflow/blob/main/test/release/perf-reliability-checklist.md)
row G5 measures it: scrape `/metrics` every 15 seconds through the long soak
and the failure injection rows, then

```sh
go run ./test/load/slo_check scrapes/*.prom
```

prints each objective, measured over the whole run, and exits 1 when one is
broken. An objective with no event in the run is reported as not measured,
never as met. The faults of the long soak are part of the run: an objective a
60 second database outage breaks over 12 hours is broken. Leadership and
outcome durability are judged by the soak monitor's `leader_churn` and
`success_archived` checks, which already fail the run.

A breach is fixed, or the objective is amended with the reason in this page
and the changelog, before the cut. It is never waived for one release.
