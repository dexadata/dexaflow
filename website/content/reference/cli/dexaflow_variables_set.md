---
aliases:
  - /cli/leoflow_variables_set.html
  - /reference/cli/leoflow_variables_set/
title: "dexaflow variables set"
linkTitle: "variables set"
weight: 57
---

Create or replace a variable (upsert).

### Synopsis

Upserts a variable: creates it, or replaces an existing one with the same key. Pass the value as the second argument, or use --value-stdin to read it from stdin (keeps a secret out of your shell history and argv).

```
dexaflow variables set <key> [value] [flags]
```

### Options

```
      --description string   human-readable description
  -h, --help                 help for set
      --server string        control plane base URL (default: config server_url)
      --token string         JWT bearer token (default: config token)
      --value-stdin          read the value from stdin instead of the positional argument
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.dexaflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [dexaflow variables](/reference/cli/dexaflow_variables/)	 - Manage control-plane variables.

