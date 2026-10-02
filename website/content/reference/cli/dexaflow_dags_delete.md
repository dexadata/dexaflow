---
aliases:
  - /cli/leoflow_dags_delete.html
  - /reference/cli/leoflow_dags_delete/
title: "dexaflow dags delete"
linkTitle: "dags delete"
weight: 29
---

Clear a DAG's run history, or fully deregister it with --deregister.

### Synopsis

By default this clears the DAG's run history but keeps the DAG and its versions registered — the same as the UI trash button (ADR 0020). With --deregister it removes the DAG artifact entirely.

GitOps note: deregister is not permanent while the DAG's source still exists. In production the next deploy re-registers it as a new version; under `leoflow dev` the watcher re-registers it on the next reload — delete the DAG's file to stop that.

```
dexaflow dags delete <dag_id> [flags]
```

### Options

```
      --deregister      remove the DAG artifact entirely, not just its run history
  -h, --help            help for delete
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

* [dexaflow dags](/reference/cli/dexaflow_dags/)	 - Manage registered DAGs.

