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
| API availability | 99.9% | A request answered without a 5xx, the probes and `/metrics` left out | `dexaflow_http_requests_total` |
| Leadership | under 15 minutes in 30 days without a leader | Time in which a leader ticks | `dexaflow_scheduler_loop_duration_seconds_count` |
| Outcome durability | no successful task run again | A successful attempt that stays the attempt of record | the soak's `success_archived` check |

What each one leaves out, on purpose:

- **Schedule punctuality** counts the latest slot a tick created for a DAG.
  Catchup of a long pause or a backfill is not a schedule and is not counted.
  A tick that does not run records nothing, so a scheduler outage shows here
  only as one late run per DAG when it ends: leadership, not punctuality, is
  the objective an outage spends.
- **Task start** counts the attempts that reached `running`. An attempt that
  never starts has no sample; the [DexaflowOldestQueuedTask](/operate/runbooks/oldest-queued-task/)
  alert covers it. It counts warm workers and dedicated pods alike, so an
  install that pulls a large image on a cold node spends budget on the pull.
- **Platform-caused failures** need `observability.usage_metrics` for the
  attempts count; without it the indicator has no denominator and is not
  measured. A re-placement that does not fail the attempt (a pod lost before
  it started, re-placed off the retry budget) is not counted.
- **API availability** counts the requests the control plane served, without
  `/healthz`, `/readyz` and `/metrics`: a kubelet probe that gets a 503 while
  a dependency is down is not a caller's error, and on a quiet install the
  probes would outnumber the callers. A request that never reached the
  control plane (an ingress outage) is not seen; measure that at the load
  balancer.
- **Leadership** is counted from the second minute of a gap: the recorded
  series needs two minutes without a leader tick to tell a gap from a slow
  scrape, and a failover within that is how the lease is meant to work. With
  no scheduler scraped at all (every pod down, or Prometheus cannot reach
  them) it counts as no leader.
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
| `dexaflow:slo_events:rate1h{slo}`, `rate6h` | Events per second in the window, for the alerts' minimum |
| `dexaflow:slo_objective{slo}` | The objective, from `metrics.prometheusRule.slo.objectives` |
| `dexaflow:scheduler_leaderless:bool` | 1 while no leader has ticked for 2 minutes |

The budget spent over 30 days comes from the counters, which needs 30 days of
retention (kube-prometheus-stack keeps 10 by default; use a shorter window and
scale, or a long-term store). For API availability:

```promql
(
  sum(increase(dexaflow_http_requests_total{status=~"5..",path!~"/healthz|/readyz|/metrics"}[30d]))
  / sum(increase(dexaflow_http_requests_total{path!~"/healthz|/readyz|/metrics"}[30d]))
) / (1 - 0.999)
```

1 means the whole budget is spent. Add `namespace="<release namespace>"` to
every selector when one Prometheus scrapes more than one release. The others
follow the same shape: the late
share is `1 - sum(increase(<histogram>_bucket{le=~"60(\\.0)?"}[30d])) / sum(increase(<histogram>_count[30d]))`
for schedule punctuality (`le` 120 for task start), and lost attempts over
`dexaflow_task_attempts_total` for platform failures. Minutes without a leader
in the last 30 days:

```promql
sum_over_time((dexaflow:scheduler_leaderless:bool == 1)[30d:1m])
```

## Alerts on the error budget

Two alerts in the `dexaflow-slo` group follow the multiwindow burn rate
practice: a short window confirms that a long window's burn is still going
on, so an alert stops soon after the cause does.

| Alert | Fires when | Means |
|---|---|---|
| [DexaflowErrorBudgetFastBurn](/operate/runbooks/error-budget-burn/) (critical) | the error ratio is above 14.4 times the budget's rate over 1 hour and over 5 minutes, for 2 minutes, with at least 100 events in the hour | 2% of the month's budget an hour: gone in two days |
| [DexaflowErrorBudgetSlowBurn](/operate/runbooks/error-budget-burn/) (warning) | above 6 times over 6 hours and over 30 minutes, for 15 minutes, with at least 100 events in the 6 hours | 5% of the month's budget in 6 hours: gone in five days |

The minimum (`metrics.prometheusRule.slo.minEventsPerWindow`, 100) keeps a
single bad event on sparse traffic from paging: an install that finishes 60
attempts an hour would otherwise page on one lost pod. Below it, the budget
still counts the event; only the alert waits, so an install that never reaches
100 events in an hour (one that only runs a few schedules) gets no fast burn
alert for that objective and relies on the slow one and the 30 day query. A third tier (a 1x burn over 3
days, as a ticket) is left to the 30 day budget query above.

Leadership has no burn rate alert: [DexaflowNoSchedulerLeader](/operate/runbooks/no-scheduler-leader/)
pages after about 10 minutes without a leader (no tick for 5 minutes, held 5
more), two thirds of the month's budget. Several shorter gaps that add up show
only in the 30 day query. Set an objective tighter or looser with

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
row G5 measures it: scrape `/metrics` of every control plane process every 15
seconds through the long soak and the failure injection rows, each process
into a directory of its own, then

```sh
go run ./test/load/slo_check scrapes/*/
```

prints each objective, measured over the whole run, and exits 1 when one is
broken. An objective with no event in the run is reported as not measured,
never as met. Leadership is the run's total time without a leader, across the
processes, against the month's 15 minutes. The faults are part of the run: an
objective a 60 second database outage breaks over 12 hours is broken. Outcome
durability is judged by the soak monitor's `success_archived` check, which
already fails the run.

A breach is fixed, or the objective is amended with the reason in this page
and the changelog, before the cut. It is never waived for one release.
