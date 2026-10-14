---
aliases:
  - /cli/leoflow_push.html
  - /reference/cli/leoflow_push/
title: "dexaflow push"
linkTitle: "push"
weight: 50
---

Register a compiled dag.json with the control plane.

```
dexaflow push <dag.json> [flags]
```

### Options

```
  -h, --help            help for push
      --server string   control plane base URL, over --target (default: the target in use, then server_url from the config file)
      --token string    JWT bearer token (default: the session saved for the server)
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

