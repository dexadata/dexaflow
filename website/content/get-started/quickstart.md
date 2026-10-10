---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /quickstart.html
# --- end AUTO redirect aliases ---
title: Quickstart
weight: 10
description: Get Dexaflow Lite running locally in two commands — the fastest path to a running DAG.
---

Get Dexaflow **Lite** running locally in two commands. Lite is the local edition
(Pro is chart-installable and in validation — see [Operating modes](/concepts/editions/));
run it on your machine or a trusted internal network — see [Editions](/concepts/editions/).

## Prerequisites

- **One of the following** for the datastores:
    - **Docker** running (preferred — Lite spins up `postgres:16` automatically), OR
    - **Nothing** — Lite falls back to an embedded managed Postgres downloaded
      under `~/.dexaflow` (no Docker, no system Postgres needed). See
      [Editions § Datastores](/concepts/editions/) for the auto-selection logic.
- Linux or macOS (incl. WSL2). No system Python needed: Lite installs a managed
  one. See [Installation](/get-started/installation/) for details.
- **Outbound network on the first `dexaflow setup`, unless you already have
  `python3.11`.** Setup needs that exact version and downloads a managed CPython
  3.11 under `~/.dexaflow/python` when it is not on your `PATH`. A newer host
  Python is not a substitute here: 3.12 and 3.13 can parse DAGs, which is why
  `dexaflow doctor` reports them as present, but setup still fetches its own 3.11.
  Behind a proxy or offline, install `python3.11` first and setup will use it.

## 1 · Install

```bash
curl -fsSL https://raw.githubusercontent.com/dexadata/dexaflow/main/install.sh | sh
```

This installs the binaries to a directory on your `PATH` (e.g. `/usr/local/bin`)
and runs the **setup wizard**, which asks a few questions — press Enter to accept
each `[default]`:

- **Where your DAGs live** (workspace) — default `~/dexaflow`
- **How tasks run** — `subprocess` (each task as a process on this machine — simple,
  no Docker, recommended) or `k8s` (real pod-per-task on a local mini-Kubernetes —
  mirrors Pro, needs Docker). The wizard also accepts the friendly aliases `local`
  (= `subprocess`) and `cluster` (= `k8s`).
- **Admin email** — default `admin@leoflow.local`

It then fetches a managed Python + the editor, creates your workspace, and prints
your **admin password once** (`dexaflow lite reset-password` resets it):

```
── Dexaflow Lite admin (save this — it is shown only once) ──
  user:     admin@leoflow.local
  password: tiger98
```

(Piping non-interactively, or re-running with an existing config, skips the
questions and uses the defaults — which are always printed, so you are never
guessing.) If the installer added a PATH line to your shell profile, open a new
terminal (or `source` it); installing to `/usr/local/bin` needs no reload.

## 2 · Run it

```bash
dexaflow lite
```

That's it. With no arguments, `dexaflow lite`:

1. scaffolds a starter DAG in your workspace (if it has none yet),
2. brings up Postgres — Docker `postgres:16` when Docker is present, else an embedded managed Postgres (no Redis required, see [Editions](/concepts/editions/#dexaflow-lite)),
3. starts the control plane and prints where to go:

```
✓ Dexaflow Lite is ready
    open:    http://127.0.0.1:8088
    login:   admin@leoflow.local
    project: /home/you/leoflow
```

Open that URL, log in with the password from step 1, and the DAG shows up in the
Airflow-compatible UI. `dexaflow lite` keeps running and **hot-reloads** on every
save — press Ctrl-C to stop.

{{% alert title="Open it from another machine (internal network / VPN)" color="success" %}}
Lite binds loopback by default. To reach it from your LAN, add `--host
0.0.0.0` — the banner then prints your machine's IP
(`http://192.168.x.y:8088`). It is honored only with a login configured, and
Lite is for trusted networks only — never the public internet.
{{% /alert %}}

## 3 · Trigger your first run

`dexaflow lite` scaffolds a starter DAG on first boot, so there is something to run
right away.

1. Open **http://127.0.0.1:8088** and log in — the scaffolded DAG is listed on the
   **Dags** page.

    ![The Dags list with the scaffolded DAG](/assets/screenshots/dev-dags.png)

2. Click the DAG to open it, then press **▶ Trigger** (top-right) to start a run.
3. Watch the **grid** fill in — each task goes yellow (running), then green
   (success). A fully green column is a successful run.

    ![A green grid after a successful run, with the run-duration overview](/assets/screenshots/dev-grid-tasks.png)

That is the whole loop: author, trigger, watch it go green.

## 4 · Edit your DAG

Click the **`< >` IDE** button (bottom-right of the UI) to open the built-in web
editor — file tree + Python/YAML highlighting. Edit `dag.py`, save, and the DAG
reloads in a couple of seconds. (Reload the browser tab to see structure
changes; run state auto-refreshes.) Details: [the Lite web editor](/author-dags/lite-web-editor/).

## Useful commands

```bash
dexaflow doctor                       # check platform, deps, and what's achievable
dexaflow lite --host 0.0.0.0          # reachable from your internal network
dexaflow lite reset-password     # set a new admin password (after first run)
dexaflow uninstall                    # remove the install (--purge for workspace + volumes)
```

## Next

- [DAG authoring](/author-dags/dag-authoring/) — the dialect, `dexaflow.yaml`, overrides.
- [The `dexaflow lite` workflow](/contribute/local-dev-loop/) — the edit→reload loop, executors.
- [CI/CD & deploy examples](/operate/cicd-deploy/) — ship it.
- [Editions](/concepts/editions/) — Lite (now) vs Pro (in validation).
