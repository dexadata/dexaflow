---
aliases:
  - /cli/leoflow_push.html
  - /reference/cli/leoflow_push/
title: "dexaflow push"
linkTitle: "push"
weight: 43
---

Register a compiled dag.json with the control plane.

```
dexaflow push <dag.json> [flags]
```

### Options

```
  -h, --help            help for push
      --server string   control plane base URL (default: config server_url)
      --token string    JWT bearer token
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.leoflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [dexaflow](/reference/cli/dexaflow/)	 - Dexaflow is a GitOps-first, container-native workflow orchestrator.

