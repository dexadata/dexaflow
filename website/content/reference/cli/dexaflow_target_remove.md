---
aliases:
  - /cli/leoflow_target_remove.html
  - /reference/cli/leoflow_target_remove/
title: "dexaflow target remove"
linkTitle: "target remove"
weight: 69
---

Remove a target (from the workspace file, or your config with --global).

```
dexaflow target remove <name> [flags]
```

### Options

```
      --global   remove the target from your config file even inside a workspace
  -h, --help     help for remove
      --logout   also forget the session saved for the target's server
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

