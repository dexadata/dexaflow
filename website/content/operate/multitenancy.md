---
title: Multitenancy and tenant limits
linkTitle: Multitenancy
weight: 93
description: What a tenant is, how a request is bound to one, what is kept apart and what is shared, and the limits an operator sets per tenant.
---

One Dexaflow control plane can serve several **tenants**. A tenant is a named
space with its own DAGs, runs, users and settings, and nothing one tenant does
is visible to another through the API or the UI. Every install has the tenant
`default`, created by the first migration; a single-team install never needs
another.

## Creating tenants

Tenants are created by the platform operator through the
[operator service API](/reference/configuration/#operator-service-api), which
is off until `auth.service_token` is set:

```sh
curl -X PUT "$DEXAFLOW_URL/api/v2/service/tenants/acme" \
  -H "Authorization: Bearer $SERVICE_TOKEN" -H 'Content-Type: application/json' \
  -d '{"display_name": "Acme Corp", "default_pool_slots": 16, "max_dags": 50}'
```

A tenant name is 1 to 63 lowercase letters, digits or `-`. The call is
idempotent: it creates the tenant with the same built-in roles and default
pool as `default`, and a later call changes only the fields it carries. The
same API links users to a trusted issuer, sets the tenant's limits and
suspends it. Tenant users and roles are managed inside the tenant as usual
([Users, roles and API tokens](/operate/users-roles-tokens/)).

## How a request is bound to a tenant

The tenant comes from the credential, never from the URL or a header the
client chooses:

- **Session and API tokens** carry the tenant of the user they were issued
  to. An API token stops working if its owner is moved to another tenant.
- **A password sign-in** names its tenant in the request (`tenant`, `default`
  when left out), and the account must exist in that tenant.
- **OIDC** pins the tenant from a verified claim of the identity provider's
  token: `auth.oidc.tenant_claim` names the claim (`tid` for Entra, `hd` for
  Google Workspace) and `auth.oidc.tenant_claims` maps its values to tenants.
  A value that is not in the map is refused; the sign-in never falls back to
  `default`.
- **A trusted issuer** names the tenant in its own claim
  (`auth.trusted_issuer.tenant_claim`), limited to
  `auth.trusted_issuer.allowed_tenants`. With `host_pattern`, a session cookie
  works only on its tenant's host, for platforms that give each tenant its own
  hostname ([Binding sessions to tenant hosts](/reference/configuration/#binding-sessions-to-tenant-hosts)).

Tenant resolution fails closed: a credential that resolves to no tenant is not
served from `default`, it finds nothing.

## What is kept apart

Each tenant has its own:

- DAGs, DAG versions, runs, task instances and XComs;
- connections and variables, which tasks receive only from their own tenant;
- pools, including its own `default_pool`;
- users, role grants and API tokens;
- audit log.

Names are per tenant: two tenants can both have a DAG `nightly` or a
connection `warehouse`. An id from another tenant answers `404`, as if it did
not exist.

## Row level security

Every query the control plane makes carries its tenant, and the cross-tenant
tests prove each one. Row level security adds a second barrier in Postgres
itself, for a query that a future change forgets to scope
([ADR 0104](/project/adrs/0104-row-level-security/),
[#2180](https://github.com/dexadata/dexaflow/issues/2180)). It is off by
default.

With it on, each database connection that serves an API request of one tenant
is bound to that tenant, and Postgres hides every other tenant's rows from it,
whatever the SQL says: a read finds nothing, an update changes nothing, and an
insert into another tenant is refused. Connections that serve no single tenant
(the scheduler, the reapers and janitors, sign-in before a tenant is known, the
operator service API) are not bound and see every row, as without it.

Turn it on in two steps, after upgrading:

1. Turn the policies on, with the migrate binary of your release and a role
   that may alter the tables:

   ```sh
   dexaflow-migrate -database "$DATABASE_URL" rls on
   ```

   It enables and forces row level security on the sixteen tables with a
   `tenant_id` column, in one transaction that waits at most 5 seconds for
   each table's lock, and prints each table with the verdict
   `row level security: on`. `rls status` prints the same without changing
   anything. Until the server binds its connections, nothing changes: an
   unbound connection passes every policy.
2. Set `database.row_level_security: true` (`DEXAFLOW_DATABASE_ROW_LEVEL_SECURITY=true`,
   or `extraEnv` in Helm) and restart the control plane.

The server refuses to boot with the setting on when a tenant table is not
enforcing its policy, and when it connects as a superuser or a role with
`BYPASSRLS`: Postgres exempts those from every policy, so the barrier would only
look present. The server's role may own the tables; `rls on` forces the
policies on the owner too.

To turn it off, set the value back to `false` first, then run `rls off`. The
order does not matter for safety: a server that binds while the tables do not
enforce only spends the binding, and tables that enforce while the server does
not bind see no bound connection.

What it costs is measured in ADR 0104: a bound connection pays one short
statement when it changes tenant, and every read of a tenant table evaluates
the policy. It needs session state, so it does not work behind PgBouncer in
transaction pooling mode.

## What is shared

Tenants share one control plane, one database, one scheduler and one executor.
On Kubernetes, every tenant's task pods run in the same task namespace, on the
same nodes, with the task service account unless a DAG sets
`execution.service_account`. Pods carry the `leoflow.io/tenant-id` label, so you can watch or
limit them per tenant with your own tooling. Cluster-wide settings apply to
every tenant: a `taskSecret` mounted into task pods is mounted for all of them,
and the resource defaults apply to all of them.

The control plane keeps tenants apart; the cluster does not. Tenants that must
not share nodes or a network belong on separate installs. Within one cluster,
the [placement allowlist](/operate/placement-allowlist/) limits which node
selectors, tolerations and service accounts DAG authors may use. Within one
install, turn on `auth.secret_scoping: enforce` with
`auth.secret_liveness_mode: enforce`, so a task receives only the secrets its
DAG declared and only while its attempt runs
([ADR 0055](/project/adrs/0055-secret-scoping-and-token-liveness/)).

## Per-tenant limits

The operator sets these through the service API; `0` or unset means no limit:

| Limit | What it bounds |
|---|---|
| `max_dags` | DAGs the tenant may have. New versions of existing DAGs are always accepted. |
| `max_runs_per_day` | Runs, manual and scheduled, created per UTC day. |
| `min_schedule_interval_seconds` | The shortest gap a DAG's schedule may leave between runs. |
| `max_task_pool_slots` | The largest `pool_slots` (task size) any task may have. |
| `default_pool_slots` | The size of the tenant's `default_pool`, the slots its tasks share ([Pools](/operate/pools/)). |

Server settings that share the engine fairly between tenants:

| Setting | What it does |
|---|---|
| `scheduler.dispatch.max_per_tenant_per_tick` | Most tasks one tenant dispatches per scheduler tick, so a wide fan-out does not delay the others. |
| `execution.max_warm_pods_per_tenant` | Most warm worker pods one tenant holds (100 by default). |
| `server.pools_read_only` | Locks the pools API, so tenants cannot raise their own slots, and puts tasks that name an undefined pool in `default_pool`. |

The [configuration reference](/reference/configuration/#operator-service-api)
says exactly when each limit is checked and how it is counted.

## When a limit is hit

A request that a limit refuses answers `403` with a detail that names the
limit, for example
`the tenant reached its limit max_runs_per_day of 50 for today (UTC)`, and the
CLI prints it. Registering a DAG that breaks `max_dags`,
`min_schedule_interval_seconds` or `max_task_pool_slots` is refused the same
way, naming the task or schedule at fault.

A scheduled run that the daily limit refuses is skipped, not failed. The
scheduler logs one warning per tenant and day, counts it in
`dexaflow_scheduler_decisions_total{decision_type="tenant_daily_run_cap"}`,
and creates the run on a later tick once the UTC day turns.

Lowering a limit never removes anything: a tenant above a new `max_dags` keeps
its DAGs and can update them, but cannot add more.

## Suspending a tenant

`PUT /api/v2/service/tenants/{tenant}/suspension` stops a tenant's new runs
without touching its DAGs. The mode says how much else stops: `hold` lets
queued and running work finish, `drain` also keeps ready tasks from being
dispatched, and `stop` fails the tasks already running. A refused trigger
answers `409` with the reason the operator gave. `DELETE` on the same path
resumes the tenant, and its scheduled work continues on the next tick. The
[configuration reference](/reference/configuration/#operator-service-api)
describes each mode.
