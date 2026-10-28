---
aliases:
  - /cli/leoflow_new.html
  - /reference/cli/leoflow_new/
title: "dexaflow new"
linkTitle: "new"
weight: 51
---

Scaffold a new DAG under the workspace's dags/ folder.

### Synopsis

Writes dags/<dag_id>/dexaflow.yaml, dag.py and .gitignore in the workspace found
from the current directory. Existing files are never overwritten without --force.
Outside a workspace, use `dexaflow workspace init` first, or `dexaflow init <path>`
for a single project.

```
dexaflow new <dag_id> [flags]
```

### Options

```
      --force   overwrite the scaffold files when they already exist
  -h, --help    help for new
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

