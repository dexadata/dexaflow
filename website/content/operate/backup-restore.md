---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /backup-restore.html
# --- end AUTO redirect aliases ---
title: "Backup & restore"
weight: 50
description: "Back up and restore Dexaflow state — metadata, secrets, and logs."
---

Lite ships two commands that snapshot and re-load the whole install in one
portable file: `dexaflow lite backup` and `dexaflow lite restore`. Use them to
migrate to another machine, survive an OS reinstall, or roll back a botched
upgrade.

{{% alert title="Archive format" color="info" %}}
The archive format is documented as `manifest_version: 1`. We will not
silently break it, but we may add fields. A future binary will refuse an
older archive only if `manifest_version` changes — a pure-version mismatch
on `leoflow_version` is fine.
{{% /alert %}}

## What is included

A backup archive (`leoflow-backup-<timestamp>.tar.gz`) contains:

| File / dir | Contents |
|---|---|
| `MANIFEST.json` | Format version, `leoflow_version`, embedded schema version, Postgres version, `created_at` |
| `config.yaml` | The admin email + password hash, JWT signing secret, the key that decrypts your stored connection secrets (`secret_key`), parser command, workspace path |
| `setup.json` | Setup metadata (Python interpreter, OS/arch) |
| `datastore.sql` | A logical `pg_dump` (--clean --if-exists, plain SQL) of the managed Postgres — DAGs, runs, task instances, XCom, Variables, Connections |
| `workspace/` | Your project tree (DAGs, `dexaflow.yaml`, etc.). VCS dirs and virtualenvs are excluded (see below) |

What is **not** included:

- `~/.dexaflow/python/` (managed CPython): re-fetched on the target machine if
  needed: by `dexaflow setup` when it has no Python 3.11 to 3.13, or by
  `dexaflow lite` when a DAG declares 3.11 and the host has none.
- `~/.dexaflow/postgres/` (managed PG binaries) — same.
- `~/.dexaflow/venv/` (parser/runtime venv) — re-installed lazily.
- VCS metadata (`.git`, `.hg`, `.svn`).
- Build artifacts (`.venv`, `venv`, `__pycache__`, `.pytest_cache`,
  `node_modules`, `.tox`, `.mypy_cache`).

The trade-off: backups are small and portable (the heavy stuff is what the
binary can fetch back), and they will not silently leak `.git/` history a
user committed locally but did not push.

## Backup

```sh
# Default: leoflow-backup-<UTC-timestamp>.tar.gz in the current directory.
dexaflow lite backup

# Custom output path:
dexaflow lite backup --output ~/snapshots/before-upgrade.tar.gz
```

`backup` requires Lite to be running (it talks to the managed Postgres via
its socket to capture a consistent dump). Run `dexaflow lite` in another
terminal first.

## Restore

```sh
# Refuses to overwrite an install that already has a datastore:
dexaflow lite restore --input ~/snapshots/before-upgrade.tar.gz

# Use --force to overwrite it explicitly:
dexaflow lite restore --input ~/snapshots/before-upgrade.tar.gz --force
```

Lite does not need to be running, and does not need to have run before:
`restore` provisions the managed Postgres itself, through the same path
`dexaflow lite` uses (download it on first use, initialize its data dir,
start it on its socket, create the Lite database). If this restore started
the cluster, it stops it when it finishes; a cluster that was already running
is left running. It logs each phase:

```
▸ decoding manifest from /home/me/snap.tar.gz …
▸ provisioning the managed Postgres …
▸ applying the datastore dump via psql …
▸ restoring workspace to /home/me/dexaflow …
✓ restore complete; run `dexaflow lite` to start.
```

The archive always holds a managed-Postgres datastore, since `backup` covers
that path only. The restore pins the install to the managed Postgres, so a
plain `dexaflow lite` starts the restored data even on a machine where Docker
runs ([ADR 0094](/project/adrs/0094-lite-auto-follows-the-pinned-datastore/)).
To move the install to the Docker Postgres later, run
`dexaflow lite --postgres docker` once; that starts an empty Docker datastore
and pins the install to it.

The restore command refuses, with a clear error, when:

1. **The archive's schema is newer than this binary supports** — refusing
   the restore is the inverse of the upgrade-time drift detector (see
   [Upgrades](/operate/upgrades/)). Loading rows into a DB the binary cannot read
   would corrupt them.
2. **`~/.dexaflow/` already holds a datastore** (the managed
   `~/.dexaflow/pgdata`, or a Docker datastore a Lite run used) and `--force`
   is not set. Pass `--force` only after confirming you want to overwrite. A
   home that only `dexaflow setup` filled has no datastore, so a restore on a
   new machine needs no `--force`; the config it replaces is kept as
   `config.yaml.pre-restore` (below).
