---
aliases:
  - /cli/leoflow_lite_forget.html
  - /reference/cli/leoflow_lite_forget/
title: "dexaflow lite forget"
linkTitle: "lite forget"
weight: 39
---

Remove a DAG (and all its history) from the Lite registry without touching the source files.

### Synopsis

forget hard-deletes a DAG from the Lite registry. The dag.py and leoflow.yaml on disk are untouched — only the database rows go. FK cascade handles versions, runs, task instances, and XCom. The watcher will re-discover the project on the next tick if its files are still present, so use this when you want to deregister AND plan to delete the source files yourself, OR when you want a clean re-registration after a manual database edit.

Run it as the same user as `leoflow lite` (no sudo). The Lite Postgres must be reachable.

```
dexaflow lite forget [dag_id] [flags]
```

### Options

```
      --all       deregister every DAG in the Lite registry
      --dry-run   print what would be deregistered without writing anything
  -h, --help      help for forget
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.leoflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [dexaflow lite](/reference/cli/dexaflow_lite/)	 - Run Leoflow Lite locally with hot reload.

