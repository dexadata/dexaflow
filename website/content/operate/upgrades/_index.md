---
# --- AUTO redirect aliases (build_redirects.py): do not edit by hand ---
aliases:
  - /upgrades.html
# --- end AUTO redirect aliases ---
title: Upgrades
weight: 40
description: "Upgrade a Dexaflow control plane safely, edition by edition."
---

This page is the canonical answer to "I'm on `v0.x.y` and want to install a
newer tag: what happens to my state?" Before upgrading your cluster, your
database or a DAG's Python line, check the
[compatibility](/reference/compatibility/) page as well.

{{% alert title="Upgrade contract" color="info" %}}
The upgrade contract below is honored by the Lite edition; we test it on
every release. We will not knowingly ship a release that breaks it without a
clear migration note. On the `v0.x` line we do not yet promise
forward/backward compatibility across major versions; that is a v1
concern.
{{% /alert %}}

What changes in each release, and what to do before or after a `helm upgrade` to it, is in the [version notes](/operate/upgrades/version-notes/).

## Lite: what is preserved across upgrades

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
rows the older binary does not understand. Upgrade, or wipe, never both.

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

### Supported upgrade paths

You do not have to take every patch. One `helm upgrade` may skip any number
of releases, as long as it starts from a supported release:

| Upgrading to | Supported from, in one step |
|---|---|
| any 0.6.x | 0.5.0 or later |
| any 0.5.x | 0.5.0 or later |

Every migration in between then runs in the one migration Job, in order. The
[skip-version upgrade test](https://github.com/dexadata/dexaflow/blob/main/test/upgrade/skip-version.sh)
holds every release candidate to this, weekly and on each rc tag
([#2311](https://github.com/dexadata/dexaflow/issues/2311)): it seeds a
database at 0.5.0 with 1,000 DAGs, 1 million runs, 5 million task
instances, 500,000 archived tries and 1 million audit rows, upgrades it to
the candidate in one step, and checks that every migration finished within
5 minutes, that no index was left `INVALID`, that the schema equals a fresh
install's (`pg_dump --schema-only`), and that the control plane becomes
ready and runs a new DAG while it still serves the old runs.

Read the "Upgrading to" sections of every release you skip, in order: their
steps still apply, and the migrations of all of them run in that one Job, so
size its `activeDeadlineSeconds` and Helm's `--timeout` for their sum. Upgrade
from a release older than the table in two steps, through the oldest
supported one.

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
with the chart version (the [latest release](https://github.com/dexadata/dexaflow/releases)
tag with the leading `v` stripped).

### The migration Job's pod is not part of the control plane

The hook pod runs under its own application name,
`app.kubernetes.io/name: <chart name>-migrate`, with
`app.kubernetes.io/component: migrate`, deliberately *not* the control plane's
`app.kubernetes.io/name`. A Service selector matches every pod whose labels
contain it, so a hook pod carrying the control plane's own selector labels is
selected by the control-plane Service and PodDisruptionBudget for as long as it
runs, and a Job pod has no readiness probe: it counts as `Ready` from the instant
its container starts. On `helm upgrade` (the one path where those objects
already exist while the hook runs), that put a pod that serves nothing into the
control plane's endpoint set and into its disruption budget's healthy count.

Two consequences for your own tooling:

- **Do not select control-plane pods by `app.kubernetes.io/instance` alone.**
  That label is still on the hook pod on purpose, so
  `kubectl -n <ns> get pods -l app.kubernetes.io/instance=<release>` finds a
  wedged migration. Add `app.kubernetes.io/name=<chart name>` (or, in
  `split.enabled` installs, `app.kubernetes.io/component=api|scheduler`) when you
  mean the control plane.
- **With `networkPolicy.enabled`, the hook pod is governed by no policy**: on
  `helm install` and on `helm upgrade` alike. Egress is allow-all by default, so
  a default install is unaffected. If your namespace carries a default-deny
  policy from a platform team, give the migration pod its own egress allowance to
  Postgres (match on `app.kubernetes.io/component: migrate`), and create it
  outside the release or as a hook with a negative `helm.sh/hook-weight`: a
  policy the chart creates normally does not exist yet when the *pre-install*
  hook runs.

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

- #136: this contract.
- #137: `dexaflow lite backup` / `restore` commands.
- #60 / #61: embed migrations + single binary (Lite distribution shape).

<script>
// Sections of this page moved to their own pages: send an old link with
// one of their anchors to the page that now holds it.
(function(){var m={"an-interrupted-migration":"/operate/upgrades/version-notes/","new-opt-in-settings-in-051":"/operate/upgrades/version-notes/","new-opt-in-settings-in-052":"/operate/upgrades/version-notes/","rolling-back-051-to-050":"/operate/upgrades/version-notes/","three-unused-tables-are-dropped":"/operate/upgrades/version-notes/","upgrading-to-051-finish-the-rollout-promptly":"/operate/upgrades/version-notes/","upgrading-to-0510-a-trigger-at-max_active_runs-is-queued":"/operate/upgrades/version-notes/","upgrading-to-0510-max_active_tasks-is-compiled-per-dag-run":"/operate/upgrades/version-notes/","upgrading-to-0510-row-level-security-policies":"/operate/upgrades/version-notes/","upgrading-to-0510-the-install-profile":"/operate/upgrades/version-notes/","upgrading-to-0510-the-live-log-tail-is-published-on-demand":"/operate/upgrades/version-notes/","upgrading-to-052":"/operate/upgrades/version-notes/","upgrading-to-053":"/operate/upgrades/version-notes/","upgrading-to-054":"/operate/upgrades/version-notes/","upgrading-to-054-the-task-namespace-default":"/operate/upgrades/version-notes/","upgrading-to-055-three-unused-tables-are-no-longer-touched":"/operate/upgrades/version-notes/","upgrading-to-056":"/operate/upgrades/version-notes/","upgrading-to-057-malformed-placement-fields-are-refused":"/operate/upgrades/version-notes/","upgrading-to-057-platform-resource-defaults-fill-per-dimension":"/operate/upgrades/version-notes/","upgrading-to-058":"/operate/upgrades/version-notes/","upgrading-to-059":"/operate/upgrades/version-notes/","upgrading-to-059-the-ui-refreshes-every-5-seconds":"/operate/upgrades/version-notes/","what-changes-in-051-without-a-flag":"/operate/upgrades/version-notes/","what-changes-in-052-without-a-flag":"/operate/upgrades/version-notes/","what-changes-in-054-without-a-flag":"/operate/upgrades/version-notes/","what-the-051-migrations-do":"/operate/upgrades/version-notes/"};var a=decodeURIComponent(location.hash.slice(1));
if(m[a]&&!document.getElementById(a)){location.replace(m[a]+'#'+a);}})();
</script>
