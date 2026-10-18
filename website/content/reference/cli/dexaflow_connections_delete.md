---
aliases:
  - /cli/leoflow_connections_delete.html
  - /reference/cli/leoflow_connections_delete/
title: "dexaflow connections delete"
linkTitle: "connections delete"
weight: 30
---

Delete a connection.

```
dexaflow connections delete <connection_id> [flags]
```

### Options

```
  -h, --help            help for delete
      --server string   control plane base URL (default: config server_url)
      --token string    JWT bearer token (default: config token)
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.dexaflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
      --target name         name of the control plane to use (default: DEXAFLOW_TARGET, then the target selected for the workspace; see dexaflow target list)
```

### SEE ALSO

* [dexaflow connections](/reference/cli/dexaflow_connections/)	 - Manage control-plane connections.