3. **The archive's `MANIFEST.json` is missing** or carries a `manifest_version`
   newer than this binary understands.

`--force` does **not** silence the schema-drift refusal. Corruption is not
opt-in.

The datastore is replayed **first**, and `config.yaml` is written only once
the replay succeeded. A replay that fails (disk full, a schema mismatch)
leaves your current `config.yaml` in place, so the key that opens the
unchanged datastore is still recorded. The config a restore replaces is kept
as `~/.dexaflow/config.yaml.pre-restore` (mode `0600`) and the restore prints
its path. It is removed only once a scan that covers every datastore on disk
finds every stored secret under the keys the restored config records:

- With one datastore, the next `dexaflow lite` that starts and whose boot scan
  is clean removes it. A boot that finds secrets the restored keys do not open
  keeps it, since it may hold the key they need.
- With both a managed and a Docker datastore, a boot scans only the one it runs
  against, so it keeps the file and says so. A `dexaflow lite migrate-key` that
  finishes cleanly scans both and removes it.

The archive's `config.yaml` is written as it is. A restore never adds or
removes an encryption key on Lite's behalf: an archive taken before an install
had a key of its own restores as an install on the key published in this
repository, and `dexaflow lite` says so and names
[`dexaflow lite migrate-key`](/reference/cli/dexaflow_lite_migrate-key/).
Run it to finish the move.

## Worked example: migrate to a new machine

```sh
# On the source machine (Lite running):
dexaflow lite backup --output /tmp/snap.tar.gz
scp /tmp/snap.tar.gz user@new-host:~/

# On the new machine, after `curl ... install.sh`:
dexaflow setup           # provisions managed Python + binaries
dexaflow lite restore --input ~/snap.tar.gz   # provisions the managed Postgres, then replays
dexaflow lite            # boots with the restored datastore + workspace
```

