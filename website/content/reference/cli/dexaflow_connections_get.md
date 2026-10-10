---
aliases:
  - /cli/leoflow_connections_get.html
  - /reference/cli/leoflow_connections_get/
title: "dexaflow connections get"
linkTitle: "connections get"
weight: 25
---

Show a connection (password omitted, extra masked).

```
dexaflow connections get <connection_id> [flags]
```

### Options

```
  -h, --help            help for get
      --server string   control plane base URL (default: config server_url)
      --token string    JWT bearer token (default: config token)
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.dexaflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [dexaflow connections](/reference/cli/dexaflow_connections/)	 - Manage control-plane connections.

