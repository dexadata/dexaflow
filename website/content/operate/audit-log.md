---
title: Audit log
linkTitle: Audit log
weight: 83
description: Which actions Dexaflow records, what an entry holds, how to read it in the UI and the API, and how long it is kept.
---

Dexaflow writes an entry to its audit log when someone changes what runs:
registers or promotes a DAG version, pauses a DAG, triggers or marks a run,
clears or marks a task. It also records sign-ins through an identity provider,
API token changes, operator service API calls, and the secret checks made on
behalf of running tasks. Entries live in the `audit_log` table of the
Dexaflow database, one row per action, and belong to a tenant.

## What is recorded

| Event | When |
|---|---|
| `dag.version.register` | A DAG version is registered (`dexaflow deploy`, `POST /api/v2/dags/{dag_id}/versions`). |
| `dag.version.set_current` | The current version of a DAG changes, by a deploy or a promotion. |
| `dag.pause`, `dag.unpause` | A DAG is paused or unpaused, including a register that sets it. |
| `dagrun.<run_type>.trigger` | A run is triggered, for example `dagrun.manual.trigger`. |
| `dagrun.mark.<state>` | A run's state is set, for example `dagrun.mark.failed` or `dagrun.mark.queued` for a re-run. |
| `taskinstance.clear` | A task instance is cleared, one entry per task. |
| `taskinstance.mark.<state>` | A task instance's state is set by a user. |
| `secret.scope_warning` | A task asked for a secret its DAG did not declare. |
| `secret.liveness_would_deny`, `secret.liveness_denied` | A secret was asked for by a task instance that was no longer live, under `auth.secret_liveness_mode` `observe` or `enforce` ([ADR 0055](/project/adrs/0055-secret-scoping-and-token-liveness/)). |
| `api_token.create`, `api_token.revoke` | A user creates or revokes an API token. |
| `api_token.retire_expired` | An expired token is replaced by a new one. |
| `oidc.login.success`, `oidc.login.failure`, `oidc.tenant_pin_rejected`, `oidc.jit_provision` | Single sign-on through OIDC, and the first sign-in that creates the user. |
| `oidc.cli_code`, `oidc.cli_consent`, `oidc.cli_device_verify` | `dexaflow login` through the browser. |
| `issuer.login.success`, `issuer.login.failure`, `issuer.bearer.failure` | Sign-in through a trusted issuer, and a bearer token from it that is refused. |
| `ci_oidc.exchange` | A CI job exchanges its identity token for a Dexaflow token. |
| `auth.break_glass` | A password sign-in while OIDC is on, allowed or refused. |
| `user.create` | A user is created. |
| `user.deactivate`, `user.activate` | An admin deactivates or reactivates a user through `PATCH /api/v2/users/{user_id}`. |
| `service.tenant.ensure`, `service.tenant.suspend`, `service.tenant.resume`, `service.user.ensure` | A call to the [operator service API](/reference/configuration/#operator-service-api). |
| `retention.purge` | The retention janitor deleted audit entries. |

Entries whose event starts with `dag.`, `dagrun.`, `taskinstance.` or
`secret.` are recorded on a DAG and also show on that DAG's tab.

Each entry records an action that was accepted, apart from the sign-in and
token entries, which also record refusals and carry an `outcome`. An entry is
written in the same transaction as the change for DAG registration,
promotion and pause, so one never exists without the other. The other entries
are written after the change and are best effort: if the write fails, the
server logs it and the change stands.

## What an entry holds

| Field | What it is |
|---|---|
| `event_log_id` | The entry's id. |
| `when` | When the action happened. |
| `event` | The action, from the table above. |
| `owner` | The email of the user who acted. Empty for actions with no user, such as the secret checks, a CI exchange or the retention janitor. |
| `dag_id` | The DAG, for entries recorded on a DAG. |
| `run_id`, `task_id`, `try_number` | The run and task the action was on, where it has them. |
| `extra` | Everything else, as a JSON object: the version, image and spec hash of a register, the versions on both sides of a `set_current`, the outcome and reason of a sign-in, the id and name of a token, the rows and cutoff of a purge. |

Secret values, passwords, API tokens and their hashes are never written to the
audit log. A token entry carries the token's id and name; a secret entry
carries the kind of secret and the task, not its value.

## Read it in the UI

The **Audit Log** screen, in the Browse menu, lists the entries of your tenant,
newest first. A DAG's own **Audit Log** tab lists the entries recorded on that
DAG. Reading the audit log needs the `read:audit_log` permission, which only
the `admin` role has, so the screen, the tab and the endpoint are for tenant
admins; other roles do not see the menu entry.

## Read it through the API

`GET /api/v2/eventLogs` returns the same entries in Airflow's EventLog shape:

```sh
curl -s "$DEXAFLOW_URL/api/v2/eventLogs?dag_id=nightly&limit=100" \
  -H "Authorization: Bearer $TOKEN"
```

- `dag_id` keeps the entries of one DAG.
- `limit` is 50 by default and at most 1000, or `server.max_page_limit` when
  that is lower.
- `offset` pages through the list. For a long history, follow the cursor of
  the response instead: each page past the first then costs the same as the
  first, whatever its depth.

Each tenant reads only its own entries. A sign-in event whose tenant cannot be
resolved, such as one that names a tenant that does not exist, is recorded in
the `default` tenant, so a refused sign-in is never dropped.

## How long it is kept

By default the audit log is kept forever. `retention.audit_log_days`
(`DEXAFLOW_RETENTION_AUDIT_LOG_DAYS`) deletes entries older than that many
days, in every tenant. Each cycle that
deletes entries writes a `retention.purge` entry with the number of rows and
the cutoff, so the purge itself stays on record. The
[configuration reference](/reference/configuration/) lists the other
retention settings, such as the batch size and the dry run.

If you need the log for longer than the database keeps it, export it on a
schedule through the API, or replicate the `audit_log` table, before the
retention window closes.

## What is not recorded

- Reads: listing DAGs, opening logs, reading variables or connections.
- Changes to connections, variables and pools, deleting a DAG or a run, and
  user changes other than creation. Use your database's own audit for those
  until they are recorded.
- Password sign-ins while OIDC is off.
- Actions of the scheduler and the agents, such as retries, reschedules and
  task outcomes: those are in the run's history and the task logs.
