---
aliases:
  - /cli/leoflow_runs_list.html
  - /reference/cli/leoflow_runs_list/
title: "dexaflow runs list"
linkTitle: "runs list"
weight: 45
---

List DAG runs, filtered by --state, --older-than, and/or --dag.

```
dexaflow runs list [flags]
```

### Options

```
      --dag string            limit to a single DAG id (default: all DAGs)
  -h, --help                  help for list
      --older-than duration   only runs whose start time is older than this (e.g. 2h)
      --server string         control plane base URL (default: config server_url)
      --state string          filter by run state: queued, running, success, or failed
      --token string          JWT bearer token (default: config token)
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.leoflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [dexaflow runs](/reference/cli/dexaflow_runs/)	 - Trigger and inspect DAG runs.

