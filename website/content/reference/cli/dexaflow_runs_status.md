---
aliases:
  - /cli/leoflow_runs_status.html
  - /reference/cli/leoflow_runs_status/
title: "dexaflow runs status"
linkTitle: "runs status"
weight: 47
---

Show the state of a DAG run (the latest by default).

```
dexaflow runs status <dag_id> [flags]
```

### Options

```
  -h, --help            help for status
      --run string      specific dag_run_id (default: the most recent run)
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

* [dexaflow runs](/reference/cli/dexaflow_runs/)	 - Trigger and inspect DAG runs.

