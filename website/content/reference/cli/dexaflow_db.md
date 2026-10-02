---
aliases:
  - /cli/leoflow_db.html
  - /reference/cli/leoflow_db/
title: "dexaflow db"
linkTitle: "db"
weight: 31
---

Manage the local Lite database (schema name leoflow_dev for upgrade safety).

### Options

```
  -h, --help   help for db
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.leoflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [dexaflow](/reference/cli/dexaflow/)	 - Dexaflow is a GitOps-first, container-native workflow orchestrator.
* [dexaflow db migrate](/reference/cli/dexaflow_db_migrate/)	 - Create (if needed) and migrate the Lite database to the latest schema.
* [dexaflow db reset](/reference/cli/dexaflow_db_reset/)	 - Drop, recreate, and migrate the Lite database (DESTRUCTIVE).

