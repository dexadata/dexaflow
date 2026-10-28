---
title: "ADR 0093: an unavailable Kubernetes API is ridden out for a bounded time, off the dispatch-attempt budget"
linkTitle: "0093 · Unavailable API dispatch window"
weight: 930
description: "ADR 0093: a pod create that fails because the Kubernetes API is unavailable (a 503, a 504, a 500 such as a fail-closed admission webhook that is down, a connection refused or reset, the client deadline) no longer spends the six dispatch attempts of ADR 0031 Amendment A. The task is re-offered with a hold that grows with the outage, and fails as dispatch_failed only once the API has been unavailable to it for 15 minutes. The start of the outage is kept on the row. dispatch_failed is terminal and does not fall back onto the user's retries."
---

**Status:** Accepted
**Date:** 2026-10-16
**Relates:** ADR 0031 (scheduler architecture, Amendment A: dispatch-failure backoff), ADR 0051 (orchestration and execution state machines, the attempt epoch), ADR 0053 (admission and placement, cluster backpressure)
**Issues:** #1606

## Context

ADR 0031 Amendment A bounds a synchronous dispatch failure: the task records
a backed-off retry, and after six attempts (5, 10, 20, 40 and 80 seconds
apart, about 2m35s from the first to the last) it fails as `dispatch_failed`.
The amendment sized that for "a transient control-plane blip" and for a
permanent misconfiguration to surface in minutes. ADR 0053 later split cluster
backpressure off that budget: a quota 403 or an APF 429 is re-offered
forever.

