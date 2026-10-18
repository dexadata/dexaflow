---
aliases:
  - /cli/leoflow_ci.html
  - /reference/cli/leoflow_ci/
title: "dexaflow ci"
linkTitle: "ci"
weight: 21
---

Set up a CI pipeline that deploys the workspace.

### Options

```
  -h, --help   help for ci
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
* [dexaflow ci init](/reference/cli/dexaflow_ci_init/)	 - Write a pipeline that deploys the DAGs a push changed.

