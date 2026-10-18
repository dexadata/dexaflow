---
aliases:
  - /cli/leoflow_registry.html
  - /reference/cli/leoflow_registry/
title: "dexaflow registry"
linkTitle: "registry"
weight: 54
---

Sign in to the container registry DAG images are pushed to

### Options

```
  -h, --help   help for registry
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
* [dexaflow registry login](/reference/cli/dexaflow_registry_login/)	 - Sign in to a registry without docker, storing the credential where builds and pushes read it

