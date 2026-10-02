---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /upgrades.html
# --- end AUTO redirect aliases ---
title: Upgrades
weight: 40
description: "Upgrade a Leoflow control plane safely, edition by edition."
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

Reinstalling (running the new `install.sh`, or `brew upgrade leoflow` once
that ships) over an existing Lite install **preserves all of these by
default**:

| What | Where | Notes |
|---|---|---|
| **Workspace** | The path under `workspace:` in `~/.dexaflow/config.yaml` (default `~/leoflow`) | Your `dag.py`, `dexaflow.yaml`, and any other project files. The installer does not touch this directory. |
| **Datastore** | `~/.dexaflow/managed-postgres/data/` (managed Postgres) **or** the `leoflow-data-*` Docker volume (Docker Postgres) | Includes DAG history, runs, task instances, XCom, Variables, Connections. The new binary applies any pending SQL migrations on first start. |
| **Admin login** | `~/.dexaflow/config.yaml` (`admin_email`, `admin_password_hash`) | Your password is not regenerated. Use `leoflow lite reset-password` if you forgot it. |
| **JWT signing secret** | `~/.dexaflow/config.yaml` (`jwt_secret`) | Browser sessions survive the upgrade (no forced re-login). |
| **Parser + runtime venv** | `~/.dexaflow/venv/` | Project dependencies are reinstalled lazily as needed (the marker at `~/.dexaflow/venv/.leoflow-deps` triggers a refresh when the project's deps change). |

## What changes

| What | Why |
|---|---|
| The `leoflow` / `leoflow-server` / `leoflow-agent` binaries on `PATH` | Replaced by `install.sh`. |
| `~/.dexaflow/python/` (managed CPython) | Pinned per release; replaced if the new release pins a different version. |
| The SQL schema | The new binary applies any missing migrations on first start. |

## Drift detection

If you somehow run an **older** `leoflow` binary against a database a **newer**
binary has already migrated, the older binary refuses to start with:

```
database is at schema version 18 but this binary only knows up to 15;
an older `leoflow` is being run against a newer database.
Upgrade the binary, or run `leoflow uninstall --purge` to start over
(this WIPES your data)
```

This is the safe behavior: continuing with a stale schema would corrupt
rows the older binary does not understand. Upgrade, or wipe — never both.

## Fresh start

If you want a clean slate without the prior history:

```sh
leoflow uninstall --purge
```

`--purge` removes the binaries, `~/.dexaflow/` (config + datastore + parser
sources), and the workspace directory. Without `--purge`, uninstall keeps the
datastore and workspace so a future reinstall picks up where you left off
(this is also the contract upgrades rely on).

## How to test an upgrade safely (recommended)

Before installing a newer tag on a Lite install you depend on:

1. **Back up first.** See [Backup and restore](/operate/backup-restore/):
   ```sh
   leoflow lite backup --output ~/snap-before-upgrade.tar.gz
   ```
2. Install the new version. The drift detector protects you from the worst
   downgrade case.
3. If anything looks off, restore from the tarball.

## Pro — upgrade path

The Pro control plane upgrades with the standard Helm flow: re-run
`helm upgrade` against the same release, pointing the pinned image tag at the
newer version.

```sh
# OCI chart (the primary install path — see Installation):
helm upgrade leoflow oci://ghcr.io/dexadata/charts/leoflow --version <VERSION> \
  -n leoflow --reset-then-reuse-values

# Or pin the image tags explicitly:
helm upgrade leoflow oci://ghcr.io/dexadata/charts/leoflow --version <VERSION> \
  -n leoflow --reset-then-reuse-values \
  --set image.tag=<VERSION> \
  --set migrations.image.tag=<VERSION>
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
`database.url`) before the new `leoflow-server` rolls out, so the schema is
brought to parity before any new binary serves traffic. The same startup
**drift detector** described above protects a Pro control plane from being run
against a database a newer binary already migrated. Use `--version <VERSION>`
with the chart version — the [latest release](https://github.com/dexadata/leoflow/releases)
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

## Related issues

- #136 — this contract.
- #137 — `leoflow lite backup` / `restore` commands.
- #60 / #61 — embed migrations + single binary (Lite distribution shape).
