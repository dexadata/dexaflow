---
aliases:
  - /cli/leoflow_runs_logs.html
  - /reference/cli/leoflow_runs_logs/
title: "dexaflow runs logs"
linkTitle: "runs logs"
weight: 47
---

Stream a task attempt's logs (the latest attempt by default).

```
dexaflow runs logs <dag_id> <run_id> <task_id> [flags]
```

### Options

```
  -f, --follow          keep streaming new log lines while the task is still running
  -h, --help            help for logs
      --server string   control plane base URL (default: config server_url)
      --token string    JWT bearer token (default: config token)
      --try int         attempt number to read (default: the task's latest attempt)
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.dexaflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [dexaflow runs](/reference/cli/dexaflow_runs/)	 - Trigger and inspect DAG runs.

