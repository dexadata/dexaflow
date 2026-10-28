---
title: "Database lost, restored or repointed"
linkTitle: Database lost or replaced
weight: 52
description: "What a Dexaflow control plane does when its database is emptied, restored from a backup, failed over or repointed under it, on Helm and on Lite, and the command that recovers each case."
---

A control plane's database can change under a release that is already
installed: an `emptyDir` Postgres moves to another node, a volume is lost, a
backup is restored into a fresh instance, a failover lands on a lagging
replica, or `database.url` is changed. This page says what Dexaflow does in
each case and what to run. The design is
[ADR 0090](/project/adrs/0090-database-replaced-under-a-running-release/).

## What the control plane does

On a Cluster (Helm) install the control plane never crash-loops on its schema and
never migrates a database it does not recognize:

- **It waits instead of exiting.** A pod that finds the schema absent or
  behind stays up. `/healthz` answers 200, `/readyz` answers 503 with a fixed
  reason and the command to run, and every other request gets the same 503,
  so nothing is written. It checks again every 1 to 30 seconds and starts
  serving on its own once the schema is current.
- **It names the command for this release.** The chart passes the release
  name, namespace, chart version and `migrations.enabled` to the pod, so for
  a release `dexaflow` in the namespace `dexaflow` the reason reads:

  ```text
  schema absent: apply the migrations: `helm upgrade dexaflow oci://ghcr.io/dexadata/charts/dexaflow -n dexaflow --version {{< latest-version >}} --reuse-values` runs the migration Job again; the server checks again on its own
  ```

  With `migrations.enabled: false` it names your own migration step and the
  `dexaflow-migrate` image of the release instead. The same command is in the
  boot log line (`action` field) and, for a pod that was already serving, in
  its readiness log line.
- **It tells a replaced database from a fresh one.** The migration Job records
  an installation id in the database and in the `<fullname>-installation`
  ConfigMap (`<release>-dexaflow-installation`, or `<release>-installation`
  when the release name already contains `dexaflow`). A database with another
  id, with none, or with no schema at all is reported as `database replaced`:
  the Job migrates nothing and fails with exit code 8, and the control plane
  waits, not ready.
- **It restarts when the database is swapped under it.** A pod that was
  serving and finds a replaced database exits with code 3, so no cache built
  on the old database (tenant ids, DAG specs) survives. The restarted pod
  waits as above.
- **The wait is visible.** `dexaflow_schema_ready{reason}` is 0 while a pod
  waits, and the optional chart alert (`metrics.prometheusRule.enabled`) fires
  after 5 minutes, as critical for `database replaced`.

Find the reason with:

```bash
kubectl -n <namespace> get pods -l app.kubernetes.io/name=dexaflow    # Running, 0/1 ready
kubectl -n <namespace> logs deploy/<fullname> --tail=50 | grep 'database schema\|replaced'
kubectl -n <namespace> port-forward deploy/<fullname> 9090 &
curl -s localhost:9090/readyz
```

Each reason and its fix are in
[a control plane that is up but not ready](/operate/troubleshooting/#a-control-plane-that-is-up-but-not-ready).

## Helm: what happened and what to run

| What happened | What the release reports | What to run |
|---|---|---|
| The database was emptied: an `emptyDir` Postgres rescheduled, a volume lost or recreated | `database replaced: no schema`; the Job fails with exit code 8 | If a backup exists, restore it (next row). If the data is gone, start over on purpose: `helm upgrade <release> oci://ghcr.io/dexadata/charts/dexaflow -n <namespace> --version <chart version> --reuse-values --set installation.id=$(uuidgen)`. Then move Postgres to a persistent volume or a managed service. |
| A backup of this installation was restored, at the same version | ready again, nothing to do | Nothing: the backup carries the same installation id. A pod that was serving keeps serving; restart the pods if the restored tenants were recreated ([backup and restore](/operate/backup-restore/)). |
| A backup of this installation was restored, older than the release, or a failover landed on a lagging replica | `schema behind vN, requires vM` | The command the reason names: `helm upgrade <release> oci://ghcr.io/dexadata/charts/dexaflow -n <namespace> --version <chart version> --reuse-values`, which runs the migration Job again. |
| A backup taken before 0.5.6, which has no installation id, was restored | `database replaced: no installation id` | Accept it once with a new id: the same `helm upgrade` with `--set installation.id=$(uuidgen)`. |
| `database.url` now points at another installation's database, on purpose | `database replaced: another installation id` | Adopt it: the same `helm upgrade` with `--set installation.id=<the id it carries>` (`psql "$DATABASE_URL" -c 'SELECT id FROM installation'`). |
| `database.url` now points at the wrong database, by mistake | `database replaced: ...` | Point `database.url` back at the right database. The pods start once the id matches. |
| The release was uninstalled and installed again into the same namespace against an empty database | `database replaced: no schema` | `helm uninstall` leaves the ConfigMap in place. Accept the fresh start with a new id as above, or delete the ConfigMap before installing. |
| A migration was interrupted (a drained node, a cut connection) | `schema dirty at vN`, or boot stops with `schema is dirty at vN, the version this binary requires` | Follow the recovery table in [an interrupted migration](/operate/upgrades/#an-interrupted-migration), one row per migration file, then upgrade again. |
| The rollout ran before its migration Job, or the Job failed | `schema behind vN, requires vM`; new pods unready while the old ones keep serving | Fix what the Job's log names ([the migration Job](/operate/troubleshooting/#the-migration-job)), then run the same `helm upgrade`. |

Changing `installation.id` accepts one replacement: the Job migrates the
database, writes the id into it and into the ConfigMap, and the pods roll to
read it. The next replacement is refused again until the id is changed again.
The detail of each case is in
[a replaced database](/operate/troubleshooting/#a-replaced-database).

### With `migrations.enabled: false`

No Job runs, so no installation id is recorded and nothing is compared unless
your own migration step runs the `dexaflow-migrate` image with
`-installation-configmap`, `-installation-namespace` and, to accept a
replacement, `-installation-id`. Without that:

- An absent or behind schema waits as above, and the reason names your own
  migration step and the image tag to run (`up`).
- An emptied database is not told apart from a fresh one. With
  `migrations.onBoot: true` the control plane migrates it at boot and comes
  back empty and healthy, so alert on the data you expect, or record the id.
- A pod that was serving does not exit when the database is swapped. Restart
  the control plane after a restore or a repoint:
  `kubectl -n <namespace> rollout restart deployment -l app.kubernetes.io/instance=<release>`.

### With `migrations.onBoot: true`

The `all` role, or the scheduler in split mode, applies the embedded
migrations itself when the schema is absent or behind. It never migrates a
schema that is ahead or dirty, nor a database reported as `database replaced`,
so the table above is unchanged for an emptied or foreign database. It does
make the `schema behind` rows recover with no command.

## Lite

Lite owns its database: `dexaflow lite` runs on its own managed or Docker
Postgres, ignores `DEXAFLOW_DATABASE_URL` and `LEOFLOW_DATABASE_URL`, and
applies the embedded migrations before it starts the server. So it has no
installation id and no `database replaced` state, and repointing it is not a
case.

| What happened | What to run |
|---|---|
| The datastore under `~/.dexaflow` was deleted or lost | `dexaflow lite` starts on a new, empty database. To get the data back, stop it and restore a backup: `dexaflow lite restore --input <archive> --force`, then `dexaflow lite` ([backup and restore](/operate/backup-restore/#restore)). |
| A backup was restored, older than the binary | Nothing: `dexaflow lite` applies the missing migrations as it starts. An archive newer than the binary is refused by the restore. |
| A server run on its own reports `schema absent` or `schema behind` | `dexaflow db migrate`, or restart `dexaflow lite`. |
| A migration was interrupted | Follow [an interrupted migration](/operate/upgrades/#an-interrupted-migration), then start `dexaflow lite` again. |

## Avoid it

- Run Postgres on a persistent volume or a managed service. `helm install`
  and `helm upgrade` warn when `database.url` names an in-cluster Postgres
  Service.
- Back up on a schedule and run a [restore drill](/operate/backup-restore/#regular-restore-drills).
- Turn on `metrics.prometheusRule.enabled`, or the equivalent rule on
  `dexaflow_schema_ready`, so a pod that waits is noticed.
