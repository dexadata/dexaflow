---
title: "ADR 0106: Leader writes are fenced by a lease term"
linkTitle: "0106 · Leader writes fenced by a lease term"
weight: 1060
description: "ADR 0106: every acquisition of scheduler leadership increments a term in a scheduler_lease row. The leader renews by term, tags its database sessions and its pods with it, and a new leader ends the sessions of older terms, so a deposed leader can no longer write or create pods after the new leader's first tick. The advisory lock stays as the acquisition mutex. Amends ADR 0009 and ADR 0031."
---

**Status:** Proposed
**Date:** 2026-11-06
**Amends:** ADR 0009 (leader election via advisory locks): the lock stays the way leadership is taken, but it no longer is the proof of leadership. ADR 0031 (scheduler architecture), section "Leader-overlap correctness": the overlap window is closed for database writes and pod creates instead of being tolerated.
**Relates:** ADR 0051 (attempt epoch), ADR 0052 (durable task outcome), issue [#2329](https://github.com/dexadata/dexaflow/issues/2329)

## Context

Leadership is `pg_try_advisory_lock` on one dedicated session
(`internal/scheduler/leader.go`), checked every 5 s by a watchdog
(`leadership.go`). The lock proves leadership for that one session only. The
tick, the dispatch workers, the reapers, the pod reconciler, the warm pool
reconciler and retention write through other pools. When the leader session
drops (a network blip, an idle reap by a proxy, a managed database failover),
Postgres releases the lock at once and a follower takes it within 5 s, while
the old leader keeps writing until its watchdog notices: up to three failed
checks, about 15 s, on a session that hangs instead of failing.

ADR 0031 argued that the overlap is harmless because writes are guarded:
enum transitions, `WHERE state = ...` predicates, and since ADR 0051 the
attempt epoch. Most are. These are not:

- **Pod creates from the old leader's dispatch buffer.** The buffer holds up
  to 512 items under the `cluster` profile. A worker that dequeued one before
  the step-down creates the pod, and a create carries no database guard.
- **Writes whose guard does not cover what the new leader changed.** A
  guarded `UPDATE` checks the row's state, not who decided the transition.
  An old leader acting on a tick snapshot can apply a transition the new
  leader has already made moot (a run it finalized, a TI it re-placed under
  a new epoch whose guard the old write happens to satisfy).
- **External side effects:** failure alerts, warm pool creates and deletes,
  `DeleteCollection` of a settled run's pods.

Two more facts make the session lock weak as a proof: a managed database
failover drops it for every replica at once, so two replicas can both win
the next attempt on different sides of a split, and it cannot work behind
PgBouncer in transaction mode, where a session is not a connection.

## Decision

1. **A lease row with a term.** A new table `scheduler_lease` holds one row
   (`id = 1`, `holder`, `term bigint`, `acquired_at`, `renewed_at`).
   Acquiring leadership is still `pg_try_advisory_lock`, the fast, instant
   mutex; right after it succeeds the winner runs
   `UPDATE scheduler_lease SET term = term + 1, holder = $me, acquired_at = now(), renewed_at = now() WHERE id = 1 RETURNING term`.
   That term is the leader's identity for the whole leadership.

2. **Renewal is by term.** The watchdog's check becomes
   `UPDATE scheduler_lease SET renewed_at = now() WHERE id = 1 AND term = $mine`.
   No row changed means another replica took a newer term, and the leader
   steps down at once (`term_superseded`), whatever its own session says.
   The advisory lock check stays alongside it.

3. **The database refuses writes from an older term.** Every connection of
   the leader's pools is tagged with its term when it is handed out (pgx
   `PrepareConn`): `SET dexaflow.leader_term = N` and an `application_name`
   of `dexaflow-leader/N`, only when the tag changed, so a steady leader pays
   nothing per query. A statement level `BEFORE INSERT OR UPDATE OR DELETE`
   trigger on the tables the leader writes (`dag_runs`, `task_instances`,
   `task_instance_history`, `task_state_history`, the warm pool and dispatch
   tables) raises `stale_leader_term` when the session carries a tag that
   is not the lease's current term. One indexed read of a one-row table per
   statement, and nothing at all for an untagged session: the API, the agent
   RPCs and the migration job are not tagged and are not fenced.

