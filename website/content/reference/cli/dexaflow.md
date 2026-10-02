---
aliases:
  - /cli/leoflow.html
  - /reference/cli/leoflow/
title: "dexaflow"
linkTitle: "leoflow"
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

* [dexaflow admin](dexaflow_admin.md)	 - Operate a running control plane (health, pause, drain, runs).
* [dexaflow auth](dexaflow_auth.md)	 - Manage authentication tokens.
* [dexaflow build](dexaflow_build.md)	 - Build the container image of every DAG project in a workspace.
* [dexaflow compile](dexaflow_compile.md)	 - Compile a DAG project into dag.json via the Python parser.
* [dexaflow completion](dexaflow_completion.md)	 - Generate the autocompletion script for the specified shell
* [dexaflow connections](dexaflow_connections.md)	 - Manage control-plane connections.
* [dexaflow dags](dexaflow_dags.md)	 - Manage registered DAGs.
* [dexaflow db](dexaflow_db.md)	 - Manage the local Lite database (schema name leoflow_dev for upgrade safety).
* [dexaflow deploy](dexaflow_deploy.md)	 - Build, push, and register a DAG to a control plane (Pro).
* [dexaflow doctor](dexaflow_doctor.md)	 - Report host platform, dependencies, and the achievable operating tier.
* [dexaflow init](dexaflow_init.md)	 - Scaffold a new DAG project (leoflow.yaml + dag.py).
* [dexaflow lite](dexaflow_lite.md)	 - Run Leoflow Lite locally with hot reload.
* [dexaflow push](dexaflow_push.md)	 - Register a compiled dag.json with the control plane.
* [dexaflow runs](dexaflow_runs.md)	 - Trigger and inspect DAG runs.
* [dexaflow server](dexaflow_server.md)	 - Information about running the control plane.
* [dexaflow setup](dexaflow_setup.md)	 - Bootstrap the managed Leoflow runtime (Python, parser, workspace).
* [dexaflow uninstall](dexaflow_uninstall.md)	 - Remove the Leoflow installation (~/.leoflow).
* [dexaflow validate](dexaflow_validate.md)	 - Validate leoflow.yaml and the DAG source against the schema.
* [dexaflow variables](dexaflow_variables.md)	 - Manage control-plane variables.
* [dexaflow version](dexaflow_version.md)	 - Print the version, git commit, and build date.

