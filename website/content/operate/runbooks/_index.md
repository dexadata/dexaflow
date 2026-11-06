---
title: Alerts and runbooks
linkTitle: Runbooks
weight: 75
description: The alerts the Helm chart ships, what each one means, and what to do when it fires.
---

The chart renders a `PrometheusRule` with two groups when
`metrics.prometheusRule.enabled` is `true`. It needs the Prometheus Operator
CRDs (kube-prometheus-stack) and a scrape of `/metrics`
(`metrics.serviceMonitor.enabled`).

- **page** (`severity: critical`, one warning case): the control plane cannot
  schedule, settle or serve right now. Route it to whoever is on call.
- **ticket** (`severity: warning`): something degrades and needs a look during
  working hours.

Every rule is scoped to the release namespace and carries a `runbook_url`
annotation that points at its page below. Set
`metrics.prometheusRule.runbookBaseURL` when the people on call read a mirror
of these docs. Thresholds are values under `metrics.prometheusRule.thresholds`;
the defaults are in the table.

```yaml
metrics:
  serviceMonitor:
    enabled: true
  prometheusRule:
    enabled: true
    additionalLabels:
      release: kube-prometheus-stack   # match your Prometheus ruleSelector
    thresholds:
      queueWaitP99Seconds: 300
```

## Page

| Alert | Fires when | Default |
|---|---|---|
| [DexaflowSchemaNotReady](schema-not-ready/) | `dexaflow_schema_ready == 0` for 5 minutes (critical only for a replaced database) | |
| [DexaflowNoSchedulerLeader](no-scheduler-leader/) | a scheduler replica is up and no leader tick ran for 5 minutes, for 5 minutes | |
| [DexaflowReaperSettlingValveOpen](reaper-settling-valve-open/) | any `reap_settling_valve_open` decision in 10 minutes | |
| [DexaflowInfraConfirmValveOpen](infra-confirm-valve-open/) | any `infra_confirm_valve_open` decision in 10 minutes | |
| [DexaflowTaskQueueWaitHigh](task-queue-wait-high/) | p99 of `dexaflow_task_queue_wait_seconds` of a tenant over the threshold for 15 minutes | `queueWaitP99Seconds: 300` |
| [DexaflowAPIErrorRatioHigh](api-error-ratio-high/) | share of 5xx answers over the threshold for 10 minutes | `apiErrorRatio: 0.01` |
| [DexaflowDatabasePoolCanceledAcquires](database-pool-canceled-acquires/) | canceled connection acquires on a pool every 5 minutes for 10 minutes | |
| [DexaflowRedisCommandFailures](redis-command-failures/) | Redis command failures (client cancellations excluded) every 5 minutes for 10 minutes | |
| [DexaflowRedisEvictingKeys](redis-evicting-keys/) | Redis `evicted_keys` grew in the last 10 minutes | |
| [DexaflowResilienceBackstopDisabled](resilience-backstop-disabled/) | a resilience backstop is disabled, for 5 minutes | |
| [DexaflowAgentLostReapingHeld](agent-lost-reaping-held/) | any `agent_lost_mass_silence_hold` or `agent_lost_mass_silence_valve_open` decision in 10 minutes | |

## Ticket

| Alert | Fires when | Default |
|---|---|---|
| [DexaflowRedisEvictionPolicyUnsafe](redis-eviction-policy-unsafe/) | Redis runs a `maxmemory-policy` other than `noeviction`, for 15 minutes | |
| [DexaflowSchedulerStepDowns](scheduler-step-downs/) | leader step-downs in one hour over the threshold, shutdowns excluded | `stepDownsPerHour: 3` |
| [DexaflowTasksLost](tasks-lost/) | attempts failed as `agent_lost`, `pod_lost`, `dispatch_lost` or `warm_worker_lost` in one hour over the threshold | `lostTasksPerHour: 10` |
| [DexaflowTLSReloadFailed](tls-reload-failed/) | a rotated certificate failed to load in the last 15 minutes | |
| [DexaflowAlertDispatchFailing](alert-dispatch-failing/) | an on-failure alert failed to send in the last 30 minutes | |
| [DexaflowUsageEventsLag](usage-events-lag/) | usage events lag over the threshold for 15 minutes | `usageEventsLagSeconds: 900` |
| [DexaflowOldestQueuedTask](oldest-queued-task/) | the oldest queued attempt of a tenant older than the threshold, for 5 minutes | `oldestQueuedSeconds: 600` |
| [DexaflowMixedVersions](mixed-versions/) | processes report more than one version for longer than the threshold | `mixedVersionsFor: 30m` |

## What is not covered yet

The retention janitor exports no error counter, so a failing retention cycle
has no alert; its log line (`retention`) is the signal for now.

The rules are tested: `scripts/prometheus-rules-test.sh` renders them and runs
`promtool test rules` against `helm/dexaflow/tests/promtool/`, with a case that
fires and one that stays quiet for every alert. Change a rule and its test
together.

See [Metrics](/reference/metrics/) for every family the rules read.
