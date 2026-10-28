---
title: "ADR 0090: the control plane waits for its schema, and names a database that was emptied, replaced, restored or repointed under it"
linkTitle: "0090 · Database replaced under a release"
weight: 840
description: "ADR 0090: a control plane that finds its schema absent or behind stays up and not ready, names the recovery command for its install method, and retries until the schema is current instead of exiting. An installation id kept in the database and in a ConfigMap the migrate wrapper owns tells an emptied or replaced database from a fresh one and makes accepting it an explicit step; a running pod that sees the id change exits so no cache outlives the old database. Self-migration on boot is opt-in, limited to roles that hold a DDL identity, and never runs against an ahead schema. A schema-readiness metric and an optional alert rule make the wait visible."
---

**Status:** Accepted
**Date:** 2026-10-16
**Relates:** ADR 0049 (api and scheduler roles), ADR 0068 (patch content), ADR 0033 (release flow)
**Issues:** #1846 (this ADR), #1842 (the crash loop that started it), #1020 (the migrate wrapper), #1614 (tenant id cache), #1619 (recovery table), #1023 (readiness re-checks the schema), #154 (Lite and Helm database docs)

## Context

Migrations run in one place on Pro: the chart's migration Job, a
`pre-install,pre-upgrade` hook (`helm/dexaflow/templates/migration-job.yaml`,
rendered only while `migrations.enabled` is true). Nothing else runs them. The
database can still lose or change its schema under a release that is already
installed: an `emptyDir` Postgres moved to another node (#1842, a Spot
preemption on GKE), a restore into a fresh instance, a restore of an older
backup, a failover to a lagging replica, or a changed `database.url`.

What the code does today, by timing:

- **At boot the process exits.** `openVerifiedPostgres`
  (`cmd/dexaflow-server/main.go`) calls `CheckSchemaCurrent`, whose verdict
  `checkSchemaCurrent` (`internal/storage/schema_check.go`) is an error for
  an absent `schema_migrations`, for a dirty version, and for a version
  behind the binary's latest. `run` returns it and `main` exits 1, before any
  listener is bound. The pod crash-loops until someone runs `helm upgrade`.
