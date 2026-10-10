---
aliases:
  - /cli/leoflow_db_migrate.html
  - /reference/cli/leoflow_db_migrate/
title: "dexaflow db migrate"
linkTitle: "db migrate"
weight: 32
---

Create (if needed) and migrate the Lite database to the latest schema.

```
dexaflow db migrate [flags]
```

### Options

```
  -h, --help   help for migrate
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.dexaflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [dexaflow db](/reference/cli/dexaflow_db/)	 - Manage the local Lite database (schema name leoflow_dev for upgrade safety).

