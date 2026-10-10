---
aliases:
  - /cli/leoflow_db_reset.html
  - /reference/cli/leoflow_db_reset/
title: "dexaflow db reset"
linkTitle: "db reset"
weight: 33
---

Drop, recreate, and migrate the Lite database (DESTRUCTIVE).

```
dexaflow db reset [flags]
```

### Options

```
  -h, --help   help for reset
      --yes    confirm the destructive reset
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.dexaflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [dexaflow db](/reference/cli/dexaflow_db/)	 - Manage the local Lite database (schema name leoflow_dev for upgrade safety).

