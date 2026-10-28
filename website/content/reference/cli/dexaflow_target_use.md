---
aliases:
  - /cli/leoflow_target_use.html
  - /reference/cli/leoflow_target_use/
title: "dexaflow target use"
linkTitle: "target use"
weight: 70
---

Select the target commands use when no --target is given.

### Synopsis

Inside a workspace, the choice is yours alone: it is saved in your config file
for that workspace and the shared file is left untouched. --workspace-default
changes the workspace's default_target instead, for everyone who clones it.
Outside a workspace, or with --global, it sets the target used everywhere else.

```
dexaflow target use <name> [flags]
```

### Options

```
      --global              set the target used outside workspaces
  -h, --help                help for use
      --workspace-default   write default_target into the shared workspace file
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.dexaflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
      --target name         name of the control plane to use (default: DEXAFLOW_TARGET, then the target selected for the workspace; see dexaflow target list)
```

### SEE ALSO

* [dexaflow target](/reference/cli/dexaflow_target/)	 - Manage named control planes (targets) to deploy to and query.

