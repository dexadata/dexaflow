---
aliases:
  - /cli/leoflow_runs.html
  - /reference/cli/leoflow_runs/
title: "dexaflow runs"
linkTitle: "runs"
weight: 57
---

Trigger and inspect DAG runs.

### Options

```
  -h, --help   help for runs
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.dexaflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
      --target name         name of the control plane to use (default: DEXAFLOW_TARGET, then the target selected for the workspace; see dexaflow target list)
```

### SEE ALSO

* [dexaflow](/reference/cli/dexaflow/)	 - Dexaflow is a GitOps-first, container-native workflow orchestrator.
* [dexaflow runs list](/reference/cli/dexaflow_runs_list/)	 - List DAG runs, filtered by --state, --older-than, and/or --dag.
* [dexaflow runs logs](/reference/cli/dexaflow_runs_logs/)	 - Print task logs: one task's attempt, or every task of a run (the latest run by default).
* [dexaflow runs status](/reference/cli/dexaflow_runs_status/)	 - Show the state of a DAG run (the latest by default).
* [dexaflow runs trigger](/reference/cli/dexaflow_runs_trigger/)	 - Trigger a new run of a DAG, and optionally follow it to the end.

