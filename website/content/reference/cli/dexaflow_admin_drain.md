---
aliases:
  - /cli/leoflow_admin_drain.html
  - /reference/cli/leoflow_admin_drain/
title: "dexaflow admin drain"
linkTitle: "admin drain"
weight: 6
---

Pause every DAG, then wait for active runs to finish (quiesce for maintenance).

### Synopsis

Safely quiesce the control plane before maintenance or an upgrade: pause every registered DAG so no new runs start, then poll active runs until none remain or --timeout elapses. On timeout it prints the still-running runs and exits non-zero. Use --no-wait to pause without waiting.

```
dexaflow admin drain [flags]
```

### Options

```
  -h, --help                     help for drain
      --no-wait                  pause every DAG but do not wait for active runs
      --poll-interval duration   how often to re-check for active runs (default 5s)
      --server string            control plane base URL (default: config server_url)
      --timeout duration         max time to wait for active runs to drain (default 10m0s)
      --token string             JWT bearer token (default: config token)
      --wait                     poll active runs until they drain (default true)
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.leoflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [dexaflow admin](dexaflow_admin.md)	 - Operate a running control plane (health, pause, drain, runs).

