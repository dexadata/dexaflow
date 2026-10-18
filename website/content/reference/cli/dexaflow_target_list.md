---
aliases:
  - /cli/leoflow_target_list.html
  - /reference/cli/leoflow_target_list/
title: "dexaflow target list"
linkTitle: "target list"
weight: 68
---

List the targets, marking the one in use.

```
dexaflow target list [flags]
```

### Options

```
  -h, --help   help for list
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

