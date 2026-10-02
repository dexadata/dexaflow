---
aliases:
  - /cli/leoflow_admin_dags.html
  - /reference/cli/leoflow_admin_dags/
title: "dexaflow admin dags"
linkTitle: "admin dags"
weight: 3
---

Pause or unpause registered DAGs.

### Options

```
  -h, --help   help for dags
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.leoflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [dexaflow admin](dexaflow_admin.md)	 - Operate a running control plane (health, pause, drain, runs).
* [dexaflow admin dags pause](dexaflow_admin_dags_pause.md)	 - Pause a DAG (PATCH is_paused), or every DAG with --all.
* [dexaflow admin dags unpause](dexaflow_admin_dags_unpause.md)	 - Unpause a DAG (PATCH is_paused), or every DAG with --all.

