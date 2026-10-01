# Leoflow Lite bundle

One-shot install script + curated DAG bundle for the Leoflow Lite hands-on
validation. The installer wraps the canonical [`install.sh`](../install.sh)
with the extra steps a fresh box needs to be productive: `leoflow setup`,
the sample DAGs, and a credentials block. Works on any Linux host (VMs,
containers, bare metal) — the alpha-validation flow runs against this same
bundle.

## Single command

```bash
curl -fsSL https://raw.githubusercontent.com/dexadata/leoflow/main/bundle/install.sh | bash
```

The script will:

1. Detect Linux/arch and delegate to the canonical `install.sh` to download
   the latest pre-release `leoflow` binary into `~/.local/bin/`.
2. Run `leoflow setup` — which **generates the admin password and prints it
   once in cyan**. SAVE IT.
3. Drop the curated DAG bundle into `~/leoflow/` (the workspace).
4. Print the credentials block + start command.

After it finishes, run:

```bash
leoflow lite
```

…and open `http://localhost:8088` in your browser. If you're on a remote
host (SSH, VM, container) expose the UI with `leoflow lite --host 0.0.0.0`.

## What's bundled

| DAG | Schedule | What it exercises |
|---|---|---|
| `recurring_print` | `*/3 * * * *` | scheduler tick loop, dispatch happy path |
| `recurring_parallel` | `*/5 * * * *` | parallel fan-out under steady load |
| `lifecycle` | manual | 3-task ETL with XCom |
| `taskflow_sales` | manual | TaskFlow patterns |
| `ml_hparam_search` | manual | **Map-reduce for ML** — parallel hyperparameter search ➜ pick best |
| `fan_out_aggregate` | manual | map-reduce — fan-out + aggregate |
| `montecarlo_pi` | manual | map-reduce — parallel Monte Carlo π |

Connector DAGs (postgres / mysql / mssql / sqlite / redis / http) are NOT
bundled by default — they need a Connection set up first via the UI
(Admin → Connections). Copy them from `examples/<name>/` after configuring.

## Credentials

`leoflow setup` generates a per-instance password and prints it ONCE during
install. There is no way to recover that exact password later — but there's
no need to: `sudo leoflow lite reset-password` writes a new one and prints
it the same way.

Default email is `admin@leoflow.local` (override at install time with
`leoflow setup --admin-email you@example.com`; the bundle script doesn't
do that — re-run `setup` after if you want a different email).

## What to look for during a hands-on validation

The point is to **catch bugs that survived all the unit/integration tests**.
Specifically:

- Are recurring runs steady? Run for 1 h, count completed runs of
  `recurring_print` — expect 20. Any gaps, stuck `queued` states, missed
  cron slots → file an issue with the timestamp.
- Does parallelism actually parallelise? Look at `recurring_parallel`'s
  task durations: the 4 workers should finish within ~7 s of each other
  (max sleep is 6 s), NOT take 18 s in sequence.
- Trigger `lifecycle` from the UI. Logs visible? Status transitions correct?
  XCom values flow between the 3 tasks?
- Try to break it — restart the host mid-run; kill the leoflow process
  with `pkill -9 leoflow`; pull the network briefly. Recovery contract is
  in `docs/scheduler-resilience.md`.

Findings → comments on the alpha-prep issues.

## What this is NOT

- Not a release of `v0.1.0-alpha.1` — that tag cuts only after a clean
  hands-on pass.
- Not a multi-user install — Lite is single-admin by design (see
  `docs/editions.md`).
- Not a Pro install — Pro is the Helm chart (`helm/leoflow/`), a separate
  rollout.

## Re-running

The install script is **idempotent** for everything except the password:

- A second `leoflow setup` preserves the existing config (and prints
  "already configured"). The password from the first run still works.
- DAGs are copied fresh each run; you can also drop your own DAG folders
  into `~/leoflow/` at any time.
- To start from a clean slate: `leoflow uninstall` (removes
  `~/.leoflow/`, KEEPS `~/leoflow/` workspace), then re-run this script.