- **A running pod stays up and not ready.** `SchemaReady` re-reads the
  version on every `/readyz` (#1023) through `checkSchemaReady`, and an absent
  or behind schema answers 503 with `postgres schema not current`.
  `livenessHandler` (`internal/api/health.go`) is static on purpose, with
  the comment that a crash loop on a database problem is worse than a pod
  that stays up, not ready and available for diagnosis.

So one database state gives two behaviors, and the boot one contradicts the
policy the liveness handler states. The two verdicts already differ on
purpose in one state: a dirty version above the binary's latest is a
migration in flight and keeps a running pod ready. Both accept a clean
schema that is ahead of the binary, because migrations are expand-contract
and `helm rollback` runs an older image against a newer schema.

Three further gaps:

1. **An empty database looks like a fresh install.** Nothing in it, or in
   the release, records that this installation already had data. Migrating
   it automatically would bring back an empty, healthy control plane with
   every DAG version, run, user, connection and token gone, and no signal
   that this is an incident.
2. **In-process state outlives the database.** `Repository.tenantID`
   (`internal/storage/repository.go`) caches tenant name to id in a
   `sync.Map` for the life of the process (#1614). After the database is
   replaced, the cached ids point at rows that do not exist, or at other
   rows. Agents and warm workers holding attempt tokens issued against the
   old database are the same shape.
3. **The error text names one remedy.** The boot and readiness messages say
   "the Helm pre-upgrade migration Job, or `dexaflow db migrate` for Lite",
   which is no command an operator can paste, and says nothing when
   `migrations.enabled` is false.

On identities: ADR 0049 gives the api role a restricted network identity.
The storage code and its tests treat the api role as able to run on a
database identity without DDL privileges (`SchemaVersion` issues only a
`SELECT` for that reason), but the chart renders the same `databaseUrl`
Secret key for every Deployment today. Which identity holds DDL is a
property of the DSN an operator configures, not something the server can
read off its role.

## Decision

1. **Boot waits instead of exiting on an absent or behind schema.**
   - The process binds `/healthz` and `/readyz` (on the API port when it
     serves the API, and on the metrics port in every role) before it
     checks the schema, and serves nothing else until the schema is
     current. `/healthz` answers 200, so the startup and liveness probes
     pass; `/readyz` answers 503.
   - It re-reads the version with a capped exponential backoff (1 s
     doubling to 30 s) and logs one line per state change, not per
     attempt. Once the schema is current it builds the rest of the
     process as boot does today and becomes ready.
   - A dirty version equal to the binary's latest still refuses to boot,
     as today: the migration this binary needs is half applied and needs a
     person ([upgrades](/operate/upgrades/#an-interrupted-migration)). A
     dirty version below or above it waits like a behind schema, because a
     migration may be in flight; if it is stuck, the reason says dirty.
   - A clean schema ahead of the binary is accepted, with the warning boot
     logs today.
   - Lite is unchanged: `dexaflow lite` already applies the embedded
     migrations before it starts the server, and refuses an ahead schema.
2. **The not-ready reason names the recovery command.** The chart passes
   the release name, namespace, chart version and `migrations.enabled` to
   the server as environment variables. The log line carries the full
   message; `/readyz` carries a fixed reason (`schema absent`,
   `schema behind vN, requires vM`, `schema dirty at vN`, `database
   replaced`) and the command, and never the DSN or a host, since it is
   unauthenticated. The command is, by install method:
   - chart with migrations on: `helm upgrade <release>
     oci://ghcr.io/dexadata/charts/dexaflow -n <namespace> --version <chart
     version> --reuse-values`, which runs the hook again;
   - chart with migrations off: the operator's own migration step, named as
     such, with the migrate image and tag of this release;
   - Lite: `dexaflow db migrate`.
   The hook Job is deleted once it succeeds (`hook-succeeded`), so there is
   no Job to copy with `kubectl create job --from`, and the hint never
   suggests it.
3. **An installation id tells a replaced database from a fresh one.**
   - A migration adds a one-row `installation` table. The id in it is
     written by the migrate wrapper of #1020 after `Up`, never by the SQL,
     from the value the chart hands the Job.
   - The id outside the database lives in a ConfigMap that the migrate
     wrapper creates and updates itself, not in a chart template, so no
     Helm `lookup` is involved and `helm template`, Argo CD and Flux
     installs behave like `helm install`. The id is not a secret. The
     Job's service account gets `get` and `create` on ConfigMaps in the
     release namespace and `update` on that one name. An operator can
     still pin the id with `installation.id`; a pinned id that differs
     from the ConfigMap is the acknowledgement described below. The
     server reads the id from the ConfigMap through the environment.
   - The wrapper writes the id into a database without one in two cases
     only: the schema is absent and the namespace has no installation
     ConfigMap yet (the first install), or the schema exists from before
     the `installation` table (an existing installation upgrading, adopted
     once). In both cases it creates the ConfigMap with the same id.
   - Any other mismatch is reported as a replaced database, by the Job
     (which then fails without migrating) and by the server (which stays
     not ready under decision 1): an absent schema on an established
     release, a schema without an id, or a different id. A restore of a
     backup of the same installation carries the same id and is not a
     replacement; it is checked as absent, behind or ahead as usual.
   - An operator accepts a replacement by changing `installation.id`: to
     the id the database carries, for a database adopted from another
     installation, or to a new id, for an emptied database, which the
     wrapper then writes because the chart's id differs from the one the
     ConfigMap held. A changed id stays changed, so the acknowledgement
     applies once and does not accept the next replacement.
4. **Self-migration on boot is opt-in.** `migrations.onBoot` (default
   false) makes the server apply the embedded migrations before decision
   1's wait, through the same wrapper package the Job image runs (#1020),
   in-process, not in an initContainer.
   - Only the `all` and `scheduler` roles migrate; the api role never does,
     whatever its DSN can do. In split mode the chart renders the setting
     on the scheduler Deployment only.
   - Only when the schema is absent or behind, never when it is ahead or
     dirty, and never when decision 3 reports a replaced database.
   - golang-migrate's advisory lock serializes it with the Job and with
     other replicas. The hook Job stays the default path and keeps the
     ordering an upgrade needs; `onBoot` covers what the hook cannot see,
     such as `migrations.enabled=false` installs and a repoint between
     upgrades.
5. **A running pod exits when the installation id changes.** Readiness
   reads the id with the version, in the same probe query. When the id the
   process read at boot is gone or different, the process logs the reason
   and exits with a code of its own. Its restart comes up under decision 1
   and waits, not ready, for decision 3's acknowledgement. Exiting is the
   only flush that is complete: it drops the tenant id cache (#1614), the
   spec cache and every other cache without listing them. Agents and warm
   workers that hold attempt tokens of the old database reconnect to a
   process that does not know those attempts; that path is the existing one
   for an unknown attempt and is not changed here.
6. **The wait is visible, not silent.** A pod that stays not ready with
   nobody looking is almost as bad as a crash loop.
   - Every role exports `dexaflow_schema_ready` (1 or 0) with a `reason`
     label taken from decision 2's fixed reasons, and
     `dexaflow_schema_wait_seconds`, the time since the process started
     waiting.
   - The chart gains an optional PrometheusRule
     (`metrics.prometheusRule.enabled`, default false, next to the existing
     ServiceMonitor) with one alert: `dexaflow_schema_ready == 0` for 5
     minutes, critical when the reason is `database replaced`. The
     observability docs give the same expression for installs without the
     Prometheus Operator.
   - While the ids differ, the process opens no write path: it serves only
     the probes and metrics, so a database adopted by mistake is never
     written to before an operator acknowledges it.

## Alternatives considered

- **An initContainer that runs `migrate up` on every pod (#1842).** On
  `helm rollback` an older image meets a newer schema, and `migrate up` with
  the older files fails on a version it does not have, while the boot check
  accepts an ahead schema on purpose. It needs the DDL DSN in every pod,
  the api pods included, and the #1020 review already ruled it out for
  doubling the failure surface of the DSN. It would also migrate an emptied
  database without a word, which is the silent data loss of gap 1.
- **Keep the hook only, and document the recovery next to the error.**
  Cheapest, and decision 2 takes the useful half of it. It leaves the crash
  loop of #1842 in place and still needs a person for a database that only
  needed migrating.
- **The server always migrates itself when the schema is absent or behind.**
  Removes the human step but needs DDL in every role, contradicts the api
  role's restricted identity, and, without decision 3, turns a lost database
  into an empty healthy one. Kept as an opt-in, bounded by decisions 3 and 4.
- **Flush the known caches on an id change instead of exiting.** It needs a
  list of every in-process cache that stays correct as code is added, and
  one missed cache is a cross-tenant read. A restart is already a supported
  path for every pod.

## Consequences

- A release whose database lost its schema is down and diagnosable, not
  crash-looping: the pod is listed, its log and `/readyz` say what happened
  and what to run. A rollout whose migration Job failed stalls on unready
  new pods while the old ones keep serving, instead of a crash loop.
- The boot path changes order: the probe listeners bind before the schema
  check, so `checkSchemaCurrent` becomes the verdict of a loop instead of a
  return value of `run`. The startup probe budget of the chart no longer
  bounds a schema wait, because `/healthz` answers during it.
- The migrate wrapper of #1020 becomes a prerequisite: decisions 3 and 4
  both run through it, and the Job image switches to it.
- New chart surface: `installation.id`, `migrations.onBoot`,
  `metrics.prometheusRule.enabled`, the release environment variables of
  decision 2, and a Role for the migrate Job's ConfigMap. The ConfigMap
  itself is not rendered by the chart, so `helm uninstall` leaves it in
  place; reinstalling into the same namespace against an empty database is
  therefore reported as a replaced database, which is the safe reading.
  Each needs its helm-unittest cases and its `values.yaml` doc comment.
- Follow-ups outside this record: expand/contract migrations so a rollback
  always runs on the newer schema (#1994), a warning for a bundled Postgres
  without a volume plus restore drills (#1995), and an end-to-end test that
  empties the database under a running release (#1996).
- Docs: one operate page for a database that was lost, restored or
  repointed, covering Helm and Lite (#154), linked from every reason of
  decision 2; and the interrupted-migration table in
  [upgrades](/operate/upgrades/#an-interrupted-migration) stays the
  reference for a dirty version (#1619).
