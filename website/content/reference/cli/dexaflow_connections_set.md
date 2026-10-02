---
aliases:
  - /cli/leoflow_connections_set.html
  - /reference/cli/leoflow_connections_set/
title: "dexaflow connections set"
linkTitle: "connections set"
weight: 27
---

Create or update a connection (upsert).

### Synopsis

Creates a connection, or updates an existing one with the same id. --conn-type is required.

Only the fields you pass are changed; any field you omit keeps its current value. So you can change just --host without re-supplying the password (which cannot be read back anyway). To clear a field, pass it as an empty string, e.g. --login '' or --schema ''.

The password and extra are sent to the control plane but never printed back; read commands show masked values. Prefer --password-stdin / --extra-file over --password / --extra so a secret never lands in your shell history or the process table.

```
dexaflow connections set <connection_id> [flags]
```

### Options

```
      --conn-type string     connection type, e.g. postgres, http, aws (required)
      --description string   human-readable description
      --extra string         extra JSON blob (provider-specific; secrets here are masked on read)
      --extra-file string    read the extra JSON from a file instead of --extra (keeps provider secrets out of argv)
  -h, --help                 help for set
      --host string          connection host
      --login string         connection login/username
      --password string      connection password (prefer --password-stdin)
      --password-stdin       read the password from stdin instead of --password (avoids ps/shell-history exposure)
      --port int             connection port (omit to keep the stored value; an explicit value, including 0, overwrites it)
      --schema string        connection schema/database
      --server string        control plane base URL (default: config server_url)
      --token string         JWT bearer token (default: config token)
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.leoflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [dexaflow connections](/reference/cli/dexaflow_connections/)	 - Manage control-plane connections.

