---
aliases:
  - /cli/leoflow_db_reset.html
  - /reference/cli/leoflow_db_reset/
title: "dexaflow db reset"
linkTitle: "db reset"
weight: 40
---

Drop, recreate, and migrate the Lite database (DESTRUCTIVE).

### Synopsis

reset drops Lite's own database, the managed or the Docker Postgres `dexaflow lite` starts, then recreates and migrates it. It never acts on another database: Lite does not read DEXAFLOW_DATABASE_URL (or LEOFLOW_DATABASE_URL), and reset refuses, dropping nothing, when either is set to a different host, port or database.

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
      --target name         name of the control plane to use (default: DEXAFLOW_TARGET, then the target selected for the workspace; see dexaflow target list)
```

### SEE ALSO

* [dexaflow db](/reference/cli/dexaflow_db/)	 - Manage the local Lite database (schema name leoflow_dev for upgrade safety).

