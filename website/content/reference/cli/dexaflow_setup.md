---
aliases:
  - /cli/leoflow_setup.html
  - /reference/cli/leoflow_setup/
title: "dexaflow setup"
linkTitle: "setup"
weight: 63
---

Bootstrap the managed Dexaflow runtime (Python, parser, workspace).

### Synopsis

setup prepares ~/.dexaflow: it picks the Python the DAG parser runs on (a host python3.13, python3.12, python3.11 or python3 reporting 3.11 to 3.13, otherwise downloading a pinned, checksum-verified relocatable CPython 3.11; no sudo, no system packages; a DAG declaring python_version 3.11 on a host without 3.11 fetches that CPython when its venv is first built), extracts the embedded parser and runtime sources, points the parser at the interpreter (pure Python, no venv, no Airflow; ADR 0024), and creates a workspace directory. Re-running is safe; --dry-run shows the plan.

```
dexaflow setup [flags]
```

### Options

```
      --dry-run            detect and print the plan without downloading or writing anything
  -h, --help               help for setup
      --workspace string   workspace dir for your DAG projects (default ~/dexaflow, or an existing ~/leoflow)
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.dexaflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
      --target name         name of the control plane to use (default: DEXAFLOW_TARGET, then the target selected for the workspace; see dexaflow target list)
```

### SEE ALSO

* [dexaflow](/reference/cli/dexaflow/)	 - Dexaflow is a GitOps-first, container-native workflow orchestrator.

