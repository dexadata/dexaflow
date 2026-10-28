---
aliases:
  - /cli/leoflow_workspace_init.html
  - /reference/cli/leoflow_workspace_init/
title: "dexaflow workspace init"
linkTitle: "workspace init"
weight: 80
---

Create a workspace: dexaflow.workspace.yaml, dags/ and .gitignore.

```
dexaflow workspace init [dir] [flags]
```

### Options

```
      --force          replace an existing workspace file
      --git git init   also run git init when the folder is not in a git repository yet
  -h, --help           help for init
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.dexaflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
      --target name         name of the control plane to use (default: DEXAFLOW_TARGET, then the target selected for the workspace; see dexaflow target list)
```

### SEE ALSO

* [dexaflow workspace](/reference/cli/dexaflow_workspace/)	 - Create and manage a workspace (many DAGs, shared targets and defaults).

