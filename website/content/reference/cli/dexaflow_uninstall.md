---
aliases:
  - /cli/leoflow_uninstall.html
  - /reference/cli/leoflow_uninstall/
title: "dexaflow uninstall"
linkTitle: "uninstall"
weight: 51
---

Remove the Leoflow installation (~/.leoflow).

### Synopsis

uninstall removes the managed Leoflow home (~/.leoflow): the binaries, config, managed Python, Monaco assets, and local dev state. It does NOT remove your DAG workspace or your datastore (the managed Postgres data in ~/.leoflow/pgdata and this install's Docker volume) unless you pass --purge — so a reinstall keeps your data. It asks for confirmation unless --yes is given. (To upgrade instead, just re-run install.sh — it replaces the binaries and keeps your config.)

```
dexaflow uninstall [flags]
```

### Options

```
  -h, --help    help for uninstall
      --purge   also remove the DAG workspace and Docker datastore volumes (destructive)
      --yes     skip the confirmation prompt
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.leoflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [dexaflow](dexaflow.md)	 - Dexaflow is a GitOps-first, container-native workflow orchestrator.