Everything else stayed on the attempt budget, including the errors that say
the API could not take the request at all: a 503 while an apiserver restarts
or etcd changes leader, a 504 or a server timeout, a 500 from a fail-closed
admission webhook that cannot be reached (or a 502 from the load balancer in
front of a managed control plane), a connection refused or reset, and the
client deadline. A managed control plane upgrade lasts longer than 2m35s, so
every task due in that window failed as `dispatch_failed`, and a run failed
with it, although nothing was wrong with the tasks (#1606).

Two more facts shaped the decision:

- **A failed create can still have made a pod.** A reset connection or a
  deadline loses the response, not necessarily the request. The pod's name is
  random per create, so a second create makes a second pod.
- **`dispatch_failed` fell back onto the user's retries.** The two queries
  that wrote it left `max_tries` alone, so the planner read a failed task with
  retries left, moved it to `up_for_retry` and dispatched it again on a new
  try, with a fresh dispatch budget. That contradicts Amendment A ("a dispatch
  failure does NOT consume the task's `try_number`") and ADR 0051 (an infra
  fault never bills `retries`).

## Decision

1. **Unavailable is a disposition of its own.** The Kubernetes executor
   classifies a 503 (`ServiceUnavailable`), a 504 (`Timeout`), a 500
   `ServerTimeout` or `InternalError`, a connection refused or reset, a
   transport timeout and `context.DeadlineExceeded` as `Unavailable`. Only
   typed errors count; a message that merely mentions a refused connection is
   not one. The client rate limiter's own refusal stays `Deferred`, quota 403
   and 429 stay `Backpressure`, and a request the apiserver understood and
   refused (400, a 403 that is not quota, 404, 409, 422, a webhook that
   answered and denied) stays `Rejected` on the attempt budget. The Lite
   subprocess executor never returns `Unavailable`.

2. **It does not spend the dispatch-attempt budget, and it is bounded in
   time.** Both dispatch paths (the tick and the buffered workers) re-offer
   the task without incrementing `dispatch_attempts`. The task fails as
   `dispatch_failed` once the API has been unavailable to it for **15
   minutes**, measured from the first of its consecutive unavailable
   failures (a create the API answers ends the outage, see point 4), with
   the reason
   `dispatch_failed: Kubernetes API unavailable for <duration>: <cause>`.
   Fifteen minutes outlasts a managed control plane upgrade or an etcd leader
   change; an API that stays down longer is an outage an operator must see,
   and a poison condition that always answers 500 cannot hold a task forever.

3. **The hold grows with the outage.** Each re-offer waits as long as the
   task has already been riding the outage out, at least 5 seconds and at
   most 1 minute. That doubles the interval without a counter, so a
   recovering apiserver is not hit by every due task every few seconds, and a
   recovery is still noticed within a minute.

4. **The start of the outage lives on the row,** in the nullable column
   `task_instances.dispatch_unavailable_since`. The first unavailable failure
   of a dispatch episode stamps it and later ones keep it; it is reset with
   `dispatch_attempts` (a retry, an infra re-place, a clear) and on a
   reschedule re-dispatch. It is also cleared when the API answers a create
   with cluster backpressure or a rejection, on both paths, so the next
   unavailable failure starts a new window. A dispatch that never reached
   the API (the client rate limiter's refusal, a buffer wait past its bound)
   keeps it, and so does the write that marks the row `queued`, which on the
   buffered path lands before the create is sent. This follows the
   DB-as-truth rule of ADR 0031 and the reason Amendment A gave for its own
   columns. An in-memory map would restart the window on every restart or
   leader change. No existing column
   can carry the value: `scheduled_at` also covers a wait on cluster
   backpressure, which is unbounded, and `first_reschedule_at` belongs to
   sensors. The column is added without a default, so the migration only
   touches the catalog, and a previous release ignores it.

5. **An ambiguous create is safe to retry because the attempt is fenced.**
   The re-offer bumps the attempt epoch, as every requeue rail does, and the
   re-dispatch claims a new one (ADR 0051 amendment). A pod the lost create
   left behind carries the old epoch: its agent's RUNNING pre-flight report
   is refused with `should_terminate` before user code starts. If that pod
   wins the race and reports RUNNING before the re-offer is written, the
   re-offer is guarded to `scheduled` and `queued` and leaves the row alone,
   and no second dispatch can claim a running attempt. Only one of the two
   pods runs the task.

6. **`dispatch_failed` is terminal.** Failing a task as `dispatch_failed`,
   whether the attempt budget or the window ran out, on the tick or in a
   buffered worker, spends its retry budget the way a refused dispatch does
   (`max_tries` lowered to the current try). A clear restores it from the
   task.

This ADR changes the decision of ADR 0031 Amendment A for one class of
errors (it said every synchronous failure is bounded by attempts), so it is a
new decision rather than an edit of that amendment.

## Consequences

- A control plane outage shorter than 15 minutes no longer fails the tasks
  due during it; they are dispatched within a minute of the API coming back.
- A task can stay `scheduled` for up to about 16 minutes during an outage.
  The orphan-run reaper counts a scheduled task as live, so the run is not
  reaped meanwhile.
- A permanent misconfiguration that answers 500 surfaces after 15 minutes
  instead of 2m35s. One that answers 4xx surfaces as before.
- The step deadline and the client deadline now read as unavailability, not
  as a counted attempt, which is what they are.
- Migration 048 adds the column. It is additive and the previous patch
  ignores it; its down drops the column, and a task riding out an outage at
  rollback time falls back onto the attempt budget.
- A task that used `retries` to survive a long dispatch outage (the
  accidental fallback) now fails at the end of the window instead.

## Alternatives considered

- **Classify these errors as Backpressure (retry forever).** Rejected: a
  webhook that is broken for good, or an apiserver that never comes back,
  would hold every task `scheduled` with no signal.
- **Turn the whole attempt budget into a time window.** Rejected: a request
  the apiserver refused will be refused again, and should keep surfacing in
  minutes.
- **Count unavailable failures with a larger attempt bound.** It needs a
  second counter column anyway, and a count says less than a duration about
  how long an outage has lasted.
- **Keep the window in memory.** Rejected for the restart and failover reason
  above (point 4).
