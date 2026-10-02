---
aliases:
  - /cli/leoflow_admin_health.html
  - /reference/cli/leoflow_admin_health/
title: "dexaflow admin health"
linkTitle: "admin health"
weight: 7
---

Report control-plane health; non-zero exit when unhealthy.

### Synopsis

Query the monitor endpoints and print a compact status: component health (scheduler, metadatabase, DAG processor, triggerer), executor capability, and version. Exits non-zero when any component is unhealthy or the health endpoint is unreachable — usable as a post-deploy smoke test.

```
dexaflow admin health [flags]
```

### Options

```
  -h, --help            help for health
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

* [dexaflow admin](/reference/cli/dexaflow_admin/)	 - Operate a running control plane (health, pause, drain, runs).

