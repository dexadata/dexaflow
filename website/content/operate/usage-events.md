---
title: Read usage per tenant
linkTitle: Usage events
weight: 57
description: Record one event per finished task attempt, read them in order through the operator service API, and export per-tenant counters.
---

An operator of a shared install can know, per tenant, how much work ran:
attempts finished, by state, by size and for how long. Dexaflow records it
as usage events ([ADR 0096](/project/adrs/0096-task-attempt-usage-events/)),
written by the database in the same transaction as each change that finishes
a task attempt, so an event exists if and only if that change committed.

## Turn it on

```yaml
usage:
  events:
    enabled: true          # DEXAFLOW_USAGE_EVENTS_ENABLED
retention:
  usage_events_days: 30    # the default
auth:
  service_token: ...       # turns on the operator service API
observability:
  usage_metrics: false     # per-tenant counters, see below
```

The scheduler leader writes `usage.events.enabled` to the database within 10
seconds of taking the lead, and logs `usage events switch written` with the
value. Recording is then on for every replica, older releases included,
because the database writes the events. Turning it off stops recording and
the route answers `404`; the events already recorded stay, and retention
deletes them only while the janitor runs for another class.

## What an event is

An event is written each time a task instance reaches `success`, `failed`,
`skipped`, `upstream_failed`, `removed` or `up_for_reschedule` from another
state. A retry relabel (`failed` to `up_for_retry`) writes nothing.

An attempt is `task_instance_id`, `try_number` and `attempt_epoch`. Its first
event has `revision` 0. A user who marks the task `success` or `failed` after
it ended writes revision 1, and so on; the highest revision is the attempt's
state. A task the platform re-places after an infrastructure fault keeps its
`try_number` and gets a new `attempt_epoch`, so each execution is its own
attempt, with `last_failure_kind: infra` on the one the platform failed. Each
poke of a sensor in reschedule mode is its own attempt too: it held a slot
and ran a pod. To bill per try, group on `task_instance_id` and `try_number`.

The size is `pool_slots` (the task's `size` in `dexaflow.yaml`). The run time
is `ended_at` minus `started_at` when both are set; a task skipped or failed
upstream never started.

## Read them

`GET /api/v2/service/usage` with the service token returns pages of events
in order; the request, the answer and the refusals are in the
[operator service API](/reference/configuration/#operator-service-api)
reference. A consumer loop:

```sh
cursor=$(cat cursor 2>/dev/null)
while :; do
  page=$(curl -fsS -H "Authorization: Bearer $DEXAFLOW_SERVICE_TOKEN" \
    "https://dexaflow.example.com/api/v2/service/usage?limit=1000&after=$cursor")
  echo "$page" | jq -c '.events[]' >> events.jsonl   # dedupe downstream on the key
  cursor=$(echo "$page" | jq -r .next_cursor)
  echo "$cursor" > cursor
  [ "$(echo "$page" | jq .has_more)" = true ] || break
done
```

Store the cursor only after the events of the page are stored. Delivery is at
least once: a consumer that crashes between the two reads the page again, so
dedupe on `task_instance_id`, `try_number`, `attempt_epoch` and `revision`.

An event is served only once every transaction older than it has finished,
so a later page never brings an event that sorts before a cursor already
returned. A long transaction anywhere in the database holds events back,
never loses them; `dexaflow_usage_events_lag_seconds` is the age of the
oldest event held back. Alert on it staying above a few minutes, and look for
the open transaction in `pg_stat_activity`.

A consumer that stays down longer than `retention.usage_events_days` gets
`410` with `oldest_cursor`: the events between its cursor and that one were
deleted. It resumes from `oldest_cursor`, and the gap is known.

## Per-tenant counters

With `observability.usage_metrics` on, the scheduler leader also exports
`dexaflow_task_attempts_total{tenant,state}` and
`dexaflow_task_slot_seconds_total{tenant}` from the same events, so they and
the API read the same records. Each attempt counts once, by the state it first ended
in; a later mark is in the API only. A leader starts counting when it takes
the lead, again on each new term, and does not replay what is kept, so a
failover is a counter reset,
which `rate()` and `increase()` handle. Each tenant is a series of its own,
which is why they are off by default.

## Cost

With recording off, the trigger is not even called for writes into other
states and returns after one read for these. With it on, a single settle
costs about the same as with it off, within the noise of the run, and an
upstream failure cascade about 20 microseconds per task instance; the
measurement is
[Load Experiment 8](https://github.com/dexadata/dexaflow/blob/main/test/load/BASELINE.md#usage-event-trigger).
