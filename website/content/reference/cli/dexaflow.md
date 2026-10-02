---
aliases:
  - /cli/leoflow.html
  - /reference/cli/leoflow/
title: "dexaflow"
linkTitle: "dexaflow"
weight: 1
---

Dexaflow is a GitOps-first, container-native workflow orchestrator.

### Options

```
      --config string       config file path (default ~/.leoflow/config.yaml)
  -h, --help                help for dexaflow
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [dexaflow admin](/reference/cli/dexaflow_admin/)	 - Operate a running control plane (health, pause, drain, runs).
* [dexaflow auth](/reference/cli/dexaflow_auth/)	 - Manage authentication tokens.
* [dexaflow build](/reference/cli/dexaflow_build/)	 - Build the container image of every DAG project in a workspace.
* [dexaflow compile](/reference/cli/dexaflow_compile/)	 - Compile a DAG project into dag.json via the Python parser.
* [dexaflow completion](/reference/cli/dexaflow_completion/)	 - Generate the autocompletion script for the specified shell
* [dexaflow connections](/reference/cli/dexaflow_connections/)	 - Manage control-plane connections.
* [dexaflow dags](/reference/cli/dexaflow_dags/)	 - Manage registered DAGs.
* [dexaflow db](/reference/cli/dexaflow_db/)	 - Manage the local Lite database (schema name leoflow_dev for upgrade safety).
* [dexaflow deploy](/reference/cli/dexaflow_deploy/)	 - Build, push, and register a DAG to a control plane (Pro).
* [dexaflow doctor](/reference/cli/dexaflow_doctor/)	 - Report host platform, dependencies, and the achievable operating tier.
* [dexaflow init](/reference/cli/dexaflow_init/)	 - Scaffold a new DAG project (leoflow.yaml + dag.py).
* [dexaflow lite](/reference/cli/dexaflow_lite/)	 - Run Leoflow Lite locally with hot reload.
* [dexaflow push](/reference/cli/dexaflow_push/)	 - Register a compiled dag.json with the control plane.
* [dexaflow runs](/reference/cli/dexaflow_runs/)	 - Trigger and inspect DAG runs.
* [dexaflow server](/reference/cli/dexaflow_server/)	 - Information about running the control plane.
* [dexaflow setup](/reference/cli/dexaflow_setup/)	 - Bootstrap the managed Leoflow runtime (Python, parser, workspace).
* [dexaflow uninstall](/reference/cli/dexaflow_uninstall/)	 - Remove the Leoflow installation (~/.leoflow).
* [dexaflow validate](/reference/cli/dexaflow_validate/)	 - Validate leoflow.yaml and the DAG source against the schema.
* [dexaflow variables](/reference/cli/dexaflow_variables/)	 - Manage control-plane variables.
* [dexaflow version](/reference/cli/dexaflow_version/)	 - Print the version, git commit, and build date.

