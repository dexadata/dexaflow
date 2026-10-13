---
aliases:
  - /cli/leoflow_runs_logs.html
  - /reference/cli/leoflow_runs_logs/
title: "dexaflow runs logs"
linkTitle: "runs logs"
weight: 50
---

Print task logs: one task's attempt, or every task of a run (the latest run by default).

```
dexaflow runs logs <dag_id> [run_id [task_id]] [flags]
```

### Options

```
  -f, --follow          with a task_id, keep streaming its new log lines; without one, follow the run to its end
  -h, --help            help for logs
      --server string   control plane base URL (default: config server_url)
      --token string    JWT bearer token (default: config token)
      --try int         attempt number to read, with a task_id (default: the task's latest attempt)
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.dexaflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
      --target name         name of the control plane to use (default: DEXAFLOW_TARGET, then the target selected for the workspace; see dexaflow target list)
```

### SEE ALSO

* [dexaflow runs](/reference/cli/dexaflow_runs/)	 - Trigger and inspect DAG runs.

