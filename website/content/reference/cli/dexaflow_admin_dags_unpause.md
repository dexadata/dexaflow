---
aliases:
  - /cli/leoflow_admin_dags_unpause.html
  - /reference/cli/leoflow_admin_dags_unpause/
title: "dexaflow admin dags unpause"
linkTitle: "admin dags unpause"
weight: 5
---

Unpause a DAG (PATCH is_paused), or every DAG with --all.

```
dexaflow admin dags unpause [dag_id] [flags]
```

### Options

```
      --all             unpause every registered DAG
  -h, --help            help for unpause
      --server string   control plane base URL (default: config server_url)
      --token string    JWT bearer token (default: config token)
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.dexaflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [dexaflow admin dags](/reference/cli/dexaflow_admin_dags/)	 - Pause or unpause registered DAGs.

