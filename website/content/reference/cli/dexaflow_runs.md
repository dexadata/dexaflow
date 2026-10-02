---
aliases:
  - /cli/leoflow_runs.html
  - /reference/cli/leoflow_runs/
title: "dexaflow runs"
linkTitle: "runs"
weight: 44
---

Trigger and inspect DAG runs.

### Options

```
  -h, --help   help for runs
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.leoflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [dexaflow](/reference/cli/dexaflow/)	 - Dexaflow is a GitOps-first, container-native workflow orchestrator.
* [dexaflow runs list](/reference/cli/dexaflow_runs_list/)	 - List DAG runs, filtered by --state, --older-than, and/or --dag.
* [dexaflow runs logs](/reference/cli/dexaflow_runs_logs/)	 - Stream a task attempt's logs (the latest attempt by default).
* [dexaflow runs status](/reference/cli/dexaflow_runs_status/)	 - Show the state of a DAG run (the latest by default).
* [dexaflow runs trigger](/reference/cli/dexaflow_runs_trigger/)	 - Trigger a new run of a DAG.

