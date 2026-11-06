---
title: "ADR 0104: Row level security binds each request's connection to its tenant"
linkTitle: "0104 · Row level security"
weight: 1040
description: "ADR 0104: Postgres row level security as a second tenant barrier. A migration creates one policy per tenant table, enforced by none until the operator runs dexaflow-migrate rls on. With database.row_level_security on, the main pool binds each connection an API request uses to the request's tenant, so a query that forgets its tenant predicate still sees only that tenant. Unbound connections, such as the scheduler's, pass every policy."
---

**Status:** Proposed
**Date:** 2026-11-06
**Relates:** ADR 0068 (patch content gated by safety), ADR 0090 (expand and contract migrations), ADR 0102 (patches from main)
**Issues:** #2180, #2315 (tenant isolation suite)

## Context

Tenant scoping is enforced in application code only. Every query in
`internal/storage/queries` filters by `tenant_id`, or by an id the handler
resolved inside the caller's tenant first, and the cross-tenant tests
(`tenant_predicates_*_integration_test.go`, #1799) prove each one. Nothing in
the database stops the next query that forgets it. With Dexaflow Cloud running
tenants that do not know each other on one database, one such query is a data
leak.

Postgres row level security filters rows by a policy whatever the SQL says.
The questions are how a connection learns whose request it serves, what the
paths that legitimately read across tenants (the scheduler, reapers, janitors,
sign-in before a tenant is known, the operator service API) do, and what it
costs.

## Decision

1. **Policies.** Migration 059 creates `dexaflow_bound_tenant()`, which reads
   the session setting `dexaflow.tenant_id` (NULL when unset), and one policy
   `dexaflow_tenant_barrier` on each of the sixteen tables with a `tenant_id`
   column:

   ```sql
   USING      ((SELECT dexaflow_bound_tenant()) IS NULL OR tenant_id = (SELECT dexaflow_bound_tenant()))
   WITH CHECK ((SELECT dexaflow_bound_tenant()) IS NULL OR tenant_id = (SELECT dexaflow_bound_tenant()))
   ```

   The sub-selects make Postgres read the setting once per statement, as an
   init plan, instead of once per row: called bare, the function made a bound
   read of 100 task instances about half again as slow.

   A bound connection sees, changes and inserts only its tenant's rows; an
   unbound one passes. Two tables keep rows without a tenant: a bound
   connection may write a global `audit_log` row but not read one, and may read
   a `cli_device_authorizations` row that has not picked its tenant yet (its
   codes protect it). Tables keyed only through a parent (`dag_versions`,
   `task_instance_history`, `xcom_store`, `user_roles`) get no policy: their
   rows are reached through a policed parent or by an unguessable id.

2. **Opt-in, in two steps.** The migration enables row level security on no
   table, so an upgrade changes nothing (ADR 0068, ADR 0090).
   `dexaflow-migrate rls on` enables and forces it on the sixteen tables in one
   transaction with a 5 second lock wait; forcing holds the table owner, which
   is usually the server's own role, to the policies too. `rls off` undoes it
   and `rls status` reports it. Then `database.row_level_security: true` makes
   the server bind its connections. Either step alone is harmless: tables that
   enforce see no bound connection, and a server that binds against tables
   that do not enforce only spends the binding.

3. **Binding.** The API's authentication middleware puts the request's tenant
   on the request context (`internal/tenantctx`). With the setting on, the main
   pool's `PrepareConn` hook makes each connection it hands out match its
   caller's context: it sets `dexaflow.tenant_id` to the tenant's id (resolved
   by name in the same statement; a name that does not exist binds the nil
   UUID, which matches nothing) or clears it for a caller with no tenant. The
   binding a connection holds is remembered on the connection, so a statement
   runs only when it changes, and a connection whose binding failed is
   destroyed. Transactions acquire their connection the same way, so they are
   bound too. Only the main pool binds; the leader, health and scheduler pools
   never do.

4. **Who is bound.** Every authenticated API request, for its own tenant. Not
   bound: the scheduler and its reapers and janitors, sign-in before a tenant is
   chosen, the operator service API, the agent gRPC server (a later slice can
   bind it to the attempt token's tenant), and anything run with
   `tenantctx.Without`. Unbound is the safe default for those because each is
   either cross-tenant by design or keyed by an id it already proved.

5. **Boot check.** With the setting on, boot fails when a tenant table does
   not enforce its policy, and when the server connects as a superuser or a
   `BYPASSRLS` role, which Postgres exempts from every policy. A barrier that
   only looks present is worse than none.

6. **Session state.** The binding is a session setting, so it needs a
   connection that keeps its session: a direct connection or PgBouncer in
   session mode. Transaction pooling is not supported with the setting on, the
   same limit `database.statement_timeout_ms` has.

## Consequences

- A handler that forgets its tenant predicate reads only its own tenant's rows,
  and a write into another tenant fails, on installs that turn it on. Without
  the setting nothing changes, and the policies cost nothing while the tables
  do not enforce them.
- The cost, measured with `BenchmarkRowSecurityReadsIntegration` on 200 running
  runs and 100,000 task instances, is in the Measurements section: under a
  tenth on most reads, up to a third on the task instance list. On top of the
  policies, a connection that changes tenant pays one short statement; one
  that keeps its tenant pays nothing for the binding.
- A superuser DSN, the default of the local compose setup, cannot use it. Lite
  keeps it off.
- The tenant isolation suite (#2315) gains a variant to run with it on.

## Measurements

Median of six runs of `BenchmarkRowSecurityReadsIntegration` (200 running
runs, 100,000 task instances, Postgres 16, 4 CPUs), per operation:

| Read | Before 059 | 059, tables off | Tables on, unbound | Tables on, bound |
|---|---|---|---|---|
| Scheduler active runs | 611 ms | 626 ms | 699 ms | 635 ms |
| UI list of task instances | 960 µs | 1,055 µs | 1,265 µs | 1,359 µs |
| UI list of DAG runs | 443 µs | 493 µs | 503 µs | 514 µs |

The first two columns run the same SQL, so their gap (up to 11%) is the noise
of the machine. Against "tables off", enforcing costs the UI's task instance
list about a fifth to a third and the other reads under a tenth. The
scheduler is unbound and does not need the policies; an install that wants
none of the cost keeps the tables off.

## Alternatives considered

- **A second database role for the scheduler** (the proposal in #2180). The
  scheduler would connect as a role with `BYPASSRLS` and the API as one
  without. It needs a second credential, a second pool for the monolith and
  `CREATEROLE` on managed databases that often refuse it, for the same
  property the unbound connection gives here.
- **A transaction per query with `SET LOCAL`.** Holds no session state, so it
  works with PgBouncer in transaction mode, but it adds a `BEGIN` and a
  `COMMIT` to every statement of every request.
- **Strict policies that hide everything from an unbound connection.** Every
  cross-tenant path would have to declare itself, and one that does not would
  fail in production instead of in review. The barrier this record buys is
  against an unscoped query on a request path, where the binding is automatic.
