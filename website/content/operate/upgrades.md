---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /upgrades.html
# --- end AUTO redirect aliases ---
title: Upgrades
weight: 40
description: "Upgrade a Dexaflow control plane safely, edition by edition."
---

This page is the canonical answer to "I'm on `v0.x.y` and want to install a
newer tag — what happens to my state?"

{{% alert title="Upgrade contract" color="info" %}}
The upgrade contract below is honored by the Lite edition; we test it on
every release. We will not knowingly ship a release that breaks it without a
clear migration note. On the `v0.x` line we do not yet promise
forward/backward compatibility across major versions — that is a v1
concern.
{{% /alert %}}

## Lite — what is preserved across upgrades

Reinstalling (running the new `install.sh`, or `brew upgrade dexaflow` once
that ships) over an existing Lite install **preserves all of these by
default**:

| What | Where | Notes |
|---|---|---|
| **Workspace** | The path under `workspace:` in `~/.dexaflow/config.yaml` (default `~/dexaflow`) | Your `dag.py`, `dexaflow.yaml`, and any other project files. The installer does not touch this directory. |
| **Datastore** | `~/.dexaflow/managed-postgres/data/` (managed Postgres) **or** the `leoflow-data-*` Docker volume (Docker Postgres) | Includes DAG history, runs, task instances, XCom, Variables, Connections. The new binary applies any pending SQL migrations on first start. |
| **Admin login** | `~/.dexaflow/config.yaml` (`admin_email`, `admin_password_hash`) | Your password is not regenerated. Use `dexaflow lite reset-password` if you forgot it. |
| **JWT signing secret** | `~/.dexaflow/config.yaml` (`jwt_secret`) | Browser sessions survive the upgrade (no forced re-login). |
| **Parser + runtime venv** | `~/.dexaflow/venv/` | Project dependencies are reinstalled lazily as needed (the marker at `~/.dexaflow/venv/.leoflow-deps` triggers a refresh when the project's deps change). |

## What changes

| What | Why |
|---|---|
| The `dexaflow` / `dexaflow-server` / `dexaflow-agent` binaries on `PATH` (and their `leoflow*` links) | Replaced by `install.sh`. |
| `~/.dexaflow/python/` (managed CPython) | Pinned per release; replaced if the new release pins a different version. |
| The SQL schema | The new binary applies any missing migrations on first start. |

## Drift detection

If you somehow run an **older** `dexaflow` binary against a database a **newer**
binary has already migrated, the older binary refuses to start with:

```
database is at schema version 18 but this binary only knows up to 15;
an older `dexaflow` is being run against a newer database.
Upgrade the binary, or run `dexaflow uninstall --purge` to start over
(this WIPES your data)
```

This is the safe behavior: continuing with a stale schema would corrupt
rows the older binary does not understand. Upgrade, or wipe — never both.

## Fresh start

If you want a clean slate without the prior history:

```sh
dexaflow uninstall --purge
```

`--purge` removes the binaries, `~/.dexaflow/` (config + datastore + parser
sources), and the workspace directory. Without `--purge`, uninstall keeps the
datastore and workspace so a future reinstall picks up where you left off
(this is also the contract upgrades rely on).

## How to test an upgrade safely (recommended)

Before installing a newer tag on a Lite install you depend on:

1. **Back up first.** See [Backup and restore](/operate/backup-restore/):
   ```sh
   dexaflow lite backup --output ~/snap-before-upgrade.tar.gz
   ```
2. Install the new version. The drift detector protects you from the worst
   downgrade case.
3. If anything looks off, restore from the tarball.

## Pro — upgrade path

The Pro control plane upgrades with the standard Helm flow: re-run
`helm upgrade` against the same release, pointing the pinned image tag at the
newer version.

```sh
# OCI chart (the primary install path, see Installation):
helm upgrade dexaflow oci://ghcr.io/dexadata/charts/dexaflow --version <VERSION> \
  -n leoflow --reset-then-reuse-values

# Or pin the image tags explicitly:
helm upgrade dexaflow oci://ghcr.io/dexadata/charts/dexaflow --version <VERSION> \
  -n leoflow --reset-then-reuse-values \
  --set image.tag=<VERSION> \
  --set migrations.image.tag=<VERSION>
```

A release installed before the rename was installed from the chart named
`leoflow`, and the chart name is part of its Deployment selector, which
Kubernetes does not let an upgrade change. Keep upgrading it with
`charts/leoflow`, the same chart published under its old name:

```sh
helm upgrade <release> oci://ghcr.io/dexadata/charts/leoflow --version <VERSION> \
  -n leoflow --reset-then-reuse-values
```

### Use `--reset-then-reuse-values`, not `--reuse-values`

`--reuse-values` does **not** merge the new chart's defaults. Helm rebuilds the
previous release's coalesced values and assigns them as the new chart's values,
so every key the new chart added is absent and every default the new chart
changed is still at its old value. The upgrade is rendered by new templates
against an old chart's value tree, which is a shape neither version was tested
against.

That is not theoretical for this chart. Upgrading a 0.4.6 release to 0.4.7 with
`--reuse-values` fails to render on two counts: `probes.startup` does not exist
in a 0.4.6 value tree, and `config.server.cors.allowedOrigins` is still at
0.4.6's documented `["*"]`, which 0.4.7 refuses at render time.

`--reset-then-reuse-values` is the flag that means what most operators think
`--reuse-values` means: start from the new chart's defaults, lay the previous
release's *user-supplied* values over them, then apply `--set` and `-f`. New keys
arrive with their defaults, changed defaults take effect, and anything you
actually configured is preserved. Prefer it for every cross-version upgrade.
Keeping a values file under version control and passing `-f` is better still.

The chart runs a **pre-upgrade migrations Job** (`golang-migrate` against
`database.url`) before the new `dexaflow-server` rolls out, so the schema is
brought to parity before any new binary serves traffic. The same startup
**drift detector** described above protects a Pro control plane from being run
against a database a newer binary already migrated. Use `--version <VERSION>`
with the chart version — the [latest release](https://github.com/dexadata/dexaflow/releases)
tag with the leading `v` stripped.

### The migration Job's pod is not part of the control plane

The hook pod runs under its own application name —
`app.kubernetes.io/name: <chart name>-migrate`, with
`app.kubernetes.io/component: migrate` — deliberately *not* the control plane's
`app.kubernetes.io/name`. A Service selector matches every pod whose labels
contain it, so a hook pod carrying the control plane's own selector labels is
selected by the control-plane Service and PodDisruptionBudget for as long as it
runs, and a Job pod has no readiness probe: it counts as `Ready` from the instant
its container starts. On `helm upgrade` — the one path where those objects
already exist while the hook runs — that put a pod that serves nothing into the
control plane's endpoint set and into its disruption budget's healthy count.

Two consequences for your own tooling:

- **Do not select control-plane pods by `app.kubernetes.io/instance` alone.**
  That label is still on the hook pod on purpose, so
  `kubectl -n <ns> get pods -l app.kubernetes.io/instance=<release>` finds a
  wedged migration. Add `app.kubernetes.io/name=<chart name>` (or, in
  `split.enabled` installs, `app.kubernetes.io/component=api|scheduler`) when you
  mean the control plane.
- **With `networkPolicy.enabled`, the hook pod is governed by no policy** — on
  `helm install` and on `helm upgrade` alike. Egress is allow-all by default, so
  a default install is unaffected. If your namespace carries a default-deny
  policy from a platform team, give the migration pod its own egress allowance to
  Postgres (match on `app.kubernetes.io/component: migrate`), and create it
  outside the release or as a hook with a negative `helm.sh/hook-weight` — a
  policy the chart creates normally does not exist yet when the *pre-install*
  hook runs.

### Upgrading to 0.5.1: finish the rollout promptly

0.5.1 fences each task execution by an attempt epoch carried in the agent's
token ([ADR 0051](/project/adrs/0051-separate-orchestration-and-execution-state-machines/),
amendment). Tasks that are already running when you upgrade keep their old
tokens and are not affected. While old and new control-plane replicas serve
side by side during a rolling upgrade, three cases cost one redundant
re-placement of a task, never a wrong result:

- an old replica renews a new task's token and drops the epoch it does not
  know. The task keeps heartbeating and resolving its secrets, but when it
  finishes its final state report is rejected, and it is re-placed and runs
  again;
- an old replica performs a new task pod's projected ServiceAccount token
  exchange, which also drops the epoch. The pod exchanges before it reports
  `running`, so it is told to stop on that first report, before your code
  starts, and is re-placed once the dispatch-lost reaper sees it;
- leadership moves back to an old replica, which dispatches a task a new
  replica already advanced. That task is told to stop on its first report and
  is re-placed.

All three last only as long as old replicas serve agent traffic. On an HA install,
let the rollout finish without pausing it, or scale the control plane to one
replica for the upgrade. `dexaflow_agent_legacy_attempt_token_total` counts agent
calls that still use a token without the epoch; it falls to zero once every
task started before the upgrade has finished. A later minor release will refuse
such tokens.

If you roll back to 0.5.0 and later upgrade again, a task that 0.5.0 started
on a task instance that 0.5.1 had already dispatched before the rollback is
not covered by "keep their old tokens": the rollback keeps the attempt epoch
column, so its token without the epoch no longer matches, its final state
report is rejected, and it is re-placed and runs again. Let such tasks finish
before upgrading again, or accept the one re-run.

0.5.1 also stores each execution's log under its own name,
`{try}.e{epoch}.log` instead of `{try}.log`, so a re-placed execution no longer
overwrites the one before it. 0.5.0 reads only `{try}.log`. While old replicas
serve the API during the rollout, and after a rollback to 0.5.0, the log view
shows "No logs available" for tasks that 0.5.1 ran. Those logs are not deleted:
they show again once 0.5.1 serves the API. Logs written before the upgrade keep
`{try}.log` and stay readable by both versions. If you read log objects or files
directly, expect both names.

### What the 0.5.1 migrations do

0.5.0 left the schema at migration 026. 0.5.1 applies 027 to 039, and 040 as
well when #1421 is in the release. Nine of them change indexes or the page
layout of `task_instances`, which is what makes this upgrade different from
the column additions earlier releases shipped: eight are `CREATE INDEX
CONCURRENTLY` or `DROP INDEX CONCURRENTLY` statements and one changes the
table's `fillfactor`.

| Migration | What it does | Shape |
|---|---|---|
| `027_dag_runs_version_index` | Adds `idx_dag_runs_version` on `dag_runs (dag_version_id)`, the foreign key a DAG deletion checks once per deleted version; without it each check scanned `dag_runs` (#1319). | `CREATE INDEX CONCURRENTLY` |
| `028_drop_ti_run_index` | Drops `idx_ti_run` on `task_instances (dag_run_id)`: the leading column of the `task_instances_unique` constraint already serves every lookup by run (#1319). | `DROP INDEX CONCURRENTLY` |
| `029_drop_ti_task_index` | Drops `idx_ti_task` on `task_instances (dag_run_id, task_id)`, the same constraint's leading columns (#1319). | `DROP INDEX CONCURRENTLY` |
| `030_drop_ti_running_heartbeat_index` | Drops `idx_ti_running_heartbeat`, the one index on `last_heartbeat_at`, so a heartbeat update writes no index entry; the agent-lost reaper reads running rows through `idx_ti_state` instead (#1319). | `DROP INDEX CONCURRENTLY` |
| `031_task_instances_fillfactor` | Sets `fillfactor = 85` on `task_instances`, leaving 15% of every page written from then on free for HOT updates. Existing pages are not rewritten (#1319). | `ALTER TABLE` in a transaction, `lock_timeout` 5 s |
| `032_dag_versions_dag_hash_index` | Adds `idx_dag_versions_dag_hash` on `dag_versions (dag_id, spec_hash)`, the exact lookup registration runs on every bundle push (#1324). | `CREATE INDEX CONCURRENTLY` |
| `033_drop_dag_versions_hash_index` | Drops `idx_dag_versions_hash` on `dag_versions (spec_hash)`, replaced by 032 (#1324). | `DROP INDEX CONCURRENTLY` |
| `034_drop_dag_versions_spec_gin_index` | Drops the GIN index `idx_dag_versions_spec` on `dag_versions.spec`: no query filters a spec by containment, and the index made every version insert decompose the whole DAG document (#1324). | `DROP INDEX CONCURRENTLY` |
| `035_ti_active_tenant_index` | Adds the partial index `idx_ti_active_tenant` on `task_instances (tenant_id, state) INCLUDE (pool)` over the scheduled, queued, running and deferred rows, for pool occupancy (#1332). | `CREATE INDEX CONCURRENTLY` |
| `036_task_instance_released_at` | Adds `task_instances.released_at`, the stamp the orphan-run reaper counts as activity (#1390). | `ALTER TABLE ADD COLUMN IF NOT EXISTS` |
| `037_reconcile_tenant_system_roles` | A data migration: makes every tenant's built-in roles and their grants equal to the `default` tenant's (#1371; the changelog entry says what it overwrites). Idempotent, and its down is a no-op. | DML, one transaction |
| `038_attempt_epoch` | Adds `attempt_epoch` to `task_instances` and `task_instance_history`, default 0 (#1392). | `ALTER TABLE` in a transaction, `lock_timeout` 5 s |
| `039_infra_confirmed_at` | Adds `task_instances.infra_confirmed_at` and then, in a second transaction, stamps the infra failures that already exist as confirmed (#1406). | `ALTER TABLE` with `lock_timeout` 5 s, then an `UPDATE` |
| `040_tenant_limits` (when #1421 is in the release) | Adds the per-tenant limit columns and the daily run counter to `tenants`; every default means unlimited. | `ALTER TABLE` in a transaction |

**Where they run.** On Pro, the chart's pre-install/pre-upgrade hook Job
(named `<release>-migrate`, or `<release>-dexaflow-migrate` when the release
name does not contain the chart name; its pod carries
`app.kubernetes.io/component: migrate`) runs
`migrate -path /migrations -database $(LEOFLOW_DATABASE_URL) up` with the
release's `ghcr.io/dexadata/dexaflow-migrate` image, which bundles the SQL
files with the golang-migrate CLI, before the new control plane rolls out and
while the old replicas keep serving (their readiness check tolerates a
migration in flight above their own schema version). The Job has
`backoffLimit: 3`, so a failed pod is retried three times, and a failed Job
is kept for inspection:

```sh
kubectl -n <namespace> logs -l app.kubernetes.io/component=migrate --tail=50
```

On Lite, the new binary applies the same files from its embedded copy when
`dexaflow lite` starts (or when you run `dexaflow db migrate`), one file at a
time in the same way, so everything below applies to both editions unless it
names Helm.

**Why the hook can take longer than usual.** `CONCURRENTLY` is what keeps the
index work from blocking reads and writes on `task_instances` and
`dag_versions` during the upgrade, but each such statement waits for every
transaction that was open in the database when it started, whatever table
that transaction touches: a session a client left idle in a transaction, a
long report query, a backup holding a snapshot. The 0.5.1 audit measured 027
at 0.1 s on a quiet database and at 14 s behind an unrelated
`BEGIN; SELECT count(*) FROM tenants; SELECT pg_sleep(15)`. A build also
scans its table twice, so 035 grows with `task_instances`, and so do the
three index rebuilds a rollback runs on that table (below). Upgrade in a
window without long-running transactions, and look for them first:

```sql
SELECT pid, usename, application_name, state,
       now() - xact_start AS transaction_age, left(query, 60) AS query
FROM pg_stat_activity
WHERE datname = current_database()
  AND xact_start IS NOT NULL
  AND (state = 'idle in transaction' OR xact_start < now() - interval '1 minute')
ORDER BY xact_start;
```

While a build waits, `pg_stat_progress_create_index` shows its phase and the
session it is waiting for (`current_locker_pid`). End a session you have
identified with `SELECT pg_terminate_backend(<pid>)`; an
`idle_in_transaction_session_timeout` on the roles that connect to the
database keeps the case from recurring. Helm's `--timeout` (5 minutes by
default) bounds how long `helm upgrade` waits for the hook: when it expires
the upgrade fails but the Job keeps running, so check the Job before you
retry. A retried `helm upgrade` deletes the previous Job and its pod before it
creates a new one (`before-hook-creation`), and killing a build in progress
is exactly the interrupted case below.

### An interrupted migration

A hook pod evicted or drained mid-statement, a database connection cut, a
retried `helm upgrade` that deleted a running Job, a `dexaflow lite` process
killed while it migrates: all of them leave the same two things behind.

- **A dirty version.** golang-migrate writes the version a file moves to with
  `dirty = true` before it runs the file and clears the flag after it, so an
  interrupted file leaves `schema_migrations` at that version, dirty. Every
  later run, the Job's three retries included, stops at
  `Dirty database version N. Fix and force version.`; `dexaflow lite` refuses
  to start with `database schema is marked dirty at version N`; a Pro control
  plane refuses to boot against a dirty version too, while the replicas that
  were already serving keep serving.
- **An INVALID index, sometimes.** An interrupted `CREATE INDEX CONCURRENTLY`
  (027, 032, 035) leaves an index with the final name that Postgres marks
  INVALID and never uses, but the name is taken. Those three files
  deliberately have no `IF NOT EXISTS`: it would skip the rebuild on a retry
  and keep the invalid index, so a retry fails with `already exists` instead.
  An interrupted `DROP INDEX CONCURRENTLY` (028, 029, 030, 033, 034) can leave
  the index it was dropping marked INVALID as well.

Find out where you are, with any SQL client as the Dexaflow role:

```sql
SELECT version, dirty FROM schema_migrations;
SELECT indexrelid::regclass AS invalid_index FROM pg_index WHERE NOT indisvalid;
```

The second query finds every invalid index, whatever the migration.

**Recovery for the three index builds** (027 `idx_dag_runs_version`, 032
`idx_dag_versions_dag_hash`, 035 `idx_ti_active_tenant`), as each file's own
comment spells it out:

1. Drop the invalid index: `DROP INDEX CONCURRENTLY IF EXISTS <name>;`
2. `migrate force N-1` (26, 31 or 34): it writes version N-1 with
   `dirty = false` and runs no SQL, so `schema_migrations` is back where it
   was before the interrupted file.
3. Re-run the upgrade (`helm upgrade` again, or start `dexaflow lite`); the
   hook applies N and whatever follows it.

For the other files the first step and the version to force change. The rule
is the one the files follow: force the version whose schema the database now
holds, then re-run.

| Interrupted file | Version shown dirty | Before `force` | `force` to |
|---|---|---|---|
| 027, 032, 035 (index build) | 27, 32, 35 | drop the INVALID index named above | 26, 31, 34 |
| 028, 029, 030, 033, 034 (index drop) | 28, 29, 30, 33, 34 | run the file's `DROP INDEX CONCURRENTLY IF EXISTS` by hand (`idx_ti_run`, `idx_ti_task`, `idx_ti_running_heartbeat`, `idx_dag_versions_hash`, `idx_dag_versions_spec`); it is safe to repeat | the same number: the drop is now done |
| 031 (`fillfactor`), 038 (`attempt_epoch`) | 31, 38 | nothing: each runs in a transaction with a 5 s `lock_timeout`, so nothing changed | 30, 37; retry when the table is quiet |
| 036, 039, 040 (columns) | 36, 39, 40 | nothing: `ADD COLUMN IF NOT EXISTS`, and 039's backfill, can be repeated | 35, 38, 39 |
| 037 (role reconcile) | 37 | nothing: one transaction, rolled back, and the file is idempotent | 36 |

**How to run `migrate force`.** The hook image is the golang-migrate CLI plus
the SQL files at `/migrations`, so the `version`, `force`, `goto` and `up`
commands this page and the migration files name are all in it. From a
workstation that can reach the database, with the CLI (`go install -tags 'postgres'
github.com/golang-migrate/migrate/v4/cmd/migrate@v4.20.1`, the version the
image builds) and the repository checked out at the release tag:

```sh
migrate -path migrations -database "$DATABASE_URL" version    # prints e.g. "27 (dirty)"
migrate -path migrations -database "$DATABASE_URL" force 26
```

In the cluster, run the release's migrate image as a one-off pod that reads
the DSN from the Secret the hook uses (`<release>-secrets`, key `databaseUrl`,
or the Secret you named in `database.existingSecret`):

```yaml
# migrate-force.yaml
apiVersion: v1
kind: Pod
metadata:
  name: migrate-force
spec:
  restartPolicy: Never
  automountServiceAccountToken: false
  securityContext:
    runAsNonRoot: true
    runAsUser: 65532
    runAsGroup: 65532
    seccompProfile:
      type: RuntimeDefault
  containers:
    - name: migrate
      image: ghcr.io/dexadata/dexaflow-migrate:<VERSION>   # the 0.5.1 tag you are upgrading to
      args: ["-path=/migrations", "-database=$(LEOFLOW_DATABASE_URL)", "force", "26"]
      env:
        - name: LEOFLOW_DATABASE_URL
          valueFrom:
            secretKeyRef:
              name: <release>-secrets
              key: databaseUrl
      securityContext:
        allowPrivilegeEscalation: false
        readOnlyRootFilesystem: true
        capabilities:
          drop: ["ALL"]
```

```sh
kubectl -n <namespace> apply -f migrate-force.yaml
kubectl -n <namespace> logs -f pod/migrate-force
kubectl -n <namespace> delete pod migrate-force
```

If `database.caConfigMap` is set, give the pod the same `db-ca` volume and
mount the hook Job has (`kubectl -n <namespace> get job <release>-migrate -o yaml`
shows them). On Lite the same CLI commands work against the Lite database
(`leoflow_dev`) with `dexaflow lite` stopped, since it migrates on start; the
snapshot you took before upgrading is usually the quicker way back.

**The same on the way down.** The down files of 028, 029, 030, 033 and 034
rebuild the indexes they dropped, `CONCURRENTLY` and, deliberately, without
`IF NOT EXISTS`, for the reason given above. A down interrupted mid-build
shows the version below it dirty (golang-migrate writes the version a file
moves to) and leaves an INVALID index. Drop it with
`DROP INDEX CONCURRENTLY IF EXISTS <name>`, run `migrate force N` with N the
number of the interrupted down file (the state the database is in: the index
absent), and run the down again. A retried down without that cleanup fails
the same way a retried up does.

### Rolling back 0.5.1 to 0.5.0

**Pro.** `helm rollback <release> <revision>` rolls the chart and the images
back and leaves the schema where 0.5.1 left it. That is supported: the 0.5.0
control plane boots against a schema ahead of its own with a warning
(`database schema is ahead of this binary; proceeding`), because every 0.5.1
migration is expand-only from its point of view. The new columns default to
values it never reads, the new indexes only help it, and the dropped ones
were redundant with indexes that remain (the agent-lost reaper reads running
rows through `idx_ti_state` instead of `idx_ti_running_heartbeat`: 3.5 ms
instead of 1.3 ms for 710 running task instances among 5 million rows, in
the audit's measurement). Prefer `helm rollback` to a `helm upgrade` that
points at chart 0.5.0: a rollback runs no pre-upgrade hook, while that
upgrade runs 0.5.0's migration Job, whose image carries files up to 026 only
and fails on a database at 039 with `no migration found for version 39`. If
you must use `helm upgrade`, pass `--set migrations.enabled=false` or run the
down migration first. What the section above says about a rollback (the logs
0.5.1 wrote, the tasks 0.5.1 dispatched) applies either way.

**Lite.** `dexaflow lite` 0.5.0 refuses to start against a database above 026
(`database is at schema version 39 but this binary only knows up to 26`), and
no `dexaflow` command runs a down migration. The supported path is the
snapshot from [How to test an upgrade safely](#how-to-test-an-upgrade-safely-recommended):
`dexaflow uninstall --purge`, reinstall 0.5.0, `dexaflow lite restore`. The
backup page has the
[worked example](/operate/backup-restore/#worked-example-roll-back-a-botched-upgrade).

**Restoring the 0.5.0 schema.** Where you need the exact 0.5.0 schema back
(Lite without a snapshot, or Pro before a `helm upgrade` to 0.5.0 with the
hook on), run the down migrations from the highest migration your 0.5.1 build
carries (039 today, 040 when #1421 is in the release) to 026, with the CLI or
the 0.5.1 migrate image, which carry the down files; the 0.5.0 image does not:

```sh
migrate -path migrations -database "$DATABASE_URL" goto 26
```

The down rebuilds five indexes `CONCURRENTLY`, three of them on
`task_instances` (`idx_ti_run`, `idx_ti_task`, `idx_ti_running_heartbeat`)
and two on `dag_versions` (`idx_dag_versions_hash` and the GIN
`idx_dag_versions_spec`), so it waits for open transactions and scans the
tables the way the upgrade did, and it can be interrupted the same way (see
the end of the previous section). It drops the three indexes 0.5.1 added,
resets the `fillfactor` (pages already written keep their free space), and
drops `released_at`, `attempt_epoch` on both tables, `infra_confirmed_at`
and, with 040, the tenant limit columns, so limits an operator set are lost
and tenants are unlimited again. 037's down is a no-op: the reconciled role
grants stay. The rows in every table are preserved: the 0.5.1 audit ran 027
to 035 up, down to 026 and up again on a seeded database with both Postgres
drivers and found the index set and the `pg_dump -s` schema identical before
and after, with 036 checked separately.

**What a rollback to 0.5.0 loses, with or without the down migration:**

- **Logs written by 0.5.1.** 0.5.0 reads only `{try}.log`; 0.5.1 writes
  `{try}.e{epoch}.log` and, with `logs.sink.layout: segmented`, numbered
  segments under `{try}.e{epoch}.log.d/`. Neither is deleted, and both show
  again once 0.5.1 serves the API, but a 0.5.0 control plane shows "No logs
  available" for every task 0.5.1 ran, whatever the layout. The layout
  setting itself is unknown to 0.5.0 and ignored.
- **Attempt epochs.** With the down migration the `attempt_epoch` columns are
  dropped, and a later upgrade starts every row at epoch 0 again, the same
  state as the first upgrade. Without it the column stays, and the note above
  about a task 0.5.0 started on a row 0.5.1 had dispatched applies.
- **The other 0.5.1 columns.** The down migration drops `released_at` and
  `infra_confirmed_at`; a later upgrade recreates them, and 039 stamps the
  infra failures that exist at that time as confirmed again. The per-tenant
  limits of 040 are lost with it.

### What changes in 0.5.1 without a flag

The performance work in 0.5.1 is gated where it changes behaviour (next
section). These changes are not, and an operator sees them on upgrade:

- **Four metric families are gone** (#1323): `dexaflow_pods_running`,
  `dexaflow_scheduler_leader`, `dexaflow_active_dag_runs`,
  `dexaflow_queued_tasks` and their `leoflow_` twins. Only `pods_running` ever
  produced a series, a constant 0 that nothing set; a panel or alert on it now
  shows no data, so count task pods with kube-state-metrics instead. The other
  three were declared and never written, so nothing that reads them sees a
  change.
- **Long ids in pod and staging labels are hashed** (#1320): a DAG, task, run
  or tenant id longer than 63 characters is cut to a prefix plus a hash of the
  full id in task pod and staging volume labels, so those tasks dispatch
  instead of failing as `dispatch_failed`. Values that fit are unchanged, so
  your own selectors keep matching and pods created before the upgrade are
  still found by the reapers and the GC.
- **UI assets are gzipped once** (#1309): each static file is compressed once
  and served from memory with a strong `ETag` per encoding and
  `Vary: Accept-Encoding`; conditional requests get `304 Not Modified` and
  `Range` requests `206 Partial Content`. About 3 MB more memory per API
  replica, and a cache in front of the UI must honour `Vary`.
- **Warm workers sweep processes between attempts and are not dumpable**
  (#1316): after every attempt the warm worker kills every process descended
  from it, and exits (the pool replaces the pod) if one cannot be killed; the
  warm agent is no longer dumpable, so a crashing warm agent leaves no core
  dump. Warm pools are opt-in, so an install without them is unaffected.
- **The scheduler tick reads and plans differently** (#1330, #1334): a tick
  loads the task instances of every active run in one query instead of one
  per run, and plans each run on an index of its DAG version's tasks built
  once. Scheduling decisions are unchanged; the tick is cheaper and no longer
  grows with the number of runs in flight or with the square of a fan-out.
- **The index changes** (#1319, #1324, #1332): the migrations in the table
  above. A larger `task_instances` table means a longer first upgrade, since
  035 scans it twice, and a longer rollback, since 028 to 030 rebuild three
  indexes on it.
- **Scheduled DAGs outside the default tenant start firing** (#1361): they
  never ran before, so one with `catchup: true` and a `start_date` backfills
  its missed slots after the upgrade, bounded per tick and by
  `max_active_runs`; pause it or set `catchup: false` first if you do not want
  that.

### New opt-in settings in 0.5.1

Every setting below defaults to the 0.5.0 behaviour; the
[configuration reference](/reference/configuration/) has the full entry for
each, and the Helm value where one exists.

| Setting | Default | What it does when set | PR |
|---|---|---|---|
| `scheduler.dispatch.buffer_size` and `scheduler.dispatch.workers` (Helm `config.scheduler.dispatch.bufferSize` / `workers`) | `0` / `0` | Buffered dispatch: the tick enqueues task pods and a worker pool creates them, so a large fan-out no longer stretches the tick by every Kubernetes API call. A full buffer is backpressure, not a failed dispatch, and a pod create that fails in a worker is re-offered like a synchronous failure: a quota 403 or a 429 without spending the dispatch budget, any other error with a counted attempt and a growing backoff, and the task fails as `dispatch_failed` only once that budget is spent. | #1318, #1310, #1347 |
| `database.scheduler_max_conns` (Helm `database.schedulerMaxConns`) | `0` | A pool of its own for the scheduler loop, its reapers and its janitors, opened in addition to the main pool, so API traffic cannot stall a tick. | #1339 |
| `database.statement_timeout_ms` (Helm `database.statementTimeoutMs`) | `0` | `statement_timeout` on the main pool, which serves the API; about `30000` bounds a runaway query. Set it together with the scheduler pool, or the scheduler shares the timeout. | #1339 |
| `database.conn_max_lifetime_jitter_ms` (Helm `database.connMaxLifetimeJitterMs`) | `0` | Random extra lifetime per pooled connection, so replicas started together do not reconnect at once. | #1339 |
| `executor.collect_settled_run_pods` (Helm `executor.collectSettledRunPods`) | `false` | Deletes a settled run's finished pods in one `DeleteCollection` instead of one delete per pod after the grace period; the chart grants `deletecollection` only when it is on. | #1353 |
| `executor.kube_client.qps`, `burst`, `maintenance_qps`, `maintenance_burst` (Helm `executor.kubeClient.*`) | `0` (client-go's 5 and 10, one shared client) | Rate limits of the dispatch client, and a separate client and token bucket for the informer, reconciler, reapers and GC, so maintenance cannot starve pod creation. | #1315 |
| `observability.metrics.drop_legacy_names` (`extraEnv`) | `false` | Serves each `dexaflow_*` family once, without its `leoflow_*` twin; halves the scrape. | #1321 |
| `ui.etag_revalidation` (`extraEnv`) | `false` | Lets the browser revalidate the grid's task summaries with `304 Not Modified`; every revalidation is still authenticated. | #1314 |
| `logs.sink.layout: segmented` (Helm `logs.sink.layout`) | `single` | The s3 and gcs sinks write each attempt as numbered segments, so a flush uploads only the open segment and the control plane holds one segment per attempt. See the requirement below. | #1336, #1443 |
| `logs.tail.publish: on_demand` (Helm `logs.tail.publish`) | `always` | Publishes live-tail lines only while someone follows the attempt, instead of one Redis round trip per line for every attempt. A follower that subscribes between two checks still receives every line that arrived after it subscribed, whatever the task's output rate, and can see its first live lines up to about a second late. | #1343, #1442 |
| `execution.warm_read_only_root_filesystem` (Helm `execution.warmReadOnlyRootFilesystem`) | `false` | A read-only root filesystem and a per-attempt `HOME` for warm workers, so nothing one attempt writes reaches the next; a task that writes outside `$HOME`, `$TMPDIR`, `/tmp` and `/dev/shm` fails with it on. See [Isolation between attempts](/operate/warm-pools/#isolation-between-attempts). | #1313 |
| Helm `goMemLimit.enabled` (`goMemLimit.percent`) | `false` (`80`) | Renders `GOMEMLIMIT` as a share of `resources.limits.memory`, so the GC works harder near the container limit instead of the pod being OOM-killed. See [Memory limit for the Go runtime](/operate/helm-chart/#memory-limit-for-the-go-runtime). | #1340 |

The segmented layout has one requirement. The sink finds where an
attempt's log ends by asking the store for a segment that should not exist,
so the store must answer a missing key with not-found; on S3 that takes
`s3:ListBucket` on the bucket, without which S3 answers `AccessDenied`. The
server checks this at startup and refuses to start with
`logs.sink.layout: segmented` when a missing key is not answered with
not-found, naming the permission to grant (#1443). Grant it before you turn
the layout on, and turn it on only once every replica runs 0.5.1, since an
older server reads only `{try}.log`. The on-demand tail keeps every line a
follower is entitled to: it checks for followers at most once a second, or
sooner when the lines held since the last check reach half of its 1024-line
or 1 MiB bound, so a follower of a chatty task receives every line that
arrived after it subscribed (#1442). Both settings are new in 0.5.1 and off
by default; an install that does not set them sees no change.

## Related issues

- #136 — this contract.
- #137 — `dexaflow lite backup` / `restore` commands.
- #60 / #61 — embed migrations + single binary (Lite distribution shape).