If Docker runs on the new machine, start with `dexaflow lite --postgres
managed` instead (see [Restore](#restore)).

## Worked example: roll back a botched upgrade

```sh
# Before upgrading: take a snapshot.
dexaflow lite backup --output ~/snap-before-upgrade.tar.gz

# Upgrade (re-run install.sh, restart dexaflow lite). Something breaks.

# Wipe and restore. --purge removes the new install completely; restore
# refuses without it because ~/.dexaflow still holds the upgraded datastore.
dexaflow uninstall --purge
# Re-install the previous version's binaries via install.sh's pin, then:
dexaflow lite restore --input ~/snap-before-upgrade.tar.gz
dexaflow lite
```

## Cluster (Helm)

The Helm-installed Cluster control plane does **not** ship its own
backup and restore commands: its state lives in services you run or rent, and
each is backed up with that service's own tooling. What matters is knowing
every piece of state, what losing each one costs, and which targets the
[production profile](/operate/production-profile/) sets for them.

### What state exists

| State | Holds | Losing it costs |
|---|---|---|
| **Postgres** | Everything durable: DAGs and versions, runs, task instances, users, roles, tokens, connections and variables (encrypted), pools, the audit log, the XCom index, the installation id | The installation. Without a backup it is a fresh install. |
| **Redis** | XCom values (7 day TTL) and the live log tail ([Redis requirements](/operate/redis/)) | XCom values: runs in flight whose next task pulls one fail; finished runs show no XCom. Nothing durable. |
| **Log bucket** | Task logs written by the object sink | The logs of past attempts. Runs and their states are unaffected. |
| **`secretKey`** | The AES-256 key that encrypts connection passwords and extras at rest ([ADR 0019](/project/adrs/0019-secret-encryption-at-rest/)) | Every stored connection secret: a restored database stays unreadable without the key it was written with. Back it up with the database. |
| **`auth.jwtSecret`** | The HMAC key that signs API and agent tokens | Every issued token. A new secret signs everyone out, and the agent tokens of running task pods stop being accepted; no stored data is lost. |
| **Agent CA** (`agentTLS`) | The CA task pods use to verify the control plane | A new CA (auto-generated or yours) makes running task pods distrust the control plane until they finish; new pods get the new bundle. |

The installation id is part of Postgres and also lives in the
`<fullname>-installation` ConfigMap; a restore of the same installation brings
the matching id back ([database lost, restored or repointed](/operate/database-lost-restored-repointed/)).

### Recommended targets

| State | RPO | RTO | Provider settings that meet them |
|---|---|---|---|
| Postgres | 5 minutes | 1 hour | Point-in-time recovery with a retention of at least 7 days, automated daily snapshots, a standby in another zone. RDS, Cloud SQL and Azure Database archive WAL continuously; the RPO is the archive lag. |
| Redis | about 1 second | 15 minutes | AOF with `appendfsync everysec`, or the provider's replication with persistence and automatic failover. XCom is short lived, so a daily snapshot is enough to recover from a lost instance. |
| Log bucket | 0 for written objects | none | Bucket versioning on, a lifecycle rule that expires old versions, and the bucket in a region (or dual region) that outlives the cluster. |
| Secrets | 0 | 15 minutes | Kept outside the cluster in a secret manager and referenced by name (`secretKeyExistingSecret`, `auth.existingSecret`), never only in a `values.yaml` on one machine. |

The restore drill below measures the Postgres RTO: run it at least once per
release you deploy and keep the time it took next to the target.

### Restore

**Postgres, from a snapshot or a point in time.** Restore into a new instance
(the provider's restore never overwrites the running one), then point the
release at it with `helm upgrade --reuse-values --set database.url=...`. The
restored database carries the installation id of this installation, so the
control plane accepts it. Restart the control plane pods afterwards
(`kubectl rollout restart deployment`): each pod caches tenant ids for its
lifetime, and a restored database whose tenants carry different ids would
otherwise be queried with the old ones. A restored database from another
installation, or one older than 0.5.6, is refused until you accept it, as
[database lost, restored or repointed](/operate/database-lost-restored-repointed/)
shows.

**Postgres restored to a point older than Redis.** Runs and task instances go
back to that point; XCom values written after it stay in Redis under runs the
database no longer knows and expire with their TTL. Attempts that were running
at the restore point come back as running with no pod behind them, and the
scheduler fails or retries them as lost tasks after the settling grace. Nothing
needs to be done by hand beyond the restart above.

**Redis restored to a point older than Postgres, or flushed.** The database is
intact. XCom values pushed after the Redis snapshot are missing: a run in
flight whose next task pulls one fails that task. Clear the upstream task of
each failed run so it pushes again, or rerun the run.

**The log bucket.** Restore the objects (or a previous version of them) under
the same keys; the UI reads them again with no restart. Missing logs only show
as missing in the UI.

**A lost `secretKey`.** There is no recovery for the secrets it encrypted. Set
a new key, then set every connection password and extra again
(`dexaflow connections set`).

What the control plane reports after a restore, a failover or a changed
`database.url`, and the command that recovers each case, is on
[database lost, restored or repointed](/operate/database-lost-restored-repointed/).

## Regular restore drills

A backup that has never been restored does not count as a backup. Restore a
recent backup into a scratch environment on a schedule and check that:

1. The archive or dump is complete and restores without errors.
2. The control plane boots on it and `/readyz` returns 200.
3. An admin can sign in and sees the DAGs and runs you expect.

The repository runs the same drill every week and on every release candidate
(`.github/workflows/restore-drill.yaml`, which runs
`test/e2e/restore-drill-e2e.sh`).

### Lite restore drill

`dexaflow lite restore --force` replaces `~/.dexaflow`, so run the drill on
another machine or in a throwaway container or VM, never on the install you
backed up.

```sh
# On the install you protect: take a fresh backup.
dexaflow lite backup --output /tmp/drill-snapshot.tar.gz

# On the scratch machine: restore it and start Lite.
dexaflow lite restore --input /tmp/drill-snapshot.tar.gz --force
dexaflow lite &
for _ in $(seq 1 60); do curl -fsS http://localhost:8080/readyz && break; sleep 1; done
```

### Cluster (Helm) restore drill

Restore into a separate database and point a throwaway release at it, so the
production release never sees the drill. Use your own values file, so the
release has the same `secretKey` and can decrypt the restored connections.

```sh
# Dump the production database and restore it into an empty scratch database.
pg_dump --clean --if-exists -d "$DATABASE_URL" > /tmp/drill-backup.sql
psql "$DRILL_DATABASE_URL" -v ON_ERROR_STOP=1 < /tmp/drill-backup.sql

# Pause every DAG in the copy, so the drill release schedules nothing.
psql "$DRILL_DATABASE_URL" -c "UPDATE dags SET is_paused = true"

# Start a throwaway release on the restored database and check it is ready.
kubectl create namespace dexaflow-drill
helm install drill oci://ghcr.io/dexadata/charts/dexaflow -n dexaflow-drill \
  -f values.yaml --set fullnameOverride=dexaflow-drill \
  --set database.url="$DRILL_DATABASE_URL" --wait
kubectl -n dexaflow-drill port-forward svc/dexaflow-drill 8081:8080 &
curl -fsS http://127.0.0.1:8081/readyz

# Clean up.
helm uninstall drill -n dexaflow-drill
kubectl delete namespace dexaflow-drill
```

## Related issues

- #137 — these commands.
- #136 — upgrade contract (the safety guard inside restore mirrors the
  drift detector at startup).
- #150 — CI smoke that exercises a full backup → upgrade → restore cycle
  end-to-end.
