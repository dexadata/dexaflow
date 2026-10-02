---
aliases:
  - /cli/leoflow_lite.html
  - /reference/cli/leoflow_lite/
title: "dexaflow lite"
linkTitle: "lite"
weight: 37
---

Run Dexaflow Lite locally with hot reload.

### Synopsis

lite is the Dexaflow Lite edition: it brings up local dependencies and runs the control plane against an isolated local database, registers the DAG, and hot-reloads on every save. The UI is served on a Lite port (default 8088, --port), marked with a LITE badge, and behind a login (the admin created by `dexaflow setup`, which prints the
generated password ONCE — `dexaflow lite reset-password` sets a new one if it is gone).

Executor (--executor): 'subprocess' runs tasks unsandboxed on the host with no image build — the fast inner loop, best for local use. 'k8s' runs real pod-per-task on a dedicated, isolated k3d mini-cluster (leoflow-dev) — highest fidelity, best for development; it rebuilds the DAG image on each change.

('leoflow dev' remains as a deprecated alias.)

```
dexaflow lite [path] [flags]
```

### Options

```
      --agent-bin string     leoflow-agent binary (default: PATH, then ./bin)
      --compose string       compose file for the local Postgres (default: a managed one under ~/.dexaflow, materialized on first run)
      --executor string      execution mode: 'auto' (default; k3d if Docker is present, else subprocess), 'k8s' (dedicated k3d cluster, real pods), or 'subprocess' (host, fast, unsandboxed) (default "auto")
      --fresh                drop the local dev database first, so the session starts with nothing registered (DESTRUCTIVE: registered DAGs, runs and history)
  -h, --help                 help for lite
      --host string          address to bind the UI/API to; use 0.0.0.0 to reach it from your internal network/VPN (insecure — see the warning) (default "127.0.0.1")
      --image string         placeholder image recorded in dag.json (subprocess mode only) (default "leoflow-dev:local")
      --no-up                skip docker compose (Postgres already running); the dev DB + venv are still provisioned
      --port int             HTTP/UI port (dev default 8088, distinct from the demo's 8080) (default 8088)
      --postgres string      Postgres backend: 'auto' (default; the Docker postgres:16 when Docker is present, else a managed relocatable PG under ~/.dexaflow on a Unix socket, no Docker), 'docker', or 'managed' (best on full distros; minimal hosts may lack its system libs) (default "auto")
      --runtime-src string   source of the leoflow_runtime package installed into the dev venv (default "runtime/python")
      --server-bin string    leoflow-server binary (default: PATH, then ./bin)
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.dexaflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [dexaflow](/reference/cli/dexaflow/)	 - Dexaflow is a GitOps-first, container-native workflow orchestrator.
* [dexaflow lite backup](/reference/cli/dexaflow_lite_backup/)	 - Snapshot the Lite install (workspace + datastore + config) into a portable archive.
* [dexaflow lite forget](/reference/cli/dexaflow_lite_forget/)	 - Remove a DAG (and all its history) from the Lite registry without touching the source files.
* [dexaflow lite provision](/reference/cli/dexaflow_lite_provision/)	 - Check and provision the local deps the from-source `dexaflow lite` loop needs.
* [dexaflow lite reset-password](/reference/cli/dexaflow_lite_reset-password/)	 - Reset the Dexaflow Lite admin password.
* [dexaflow lite restore](/reference/cli/dexaflow_lite_restore/)	 - Restore a Lite install from an archive produced by `dexaflow lite backup`.

