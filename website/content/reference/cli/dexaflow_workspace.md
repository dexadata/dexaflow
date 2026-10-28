---
aliases:
  - /cli/leoflow_workspace.html
  - /reference/cli/leoflow_workspace/
title: "dexaflow workspace"
linkTitle: "workspace"
weight: 79
---

Create and manage a workspace (many DAGs, shared targets and defaults).

### Synopsis

A workspace is a folder, usually a git repository, holding DAG projects under
dags/ and a dexaflow.workspace.yaml with the targets they deploy to and shared
defaults (registry, platforms, base image). Commands run anywhere inside it find
it by walking up to that file.

### Options

```
  -h, --help   help for workspace
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
* [dexaflow workspace init](/reference/cli/dexaflow_workspace_init/)	 - Create a workspace: dexaflow.workspace.yaml, dags/ and .gitignore.

