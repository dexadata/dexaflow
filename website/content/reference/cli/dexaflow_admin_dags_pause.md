---
aliases:
  - /cli/leoflow_admin_dags_pause.html
  - /reference/cli/leoflow_admin_dags_pause/
title: "dexaflow admin dags pause"
linkTitle: "admin dags pause"
weight: 4
---

Pause a DAG (PATCH is_paused), or every DAG with --all.

```
dexaflow admin dags pause [dag_id] [flags]
```

### Options

```
      --all             pause every registered DAG
  -h, --help            help for pause
      --server string   control plane base URL (default: config server_url)
      --token string    JWT bearer token (default: config token)
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.leoflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [dexaflow admin dags](dexaflow_admin_dags.md)	 - Pause or unpause registered DAGs.

