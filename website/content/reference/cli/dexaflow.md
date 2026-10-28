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
      --config string       config file path (default ~/.dexaflow/config.yaml)
  -h, --help                help for dexaflow
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
      --target name         name of the control plane to use (default: DEXAFLOW_TARGET, then the target selected for the workspace; see dexaflow target list)
```

### SEE ALSO

* [dexaflow admin](/reference/cli/dexaflow_admin/)	 - Operate a running control plane (health, pause, drain, runs).
* [dexaflow auth](/reference/cli/dexaflow_auth/)	 - Manage authentication tokens.
* [dexaflow build](/reference/cli/dexaflow_build/)	 - Build the container image of every DAG project in a workspace.
* [dexaflow ci](/reference/cli/dexaflow_ci/)	 - Set up a CI pipeline that deploys the workspace.
* [dexaflow compile](/reference/cli/dexaflow_compile/)	 - Compile a DAG project into dag.json via the Python parser.
* [dexaflow completion](/reference/cli/dexaflow_completion/)	 - Generate the autocompletion script for the specified shell
* [dexaflow connections](/reference/cli/dexaflow_connections/)	 - Manage control-plane connections.
* [dexaflow dags](/reference/cli/dexaflow_dags/)	 - Manage registered DAGs.
* [dexaflow db](/reference/cli/dexaflow_db/)	 - Manage the local Lite database (schema name leoflow_dev for upgrade safety).
* [dexaflow deploy](/reference/cli/dexaflow_deploy/)	 - Build, push, and register a DAG to a control plane (Cluster).
* [dexaflow doctor](/reference/cli/dexaflow_doctor/)	 - Report host platform, dependencies, and the achievable operating tier.
* [dexaflow init](/reference/cli/dexaflow_init/)	 - Scaffold a new DAG project (dexaflow.yaml + dag.py).
* [dexaflow lite](/reference/cli/dexaflow_lite/)	 - Run Dexaflow Lite locally with hot reload.
* [dexaflow new](/reference/cli/dexaflow_new/)	 - Scaffold a new DAG under the workspace's dags/ folder.
* [dexaflow promote](/reference/cli/dexaflow_promote/)	 - Register on one target the exact version another target runs (same digest, no build).
* [dexaflow push](/reference/cli/dexaflow_push/)	 - Register a compiled dag.json with the control plane.
* [dexaflow registry](/reference/cli/dexaflow_registry/)	 - Sign in to the container registry DAG images are pushed to
* [dexaflow rollback](/reference/cli/dexaflow_rollback/)	 - Make an earlier registered version of a DAG current again.
* [dexaflow runs](/reference/cli/dexaflow_runs/)	 - Trigger and inspect DAG runs.
* [dexaflow server](/reference/cli/dexaflow_server/)	 - Information about running the control plane.
* [dexaflow setup](/reference/cli/dexaflow_setup/)	 - Bootstrap the managed Dexaflow runtime (Python, parser, workspace).
* [dexaflow status](/reference/cli/dexaflow_status/)	 - Show per DAG the version current on a target, its last run, and drift from the workspace.
* [dexaflow sync](/reference/cli/dexaflow_sync/)	 - Make the target run the DAGs of a workspace: deploy what changed, report or pause what is gone (Cluster).
* [dexaflow target](/reference/cli/dexaflow_target/)	 - Manage named control planes (targets) to deploy to and query.
* [dexaflow uninstall](/reference/cli/dexaflow_uninstall/)	 - Remove the Dexaflow installation (~/.dexaflow).
* [dexaflow validate](/reference/cli/dexaflow_validate/)	 - Validate dexaflow.yaml and the DAG source against the schema.
* [dexaflow variables](/reference/cli/dexaflow_variables/)	 - Manage control-plane variables.
* [dexaflow version](/reference/cli/dexaflow_version/)	 - Print the version, git commit, and build date.
* [dexaflow workspace](/reference/cli/dexaflow_workspace/)	 - Create and manage a workspace (many DAGs, shared targets and defaults).