4. **A new leader ends the sessions of older terms.** Right after its term
   increment commits, the new leader runs `pg_terminate_backend` on every
   backend whose `application_name` is `dexaflow-leader/M` with `M` below its
   own term. A statement or a transaction of the old term that was in flight
   when the term moved is aborted instead of committing later, which is the
   window the trigger alone leaves (a statement that read the lease before
   the increment committed). The old leader's pool reconnects, its new
   connections still carry the old tag, and every write they try is refused
   by the trigger until its watchdog steps it down. The roles need nothing
   more: a role may signal backends of its own role.

5. **Pods carry the term.** Task pods and warm pods get the annotation
   `dexaflow.io/leader-term`. A dispatch worker drops an item whose term is
   not the current one before it calls the apiserver. The pod reconciler
   deletes a Pending pod created under an older term whose attempt has not
   started, so a create that raced the step-down cannot start a task the
   new leader re-placed. A pod whose attempt started is never touched: its
   attempt is the row's live execution (ADR 0052).

6. **External side effects check the term first.** Alert sends, warm pool
   creates and deletes and collection deletes check the leader's term
   against the last renewal right before the call. This narrows the window
   to one call; it does not close it, and the docs say so.

7. **The leader's writers use the leader's pools.** Fencing needs every
   leader-only writer on a tagged pool: the scheduler pool
   (`database.scheduler_max_conns`) for the tick, the dispatch workers, the
   reapers, the reconciler, the warm pool reconciler and retention. With
   fencing on, the scheduler pool is dedicated whether or not its size is
   set. The setting is `scheduler.lease_fencing`, off by default for one
   minor, on under the chart's `cluster` profile, and on by default in the
   minor after.

### Rolling upgrade

The migration adds the table and its row with `term = 0`; the triggers are
created in the same migration but do nothing for an untagged session, so an
older binary keeps working against the new schema (ADR 0090). A new binary
that wins the lock while an old one still writes increments the term; the
old binary does not tag its sessions, so it is not fenced, and the overlap
is as safe as it is today. Once no old binary runs, every leader is tagged.
One leader at a time is still the advisory lock's job, unchanged.

## Alternatives considered

- **A term predicate in every leader query** (`AND EXISTS (SELECT 1 FROM scheduler_lease WHERE term = $mine)`).
  It is what #2329 sketched. It fences exactly, but it means editing a few
  hundred generated queries and keeping every future one right by review,
  and it still leaves the in-flight statement window of point 4.
- **A time based lease without fencing**: the new leader waits the old
  lease's TTL (about 20 s) before its first write, and the old leader stops
  itself at its own deadline. No database cost, but it adds the TTL to every
  failover, depends on the old leader's clock and scheduling to stop in
  time, and does nothing for writes already in flight.
- **An external coordinator** (etcd, Kubernetes Lease objects with fencing
  tokens). Kubernetes Leases give a term but no fencing in Postgres, which
  is where the writes go; ADR 0009 rejected new infrastructure for the same
  reason.

## Consequences

- After the new leader's first tick, no database write and no pod create
  from an older term lands. The integration test of #2329 runs two
  schedulers, drops the leader's session, and checks both.
- The leader's database sessions are restarted on every failover. That is
  the point; it costs one reconnect per pool connection.
- A statement on a fenced table costs one extra single-row read for a tagged
  session. It is measured before the default flips (ADR 0062 review), on the
  10k and 100k TI profiles.
- PgBouncer in transaction mode is still not supported for the leader: the
  tag is a session setting. Supporting it means `SET LOCAL` in each leader
  transaction and a lease acquired by TTL instead of the advisory lock,
  which is left to a later ADR.
- The old leader's log shows refused writes (`stale_leader_term`) for up to
  its watchdog interval after a failover. They are counted as
  `leader_write_fenced` in `dexaflow_scheduler_decisions_total`; a steady
  rate outside a failover means two replicas believe they lead.
