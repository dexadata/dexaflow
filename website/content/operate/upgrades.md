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
| `~/.dexaflow/docker-compose.yaml` (the Docker Postgres definition of a binary-only install) | The next `dexaflow lite` replaces it with the one the new binary ships and prints `↻ refreshed the managed compose (binary upgrade)`, as long as nobody edited it (a checksum of what was written is kept in `docker-compose.yaml.sha256`). An edited file is kept as is: Lite writes the new one beside it as `docker-compose.yaml.new` and warns once; merge what you need, or delete `docker-compose.yaml` and the next `dexaflow lite` writes the new one. A file written before the checksum existed is treated as edited unless it already equals the new one. |

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

## Cluster: upgrade path

The Cluster control plane upgrades with the standard Helm flow: re-run
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
**drift detector** described above protects a Cluster control plane from being run
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

### Upgrading to 0.5.4: the task namespace default

Charts before 0.5.4 defaulted `taskNamespace` to the literal `leoflow`, whatever
namespace the release was installed in. A `helm install -n dexaflow` without
`--set taskNamespace=...` sent task pods, their staging PVCs, the executor Role
and RoleBinding and the agent CA ConfigMap to a `leoflow` namespace the operator
never chose (#1682).

From 0.5.4 an unset `taskNamespace` means the release namespace on a fresh
install. On `helm upgrade` an unset value keeps the namespace the release's
running control plane already uses: the chart reads it from the
`LEOFLOW_EXECUTOR_TASK_NAMESPACE` env of the live Deployment, so the upgrade
does not move task pods or their RBAC under running tasks. The chart's NOTES
print a line when this happens.

| Your install | What the upgrade does |
|---|---|
| Release namespace is `leoflow` | Nothing changes. |
| `taskNamespace` set explicitly (any value, `leoflow` included) | Nothing changes. |
| Another release namespace, `taskNamespace` unset | Keeps `leoflow`. Set `taskNamespace: leoflow` to pin it. |
| Rendered by `helm template`, ArgoCD or Flux, `taskNamespace` unset | The renderer cannot read the live release, so it renders the **release namespace**. Set `taskNamespace: leoflow` in your values **before** syncing 0.5.4 if your task pods run there today. |

To move task pods to the release namespace on purpose:

1. Let running tasks finish, or pause the DAGs that would start new ones.
2. `helm upgrade ... --set taskNamespace=<release namespace>`. Helm deletes the
   Role, RoleBinding and CA ConfigMap it had created in `leoflow` and creates
   them in the new namespace, and the control plane switches to it.
3. The control plane no longer watches `leoflow`, so it does not clean it up.
   List what is left with
   `kubectl -n leoflow get pods,pvc,configmaps -l leoflow.io/run-id` (warm-pool
   anchors carry `leoflow.io/warm-anchor=true`) and delete it once it has
   finished.

Installs that do not use the chart are unaffected: the server's own default for
`executor.task_namespace` (`DEXAFLOW_EXECUTOR_TASK_NAMESPACE` or
`LEOFLOW_EXECUTOR_TASK_NAMESPACE`) is still `leoflow`.

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

0.5.0 left the schema at migration 026. 0.5.1 applies 027 to 040. Nine of
them change indexes or the page layout of `task_instances`, which is what makes
this upgrade different from the column additions earlier releases shipped:
eight are `CREATE INDEX CONCURRENTLY` or `DROP INDEX CONCURRENTLY` statements
and one changes the table's `fillfactor`.

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
| `040_tenant_limits` | Adds the per-tenant limit columns and the daily run counter to `tenants`; every default means unlimited. | `ALTER TABLE` in a transaction |

**Where they run.** On Cluster, the chart's pre-install/pre-upgrade hook Job
(named `<release>-migrate`, or `<release>-dexaflow-migrate` when the release
name does not contain the chart name; its pod carries
`app.kubernetes.io/component: migrate`) runs
`dexaflow-migrate -path /migrations -database $(LEOFLOW_DATABASE_URL) up` with the
release's `ghcr.io/dexadata/dexaflow-migrate` image, which bundles the SQL
files with Dexaflow's wrapper over the golang-migrate library, before the new
control plane rolls out and while the old replicas keep serving (their
readiness check tolerates a migration in flight above their own schema
version). The Job has `backoffLimit: 3`, so a failed pod is retried three
times, except for a failure a retry cannot fix: a missing database, a refused
password, an unusable `database.url`, a missing `sslrootcert` file or a dirty
version exits with a code of its own, and on Kubernetes 1.31 or newer the
Job's `podFailurePolicy` fails it on that first pod. A failed Job is kept for
inspection, and its log names the cause and the value to change
([troubleshooting](/operate/troubleshooting/#the-migration-job) lists each
message):

```sh
kubectl -n <namespace> logs -l app.kubernetes.io/component=migrate --tail=50
```

A control plane that starts on a schema that is absent or behind, because the
Job failed, did not run (`migrations.enabled: false`) or the database lost its
schema later, does not crash-loop: it stays up and not ready, says why on
`/readyz` and in its log, and starts serving on its own once the migrations
have run, so a failed upgrade stalls on unready new pods while the old ones
keep serving
([a control plane that is up but not ready](/operate/troubleshooting/#a-control-plane-that-is-up-but-not-ready)).

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
  later run stops at it: the hook Job says `schema_migrations is dirty at
  version N: a migration was interrupted` and quotes golang-migrate's
  `Dirty database version N. Fix and force version.` on its second line, and
  fails without retrying on Kubernetes 1.31 or newer; `dexaflow lite` refuses
  to start with `database schema is marked dirty at version N`; a Cluster control
  plane refuses to boot against a version dirty at the one it requires, and
  waits, not ready, on one dirty at any other version, since a migration may
  be in flight there, while the replicas that were already serving keep
  serving.
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
| 036, 039, 040, 041 (columns) | 36, 39, 40, 41 | nothing: `ADD COLUMN IF NOT EXISTS`, and 039's backfill, can be repeated | 35, 38, 39, 40 |
| 037 (role reconcile) | 37 | nothing: one transaction, rolled back, and the file is idempotent | 36 |
| 042 (`pool_slots`) | 42 | nothing: the column and its `NOT VALID` check are added in one transaction with a 5 s `lock_timeout`, which rolled back if it was interrupted; if the closing `VALIDATE CONSTRAINT` was, both are there, and every statement of the file can be repeated | 41; retry when the table is quiet |
| 043 (index build) | 43 | `DROP INDEX CONCURRENTLY IF EXISTS idx_dag_runs_tenant_settled_ended;` | 42 |
| 044 (deployer role, API tokens) | 44 | nothing: one transaction, rolled back, and the file is idempotent | 43 |
| 045 (device sign-ins) | 45 | nothing: one transaction with a 5 s `lock_timeout`, rolled back, and the file is idempotent | 44 |
| 046 (`build_info`) | 46 | nothing: one transaction with a 5 s `lock_timeout`, rolled back, so the column is not there yet | 45; retry when the table is quiet |
| 047 (`removed` task state) | 47 | nothing: one transaction, rolled back, so the value is not there yet, and `ADD VALUE IF NOT EXISTS` can be repeated | 46 |
| 048 (`dispatch_unavailable_since`) | 48 | nothing: one transaction with a 5 s `lock_timeout`, rolled back, so the column is not there yet | 47; retry when the table is quiet |
| 049 (`installation`) | 49 | nothing: one transaction, rolled back, so the table is not there yet, and the file is idempotent | 48 |
| 050 (index build) | 50 | `DROP INDEX CONCURRENTLY IF EXISTS idx_audit_resource;` | 49 |
| 051 (unwritten tables) | 51 | nothing: one transaction with a 5 s `lock_timeout`, rolled back, so the tables are still there | 50; retry when `task_instances` is quiet |
| 052 (tenant suspension) | 52 | nothing: one transaction with a 5 s `lock_timeout`, rolled back, and every statement can be repeated | 51 |
| 053 (`next_run_at`) | 53 | nothing: one transaction with a 5 s `lock_timeout`, rolled back, so the columns are not there yet | 52; retry when `dags` is quiet |
| 054 (index build) | 54 | `DROP INDEX CONCURRENTLY IF EXISTS idx_dags_next_run;` | 53 |
| 055 (usage events) | 55 | nothing: one transaction with a 5 s `lock_timeout`, rolled back, and every statement can be repeated | 54; retry when `task_instances` is quiet |

**How to run `migrate force`.** The hook image is the `dexaflow-migrate`
wrapper plus the SQL files at `/migrations`; it takes the golang-migrate CLI's
`version`, `force`, `goto` and `up` with the same flags, so the commands this
page and the migration files name all run in it. From a
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

**Cluster.** `helm rollback <release> <revision>` rolls the chart and the images
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
and fails on a database at 040 with `no migration found for version 40`. If
you must use `helm upgrade`, pass `--set migrations.enabled=false` or run the
down migration first. What the section above says about a rollback (the logs
0.5.1 wrote, the tasks 0.5.1 dispatched) applies either way.

**Lite.** `dexaflow lite` 0.5.0 refuses to start against a database above 026
(`database is at schema version 40 but this binary only knows up to 26`), and
no `dexaflow` command runs a down migration. The supported path is the
snapshot from [How to test an upgrade safely](#how-to-test-an-upgrade-safely-recommended):
`dexaflow uninstall --purge`, reinstall 0.5.0, `dexaflow lite restore`. The
backup page has the
[worked example](/operate/backup-restore/#worked-example-roll-back-a-botched-upgrade).

**Restoring the 0.5.0 schema.** Where you need the exact 0.5.0 schema back
(Lite without a snapshot, or Cluster before a `helm upgrade` to 0.5.0 with the
hook on), run the down migrations from the highest migration your 0.5.1 build
carries (040) to 026, with the CLI or
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
and the tenant limit columns of 040, so limits an operator set are lost
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
- **A token that names no tenant is refused** (#1363): the authenticator
  answers `401` at login, on every request and on renewal when a token
  trusted from its signed claims (the local dev token, or a server with no
  user store) carries no tenant, instead of serving the `default` tenant's
  data. Users loaded from the database always carry their tenant and are not
  affected. If you mint tokens yourself, include the `tenant_id`
  claim.
- **DAG JSON Schemas resolve only their own references** (#1385): a param
  schema and a task's `xcom_schema` may `$ref` into themselves
  (`#/$defs/...`) and the standard JSON Schema meta-schemas, nothing else. A
  `file://` reference, or a relative one, now fails the registration, the
  trigger or the XCom push with a schema error instead of reading a file on
  the control plane. See
  [Run parameters](/author-dags/dag-authoring/#run-parameters-and-their-schemas).
- **Clearing a task restores its retries** (#1412): a clear sets the task's
  retry budget from the `retries` of the version the re-run executes, resets
  its infra re-placements, and deletes the XCom of the attempts it clears, all
  in one transaction. A cleared task with `retries: 3` gets three retries
  again instead of none, and `on_failure_callback` fires only on its final
  attempt. See [Clearing a task](/author-dags/dag-authoring/#clearing-a-task).
- **A very long log line is split instead of ending the log** (#1338): a line
  longer than just under 4 MiB (progress bars, binary dumps) is sent in pieces
  cut on a UTF-8 boundary, with a warning naming the number of extra lines.
  Before, it ended the attempt's log stream and every later line was lost.
- **The chart opens fewer idle Postgres connections** (#1426): the chart's
  `database.maxIdleConns` default drops from 5 to 2, which is what each pod
  opens at boot, so two replicas fit a small managed Postgres; the
  `database.maxOpenConns` ceiling stays at 20. When the estimated boot or peak
  connection demand passes 20, `helm install` and `helm upgrade` print it in
  their notes; a default single replica install prints nothing.
  If you pinned `database.maxIdleConns` yourself, your value is kept. See
  [Troubleshooting](/operate/troubleshooting/) for SQLSTATE 53300.
- **Python 3.10 and 3.11 task images ship a newer setuptools** (#1410): 80.10.2
  instead of 79.0.1, to clear two HIGH image findings. A task whose
  dependencies import `pkg_resources` now logs one `UserWarning` line, and a
  warnings filter that turns `UserWarning` into an error fails on it.
- **Offset pages of DAG runs are stable** (#1352): runs that share a logical
  date are now ordered by run id as well, so paging with `offset` no longer
  repeats or skips one of them. The response body is unchanged.

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

0.5.1 also adds features that change nothing until you use them:

| Feature | How to use it | PR |
|---|---|---|
| Keyset paging for DAG runs and the event log | Pass the opaque `cursor` query parameter instead of `offset`; each page with a successor names the next cursor in the `Dexaflow-Next-Cursor` response header. See [Paging](/reference/api/#paging). | #1352 |
| Read-only pools for tenants | `server.pools_read_only` (Helm `config.poolsReadOnly`): tenant roles can no longer write pools, and a task that names a pool its tenant has not defined is confined to the tenant's `default_pool`. | #1383 |
| A tenant's default pool size | `default_pool_slots` on `PUT /api/v2/service/tenants/{tenant}` sizes a new tenant's `default_pool` and re-sizes it on a later call. | #1364 |
| Per-tenant limits | `max_dags` and the other limit fields on `PUT /api/v2/service/tenants/{tenant}` cap what a tenant registers and schedules; existing tenants stay unlimited (migration 040). See [Operator service API](/reference/configuration/#operator-service-api). | #1421 |
| A guard against private alert destinations | `scheduler.alerts.block_private_destinations`, with `scheduler.alerts.allowed_cidrs` for ranges you trust. See [Alert destinations](/reference/configuration/#alert-destinations). | #1362 |

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

### Upgrading to 0.5.2

0.5.2 applies one migration, `041_tenant_max_task_pool_slots`. It adds
`tenants.max_task_pool_slots` (`INTEGER NOT NULL DEFAULT 0`, with a `>= 0`
check). The default is a constant, so Postgres records it without rewriting the
table, and existing tenants get `0`, which is unlimited. On a database with
about one million task instances the migration took under 100 ms in the
release review, and its down about 20 ms.

**Rolling back 0.5.2 to 0.5.1.** On Cluster, `helm rollback <release> <revision>`
is supported and leaves the schema at 041: the 0.5.1 control plane boots
against it with the warning `database schema is ahead of this binary;
proceeding` and never reads the new column. As with 0.5.1, prefer it to a
`helm upgrade` that points at chart 0.5.1, whose migration Job fails with
`no migration found for version 41`; if you must use that upgrade, pass
`--set migrations.enabled=false` or run the down migration first. On Lite,
`dexaflow lite` 0.5.1 refuses a database above 040, so the way back is the
snapshot from [How to test an upgrade safely](#how-to-test-an-upgrade-safely-recommended),
or the down migration with the 0.5.2 CLI or migrate image:

```sh
migrate -path migrations -database "$DATABASE_URL" goto 40
```

The down drops the column, so any `max_task_pool_slots` an operator set is lost
and every tenant is unlimited again. Rows in every table are preserved.

### What changes in 0.5.2 without a flag

- **A DAG can no longer set `leoflow.io/` labels or annotations on its pods**
  (#1376). `dexaflow.yaml` validation, `dexaflow compile` and registration
  reject such a key, and the executor drops and logs any that reach it from a
  DAG registered earlier. Move a custom key to a prefix of your own before you
  upgrade.
- **An attempt that outlives `auth.max_attempt_credential_lifetime` fails
  instead of re-running** (#1461). Before, it was failed as `agent_lost` and
  re-placed with a fresh credential without using a retry; now it fails with
  `credential_ceiling: ...` as a task failure and its retry policy applies.
  Only tasks that run past the ceiling plus the 10 minute token TTL see this.
- **Migrated Airflow DAGs with `pool_slots` above 1 become weighted** (#1467).
  The next push of such a DAG makes each of those tasks take that many slots
  of its pool, so a pool sized for task count admits fewer of them at once.
  DAGs that do not set `pool_slots` plan exactly as before. Cluster only.
- **Error messages no longer leak local detail.** A failed alert names its
  endpoint by scheme and host only, without the webhook URL (#1370), and a
  tenant schema error no longer names the server's working directory (#1402).

### New opt-in settings in 0.5.2

Every setting below is off or neutral by default; the
[configuration reference](/reference/configuration/) has the full entry for
each. Together they implement
[ADR 0066](/project/adrs/0066-weighted-pool-slots-and-resource-unit/), weighted
pool slots and a resource unit (#1466).

| Setting | Default | What it does when set | PR |
|---|---|---|---|
| `size` in `dexaflow.yaml` (`tasks.<id>.size`, `defaults.size`), or `pool_slots` in `dag.py` | `1` | How many slots of its pool a task takes while queued or running. A task that does not fit waits for enough free slots and never fails for it. | #1467 |
| `executor.unit.cpu` and `executor.unit.memory` (Helm `executor.unit`), with `executor.unit.enforce` and `executor.unit.max_size` | unset, `refuse`, `64` | Sizes a task pod as `pool_slots x unit` and refuses a task that declares more. Roll it out with `enforce: warn` first and watch `dexaflow_unit_misfit_total`. | #1481 |
| `scheduler.pool_starvation_threshold` (Helm `config.scheduler.poolStarvationThreshold`) | `60s` | Reserves a pool for a task of more than one slot that has waited longer than this, so smaller tasks cannot keep it out. Pools without sized tasks are never reserved. `0s` disables it. | #1482 |
| `max_task_pool_slots` on `PUT /api/v2/service/tenants/{tenant}` | `0` (unlimited) | Refuses at registration, with a 403, a task whose size is above the tenant's limit. Uses migration 041. | #1483 |

### Upgrading to 0.5.3

0.5.3 applies one migration, `042_task_instance_pool_slots`. It adds
`task_instances.pool_slots` (`INTEGER NOT NULL DEFAULT 1`) so the Pools screen
and `/api/v2/pools` count the slots a sized task takes, the same weight the
admission gate charges (#1499). The default is a constant, so Postgres records
it without rewriting the table. The `>= 1` check is added `NOT VALID` and then
validated in its own transaction under a lock that blocks neither readers nor
writers. Every task instance created before the upgrade reads as one slot, so
the sized tasks of a run that started before the upgrade are under-reported on
the Pools screen until they settle or are cleared. Scheduling is not affected.

The migration waits at most 5 seconds for its lock. If a long transaction holds
`task_instances` longer than that, the migration rolls back, golang-migrate marks
version 42 dirty and nothing changed: run `migrate force 41` and retry. Every
statement can be repeated.

**Rolling back 0.5.3 to 0.5.2.** On Cluster, `helm rollback <release> <revision>`
is supported and leaves the schema at 042: the 0.5.2 control plane boots
against it with the warning `database schema is ahead of this binary;
proceeding`, never reads the new column, and its inserts get the default. As
with earlier patches, prefer it to a `helm upgrade` that points at chart 0.5.2,
whose migration Job fails with `no migration found for version 42`; if you must
use that upgrade, pass `--set migrations.enabled=false` or run the down
migration first. On Lite, `dexaflow lite` 0.5.2 refuses a database above 041,
so the way back is the snapshot from
[How to test an upgrade safely](#how-to-test-an-upgrade-safely-recommended),
or the down migration with the 0.5.3 CLI or migrate image:

```sh
migrate -path migrations -database "$DATABASE_URL" goto 41
```

The down drops the column and its check. No other data changes.

### Upgrading to 0.5.4

0.5.4 applies four migrations, 043 to 046.

- **`043_dag_runs_settled_ended_index`** builds the index the retention janitor
  reads, on `dag_runs (tenant_id, ended_at)` for runs in `success` or
  `failed`. It is built `CONCURRENTLY`: reads and writes on `dag_runs` keep
  going during the build, which took about 0.2 s on 100,000 runs. The build
  first waits for every transaction already open in the database to finish,
  whatever table it touches, with no time limit, and the hook waits with it
  (see "Why the hook can take longer than usual" under
  [What the 0.5.1 migrations do](#what-the-051-migrations-do), including
  Helm's `--timeout`). During the build, autovacuum skips `dag_runs`, and a
  manual `VACUUM`, `ANALYZE` or schema change on it waits. Upgrade when no long
  transaction is open in the database. If the build is interrupted, recover it
  as the table in [An interrupted migration](#an-interrupted-migration) says.
- **`044_deployer_role_and_api_tokens`** runs in one transaction, which took
  about 40 ms. It adds the `deploy:dag` permission, the built-in `deployer`
  role in every tenant, and the `api_tokens` table. The role has no holders,
  so no account gains anything on upgrade. A tenant that already has a custom
  role named `deployer` keeps it as it is. Creating `api_tokens` briefly takes
  a lock on `tenants` and `users` that blocks writes to them (not reads), with
  no `lock_timeout`. Every statement can be repeated.
- **`045_cli_device_authorizations`** runs in one transaction, which took
  about 20 ms. It creates the `cli_device_authorizations` table, where the
  device sign-ins of `dexaflow auth login --device` wait for approval
  ([From a machine with no browser](/operate/sso-google-workspace/#from-a-machine-with-no-browser)).
  The table is new, so no existing row changes. Its foreign keys briefly take
  a lock on `tenants` and `users` that blocks writes to them (not reads); the
  migration waits at most 5 seconds for it. If it waits longer, the
  transaction rolls back, golang-migrate marks version 45 dirty and nothing
  changed: run `migrate force 44` and retry. Every statement can be repeated.
- **`046_dag_versions_build_info`** runs in one transaction, which took about
  10 ms. It adds the nullable `dag_versions.build_info` column, where the
  register records where a version was built
  ([Where a version was built](/operate/cicd-deploy/#where-a-version-was-built)).
  The column has no default, so Postgres adds it to the catalog without
  rewriting or scanning `dag_versions`, and every version registered before
  the upgrade reads as having no build info. Adding it takes an `ACCESS
  EXCLUSIVE` lock on `dag_versions`, which waits for every open transaction
  that touched the table, and queries on the table queue behind it while it
  waits. The migration waits at most 5 seconds for it. If it waits longer,
  the transaction rolls back, golang-migrate marks version 46 dirty and
  nothing changed: run `migrate force 45` and retry when the table is quiet.
  The statement can be repeated.

**Rolling back 0.5.4 to 0.5.3.** On Cluster, `helm rollback <release> <revision>`
is supported and leaves the schema at 046: the 0.5.3 control plane boots
against it with the warning `database schema is ahead of this binary;
proceeding`. It ignores the new index, the `deployer` role, the API tokens,
the device sign-ins and the `build_info` column, so tokens created on 0.5.4
stop authenticating until you move forward again, a device sign-in in
progress cannot finish, and the versions it registers record no build info
(its inserts leave the column out and get `NULL`).
As with earlier patches, prefer it to a `helm upgrade` that points at chart
0.5.3, whose migration Job fails with `no migration found for version 46`; if
you must use that upgrade, pass `--set migrations.enabled=false` or run the
down migrations first. On Lite, `dexaflow lite` 0.5.3 refuses a database above
042, so the way back is the snapshot from
[How to test an upgrade safely](#how-to-test-an-upgrade-safely-recommended),
or the down migrations with the 0.5.4 CLI or migrate image:

```sh
migrate -path migrations -database "$DATABASE_URL" goto 42
```

The downs run from 046 to 043. The down of 046 drops `dag_versions.build_info`,
so the build info recorded for every version is lost; the register audit
entries keep a copy. The down of 045 drops `cli_device_authorizations`, so
every device sign-in in progress is lost and has to be started again with
`dexaflow auth login --device`; no token is kept there. The down of 044 drops
`api_tokens`, which revokes every API token, deletes the built-in `deployer`
roles with their user assignments, and deletes the `deploy:dag` permission, so
a custom role granted it loses that grant. The down of 043 drops the index. No
other data changes.

**Encrypting Variables is opt-in** (#507). With the default
`variable_encryption: off`, 0.5.4 stores Variables exactly as 0.5.3 does, so a
rolling upgrade and a rollback need nothing for them. Encryption needs no
migration:
an encrypted value lives in the same `value` column, marked by the
`dexaflow:enc:v1:` prefix, and every 0.5.4 mode reads both forms.

To encrypt them, once every control plane replica runs 0.5.4 (an older replica
would hand the ciphertext to a task):

1. Make sure `DEXAFLOW_SECRET_KEY` is set. Without it the server refuses to
   start with `variable_encryption` set to `on` or `decrypt`.
2. Set `DEXAFLOW_VARIABLE_ENCRYPTION=on` (Helm: `extraEnv`) and restart. Each
   replica encrypts the plaintext rows it finds at startup and logs
   `stored variables encrypted with the secret key` with the count. Replicas
   that start together are safe: each rewrite is guarded on the value it read.

**Rolling back to 0.5.3 after turning it on.** 0.5.3 cannot read an encrypted
Variable: it delivers the ciphertext to tasks as the value. Before you roll
back:

1. Set `DEXAFLOW_VARIABLE_ENCRYPTION=decrypt` on 0.5.4 and restart every
   replica. Startup turns every encrypted Variable back into plaintext and logs
   `stored variables restored to plaintext`; new writes are plaintext.
2. Check that nothing is left: this returns `0`.

   ```sql
   SELECT count(*) FROM variables WHERE starts_with(value, 'dexaflow:enc:v1:');
   ```

   A row no configured key opens is left encrypted and logged at `ERROR`;
   add the missing key to `DEXAFLOW_SECRET_KEY` and restart, or set that
   Variable again.
3. Roll back as usual. A value 0.5.3 writes is plaintext, which 0.5.4 reads as
   is, so moving forward again needs nothing either.

Setting `variable_encryption` back to `off` stops encrypting new writes but
leaves encrypted rows encrypted and readable by 0.5.4; only `decrypt` makes
them readable by 0.5.3. A backup taken while it is on holds ciphertext: restore
it with the same key, as for connections.

### What changes in 0.5.4 without a flag

**New DAGs start paused** (#1705). 0.5.4 follows Airflow: a DAG registered for
the first time starts paused, so its schedule creates no run, catchup
included, until someone unpauses it. A manual trigger on a paused DAG is
accepted and its run waits in `queued` until the unpause (see the next item).
Changing a
default in a patch is a deliberate exception to the patch rule
([ADR 0068](/project/adrs/0068-patch-content-gated-by-safety/),
`helm/AGENTS.md`), decided in #1705.

DAGs that already exist keep their state, and a redeploy never pauses or
unpauses a DAG, so the upgrade itself changes nothing that is running. Three
cases do change:

- **A DAG deleted and deployed again comes back paused**, because it is
  registered for the first time again.
- **During a rolling upgrade**, a replica still on 0.5.3 registers new DAGs
  active until every replica runs 0.5.4.
- **A DAG that already passes `is_paused_upon_creation`** compiles into a
  `dag.json` that now carries it, so its spec hash changes. Redeploying it
  under the same `--dag-version` is refused with 409; deploy it under a new
  `--dag-version` (a new commit does this on its own).

A pipeline that deploys a new DAG and expects its schedule to start has three
ways back to the 0.5.3 behavior:

- unpause the DAG after its first deploy (UI switch, or
  `dexaflow admin dags unpause <dag_id>`);
- set `is_paused_upon_creation=False` on that DAG;
- set `DEXAFLOW_DAGS_ARE_PAUSED_AT_CREATION=false` on the server to register
  every new DAG active. On Helm, set the chart value
  `config.dagsArePausedAtCreation: false` rather than `extraEnv`: the chart
  already renders this variable from that value.

`dexaflow lite` keeps registering new DAGs active. Rolling back to 0.5.3 needs
nothing: 0.5.3 ignores the setting and the spec field.

**Runs of a paused DAG wait in `queued`** (#1705). As in Airflow 3, the
scheduler no longer starts a queued run while its DAG is paused, whoever
created it: a manual trigger (UI, `POST /api/v2/dags/{id}/dagRuns`,
`dexaflow deploy --trigger`, `dexaflow runs trigger`) or a clear that
re-queues the run. The API is unchanged and still answers with the queued
run. Unpausing the DAG (UI switch or `dexaflow admin dags unpause <dag_id>`)
starts the waiting runs from the next scheduler tick, as `max_active_runs`
allows (see the next item). Runs in `running` keep going when
the DAG is paused. A run that a clear put back in `queued` waits like any
queued run, even while some of its tasks run: those tasks finish, and nothing
downstream or retried starts until the unpause. In 0.5.3 a manual trigger on a
paused DAG started at once, so a pipeline that deploys a new DAG and triggers
it now has to unpause it too (or register it active, as above). Runs queued
on a paused DAG at the moment of the upgrade wait from then on. Rolling back
to 0.5.3 starts them.

**Queued runs start under `max_active_runs`** (#1766). As in Airflow, the
scheduler moves a queued run to `running` only while fewer than
`max_active_runs` runs of its DAG are running, oldest logical date first
(close to Airflow's `run_after` order); the rest stay `queued` with nothing
dispatched until a slot frees. In 0.5.3 the cap applied only when the schedule
or a trigger created a run, so every run re-queued by a clear ran at once,
past the cap. A run that a clear re-queued now shows `running` again once it
starts, with a new `start_date`; in 0.5.3 it stayed `queued`, with no start
date, while its tasks ran.
`PATCH /api/v2/dags/{dag_id}/dagRuns/{run_id}` with `"state": "running"` is
still accepted but now stores `queued` (the response shows it, and the audit
entry records `requested_state: running`), so the scheduler starts the run
under the pause and the cap; on a run already `running` it still changes
nothing. A PATCH to `queued` also clears the run's start date. Airflow 3.2 accepts only `queued`,
`success` and `failed`; a script that sets `running` should send `queued`,
since a later minor release may refuse `running`. Rolling back to 0.5.3 needs
no migration, but 0.5.3 has no start cap and no paused hold: every queued run,
the backlog the cap held included, starts on its first tick.

**A new install's bootstrap admin is `admin@dexaflow.local`** (#1709). A first
`dexaflow setup` records `admin_email: "admin@dexaflow.local"`, and a server
started with a bootstrap password (`bootstrap.password` on Helm,
`DEXAFLOW_BOOTSTRAP_PASSWORD` or `DEXAFLOW_BOOTSTRAP_PASSWORD_HASH`) and no
`DEXAFLOW_BOOTSTRAP_EMAIL` creates `admin@dexaflow.local` on an empty database.
An install created before keeps `admin@leoflow.local` and logs in as before. The
user is never renamed and no second admin is created:

- Lite keeps the `admin_email` its `config.yaml` records.
- At every start, the server resolves the default address to
  `admin@leoflow.local` when that user exists and `admin@dexaflow.local` does
  not, or when only `admin@leoflow.local` holds the admin role, reconciles
  that account's password as before, and logs `bootstrap admin kept its
  pre-rename address`. When both addresses have a user it also logs a warning
  naming both. This covers Helm and
  docker-compose installs, which never set the email, and a Lite reinstall over
  a kept datastore: `dexaflow setup` then prints `admin@dexaflow.local`, but
  the account that logs in with the new password is `admin@leoflow.local`, and
  the `dexaflow lite` ready banner names it. `dexaflow lite reset-password`
  resets that account and records it in `config.yaml`.
- Any other `DEXAFLOW_BOOTSTRAP_EMAIL` (or `LEOFLOW_BOOTSTRAP_EMAIL`) is used as
  given.
- `auth.oidc.breakGlassEmails` must keep naming the address your admin has. The
  chart's [Google SSO example](https://github.com/dexadata/dexaflow/blob/main/helm/dexaflow/examples/values-oidc-google.yaml)
  now lists `admin@dexaflow.local`, which has no password login on an older
  database: list `admin@leoflow.local` there instead.

To move an existing install to the new address, create `admin@dexaflow.local`
with the admin role (`dexaflow auth create-user --role admin`); from the next
start the bootstrap reconciles that account, and `admin@leoflow.local` stays as
an ordinary account. On Lite, also set `admin_email` in `config.yaml`. Rolling
back to 0.5.3 needs nothing on an existing install or on Lite, whose
`config.yaml` names the address. A server install created by 0.5.4 that sets no
bootstrap email gets a second admin, `admin@leoflow.local`, from the same
bootstrap password when it starts on 0.5.3.

### Upgrading to 0.5.5: three unused tables are no longer touched

`scheduler_loops`, `replicas` and `task_state_history` (migrations 003 and
004) were never written by any release (#1186). 0.5.5 stops touching them:
the retention janitor no longer deletes from `task_state_history`, so
`dexaflow_retention_rows_deleted_total` and `dexaflow_retention_rows_eligible`
no longer have a `table="task_state_history"` series. The tables stay in the
schema, and 0.5.6 drops them in a migration. A 0.5.5 control plane runs
against either schema, so it keeps working through that migration and after a
rollback from 0.5.6. Upgrading to 0.5.5 and rolling back to 0.5.4 need nothing
for this change.

0.5.5 also applies one migration, 047.

- **`047_removed_task_state`** adds the `removed` task state of
  [ADR 0086](/project/adrs/0086-clear-rebinds-a-finished-run-with-its-task-set/)
  (`ALTER TYPE ... ADD VALUE`, catalog only). Its down migration is a no-op:
  the value stays. On Lite, the previous `dexaflow lite` refuses a database
  above its own schema, so before rolling back to 0.5.4 run
  `migrate -path migrations -database "$DATABASE_URL" goto 46` with the 0.5.5
  CLI or migrate image; the file says what the previous release does with a
  `removed` task instance.

### Upgrading to 0.5.6

0.5.6 applies two migrations, 048 and 049.

- **`048_task_instance_dispatch_unavailable_since`** runs in one transaction,
  which took about 7 ms on a `task_instances` table of 1,000,000 rows. It adds
  the nullable `task_instances.dispatch_unavailable_since` column, where the
  scheduler keeps when the Kubernetes API became unavailable to a task it is
  dispatching
  ([A pod create that fails](/operate/scheduler-resilience/#a-pod-create-that-fails)).
  The column has no default, so Postgres adds it to the catalog without
  rewriting or scanning `task_instances`, and every existing row reads as not
  waiting on an outage. Adding it takes an `ACCESS EXCLUSIVE` lock on
  `task_instances`, which waits for every open transaction that touched the
  table, and queries on the table queue behind it while it waits. The
  migration waits at most 5 seconds for it. If it waits longer, the
  transaction rolls back, golang-migrate marks version 48 dirty and nothing
  changed: run `migrate force 47` and retry when the table is quiet. The
  statement can be repeated.
- **`049_installation`** creates the one-row `installation` table of
  [ADR 0090](/project/adrs/0090-database-replaced-under-a-running-release/),
  a new table that locks nothing else. The SQL leaves it empty: on Cluster the
  migration Job writes the release's installation id into it after the
  migrations, and into a ConfigMap, `<release>-installation`, that it creates
  in the release namespace. On the upgrade to 0.5.6 the Job records an id for
  the existing installation once; from then on a database that carries
  another id, none, or no schema is refused as replaced instead of migrated
  ([a replaced database](/operate/troubleshooting/#a-replaced-database)). The
  Job now runs as its own ServiceAccount, `<release>-migrate`, with a Role on
  that ConfigMap (rendered with `rbac.create`); with your own RBAC, grant it
  before upgrading. On Lite the table stays empty and nothing is compared.

**Rolling back 0.5.6.** On Cluster, `helm rollback <release> <revision>` leaves
the schema at 049, and the previous control plane boots against it with the
warning `database schema is ahead of this binary; proceeding`: it never reads
or writes the column or the `installation` table. The installation ConfigMap
stays, so upgrading again finds the same id in the database and the
ConfigMap. On Lite, the previous `dexaflow lite` refuses a database
above its own schema, so run the down migration first, with the 0.5.6 CLI or
migrate image (it took about 8 ms on the same table):

```sh
migrate -path migrations -database "$DATABASE_URL" goto 47
```

That rolls back 049 and 048. A task that was
waiting on an unavailable Kubernetes API at that moment falls back onto the
dispatch-attempt budget of the previous release. No other data changes.

### Upgrading to 0.5.7: platform resource defaults fill per dimension

`executor.defaults.resources.cpu` and `executor.defaults.resources.memory` used
to apply only to a task that declared no `resources` at all. A task that
declared any part, for example only an `ephemeral_storage` limit or only a cpu
request, got neither default and ran with no request or limit on the
dimensions it left out. From 0.5.7 the control plane fills each of cpu, memory
and ephemeral storage that a task leaves empty (neither request nor limit) with
the platform default; a dimension the task sets at all is still entirely the
task's ([#802](https://github.com/dexadata/dexaflow/issues/802)).

Only clusters that set these defaults are affected, and only tasks that
declare part of their resources. Those tasks now request more than before when
they are next dispatched, which can leave them `Pending` on a cluster with no
headroom. Before upgrading, find them: a task whose `resources` in `dag.json`
names cpu but not memory, or the other way round, or only
`ephemeral_storage`. Give the dimensions they leave out an explicit value if
the platform default is too large for them. Nothing changes for a task with a
resource unit (`executor.unit`), which sizes every task as before. Rolling
back to 0.5.6 restores the old behavior with no other step.

### Upgrading to 0.5.7: malformed placement fields are refused

A task's `tolerations`, `affinity`, `topology_spread_constraints`,
`resource_claims` and `resources.claims` used to be accepted as written and
decoded only when the pod was built, where an entry with a misspelled key or a
value of the wrong type was dropped and the task ran without it. From 0.5.7
registration checks them against the Kubernetes types and refuses the DAG with
`400`, naming the task and the field
([#628](https://github.com/dexadata/dexaflow/issues/628),
[placement allowlist](/operate/placement-allowlist/)).

Versions registered before the upgrade keep running as they are. The next push
of a DAG with such an entry, `dexaflow deploy` or a reload in `dexaflow dev`,
is refused until it is fixed. Keys use the Kubernetes spelling, so
`toleration_seconds` or `max_skew` written in `dexaflow.yaml` or `dag.py` are
now errors: write `tolerationSeconds` and `maxSkew`. Before upgrading, look
through the placement fields of your DAGs for keys that do not use the
Kubernetes spelling. Rolling back to 0.5.6 restores the old behavior with no
other step.

### Upgrading to 0.5.8

0.5.8 applies six migrations, 050 to 055.

- **`050_audit_log_resource_index`** builds `idx_audit_resource` on
  `audit_log (tenant_id, resource_id, occurred_at DESC, id DESC)`, the index the
  event log filtered to one DAG reads; without it each page, and the count
  beside it, read every audit entry of the tenant
  ([#1612](https://github.com/dexadata/dexaflow/issues/1612)). It is built
  `CONCURRENTLY`: reads and writes on `audit_log` keep going during the build,
  which took about 0.15 s on 200,000 entries. Like 043, the build first waits
  for every transaction already open in the database to finish, with no time
  limit, and the hook waits with it (see "Why the hook can take longer than
  usual" under [What the 0.5.1 migrations do](#what-the-051-migrations-do)).
  If the build is interrupted, recover it as the table in
  [An interrupted migration](#an-interrupted-migration) says.
- **`051_drop_unwritten_tables`** drops `scheduler_loops`, `replicas` and
  `task_state_history`, described below.
- **`052_tenant_suspension`** adds `suspended_at`, `suspension_mode` and
  `suspension_reason` to `tenants`, for the service API's tenant suspension
  ([#2181](https://github.com/dexadata/dexaflow/issues/2181)). The columns are
  empty or have a constant default, so nothing is rewritten; the transaction
  waits at most 5 seconds for its lock on `tenants`. Previous releases never
  read the columns and keep running against the schema, but they do not
  honor a suspension: suspend a tenant only once every replica runs 0.5.8.
- **`053_dag_next_run_at`** adds `next_run_at` and `schedule_epoch` to `dags`,
  and **`054_dag_next_run_at_index`** builds `idx_dags_next_run` on it
  `CONCURRENTLY` ([#2175](https://github.com/dexadata/dexaflow/issues/2175)).
  The scheduler stores when each scheduled DAG is next due, at most an hour
  ahead, and each tick lists only the DAGs that may be due instead of every
  scheduled DAG. Nothing is backfilled: the first tick of a 0.5.8 leader
  lists every scheduled DAG once, as before, and stores their times. 053
  waits at most 5 seconds for its lock on `dags`; 054, like 050, first waits
  for every open transaction to finish. Previous releases never read the
  columns. During a rolling upgrade, a version registered or a run deleted
  through a 0.5.7 replica does not clear the stored time, so the change
  reaches the scheduler within the hour instead of on the next tick.
- **`055_usage_events`** creates `usage_events`, the switch row
  `usage_events_switch` and a trigger on `task_instances` that writes one
  event per finished task attempt
  ([ADR 0096](/project/adrs/0096-task-attempt-usage-events/),
  [#2174](https://github.com/dexadata/dexaflow/issues/2174)). The switch
  starts off and stays off until `usage.events.enabled` is on, so the
  trigger writes nothing for an install that does not use it. Creating the
  trigger takes a lock on `task_instances` that blocks writes to it (not
  reads) for an instant, after waiting at most 5 seconds for the
  transactions already open on the table. Previous releases never read the
  tables and keep running against the schema; with the switch on, their
  writes are recorded too, because the database writes the events.

#### Three unused tables are dropped

Migration **`051_drop_unwritten_tables`** drops `scheduler_loops`, `replicas`
and `task_state_history`, with their indexes, sequences and the
`trim_scheduler_loops()` function (#1186). They were created by migrations 003
and 004 for observability and audit, and no Dexaflow release ever wrote a row
to them: leader election uses an advisory lock, the scheduler tick is measured
by `dexaflow_scheduler_loop_duration_seconds`, and a task instance's attempts
are in `task_instance_history`. Since 0.5.5 no release reads them either. On
an install nobody touched by hand the three tables are empty and nothing is
lost. **Any row someone inserted into them by hand is deleted by the
upgrade**; to keep it, copy it out first. This shows whether there is
anything to copy:

```sql
SELECT (SELECT count(*) FROM task_state_history) AS task_state_history,
       (SELECT count(*) FROM scheduler_loops)    AS scheduler_loops,
       (SELECT count(*) FROM replicas)           AS replicas;
```

The migration runs in one transaction, which took about 5 ms of SQL (40 ms
with the CLI) on a database with 200,000 task instances: the tables are empty,
so the drop does not depend on the size of the database. Dropping
`task_state_history` removes its foreign key from `task_instances`, which
takes an `ACCESS EXCLUSIVE` lock on `task_instances` until the transaction
commits. The lock waits for every open transaction that touched the table,
and reads and writes of task instances queue behind it while it waits. The
migration waits at most 5 seconds for it. If it waits longer, the transaction
rolls back, golang-migrate marks version 51 dirty and nothing changed: run
`migrate force 50` and retry when the table is quiet. Every statement can be
repeated.

**While old replicas still run.** 0.5.5 and later never read or write the
three tables, so their replicas keep working during the rollout. Upgrading
straight from 0.5.4 is different: the 0.5.4 retention janitor deletes from
`task_state_history`, so with `retention.dag_runs_days` set, a 0.5.4 leader
running against the migrated schema fails every retention cycle that finds an
expired run. It logs `retention cycle` at `ERROR` with
`relation "task_state_history" does not exist`, deletes no run, and stops
that cycle before the audit log too. Nothing is lost, and the cycle succeeds
once a 0.5.8 replica leads. With retention off (the default) 0.5.4 never reads
the three tables.

**Rolling back 0.5.8.** On Cluster, `helm rollback <release> <revision>` leaves the
schema at 055, and the previous control plane boots against it with the
warning `database schema is ahead of this binary; proceeding`; it reads the
event log through the new index without knowing it, never touches the three
tables, and ignores tenant suspensions: a suspended tenant gets runs again
until 0.5.8 is back. If usage events were on, the trigger keeps recording,
and nothing turns the switch off until 0.5.8 leads again. On Lite, run the down migrations first, with the 0.5.8 CLI or
migrate image:

```sh
migrate -path migrations -database "$DATABASE_URL" goto 49
```

That drops the usage events and their trigger, the next due times and their
index, drops the suspension columns
(every suspended tenant runs again),
recreates the three tables, empty, exactly as before, and drops the index
`CONCURRENTLY`. Recreating the tables takes a lock on `task_instances`
that blocks writes to it (not reads) for its few milliseconds, waiting at most
5 seconds as the up does; it took about 15 ms of SQL (50 ms with the CLI) on
the same database. Rows deleted by the upgrade do not come back. Rolling back
further, to 0.5.4 with `retention.dag_runs_days` set, needs that down
migration too, so its retention finds `task_state_history` again.

## Ephemeral database loss (emptyDir Postgres)

When evaluating Dexaflow with an in-cluster Postgres running on `emptyDir`
storage (such as in local clusters or evaluation setups), the datastore has no
persistence guarantees. Any pod recreation, node reboot, node drain, or
Spot/preemptible reclamation destroys the database volume completely.

Since 0.5.6 a Cluster release notices it
([ADR 0090](/project/adrs/0090-database-replaced-under-a-running-release/)):
the database no longer carries the release's installation id, so the migration
Job refuses to migrate it, a booting control plane waits not ready, and one that
was serving exits and restarts into that wait
([a replaced database](/operate/troubleshooting/#a-replaced-database)). Nothing
recreates an empty schema in its place, and nothing brings the data back: only
a restore from a backup of this installation does.

`helm install` and `helm upgrade` print a warning in the release notes when
`database.url` points at an in-cluster Postgres Service, since the chart cannot
see whether that Postgres has a volume.

To prevent data loss:
- Never run production control planes against `emptyDir` datastores.
- For in-cluster Postgres, always back the deployment with a
  PersistentVolumeClaim (PVC).
- For production environments, point `database.url` at managed services such as
  AWS RDS, GCP Cloud SQL, or Azure Database for PostgreSQL.
- Regularly exercise database backup and restore drills (see
  [Backup and restore](/operate/backup-restore/#regular-restore-drills)).

## Related issues

- #136 — this contract.
- #137 — `dexaflow lite backup` / `restore` commands.
- #60 / #61 — embed migrations + single binary (Lite distribution shape).
