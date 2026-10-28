---
aliases:
  - /cli/leoflow_init.html
  - /reference/cli/leoflow_init/
title: "dexaflow init"
linkTitle: "init"
weight: 43
---

Scaffold a new DAG project (dexaflow.yaml + dag.py).

```
dexaflow init <path> [flags]
```

### Options

```
  -h, --help   help for init
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

