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

- `~/.dexaflow/python/` (managed CPython) — re-fetched by `dexaflow setup` on the
  target machine if needed.
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
# Refuses to overwrite an existing ~/.dexaflow install:
dexaflow lite restore --input ~/snapshots/before-upgrade.tar.gz

# Use --force to overwrite explicitly (e.g. after `dexaflow uninstall`):
dexaflow lite restore --input ~/snapshots/before-upgrade.tar.gz --force
```

The restore command refuses, with a clear error, when:

1. **The archive's schema is newer than this binary supports** — refusing
   the restore is the inverse of the upgrade-time drift detector (see
   [Upgrades](/operate/upgrades/)). Loading rows into a DB the binary cannot read
   would corrupt them.
2. **`~/.dexaflow/` already holds an install** and `--force` is not set.
   Pass `--force` only after confirming you want to overwrite.
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
dexaflow lite restore --input ~/snap.tar.gz
dexaflow lite            # boots with the restored datastore + workspace
```

## Worked example: roll back a botched upgrade

```sh
# Before upgrading: take a snapshot.
dexaflow lite backup --output ~/snap-before-upgrade.tar.gz

# Upgrade (re-run install.sh, restart dexaflow lite). Something breaks.

# Wipe and restore. --purge removes the new install completely; restore
# refuses without it because ~/.dexaflow is non-empty after the upgrade.
dexaflow uninstall --purge
# Re-install the previous version's binaries via install.sh's pin, then:
dexaflow lite restore --input ~/snap-before-upgrade.tar.gz
dexaflow lite
```

## Pro (Pro)

The Helm-installed Pro control plane does **not** ship its own
backup/restore commands. Pro operators own the Postgres backup story
via standard tooling:

- Managed Postgres providers (RDS, Cloud SQL, etc.) offer point-in-time
  recovery and automated snapshots.
- Self-hosted Postgres: use `pg_dump` / `pg_dumpall` on a cron + an offsite
  archive (S3, GCS).
- Persistent volumes: capture via Velero or your cluster's volume snapshot
  controller.

The PR that hardens the Helm chart (#96) will add a `BACKUP.md` to the
chart README pointing at the upstream guidance.

## Related issues

- #137 — these commands.
- #136 — upgrade contract (the safety guard inside restore mirrors the
  drift detector at startup).
- #150 — CI smoke that exercises a full backup → upgrade → restore cycle
  end-to-end.
