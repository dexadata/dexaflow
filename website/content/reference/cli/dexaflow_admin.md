---
aliases:
  - /cli/leoflow_admin.html
  - /reference/cli/leoflow_admin/
title: "dexaflow admin"
linkTitle: "admin"
weight: 2
---

Operate a running control plane (health, pause, drain, runs).

### Synopsis

Operator commands for a running Dexaflow control plane (Pro). These act over the /api/v2 API — checking health, pausing DAGs, draining the control plane before maintenance, and inspecting runs — and reuse the same --server/--token/config precedence as `dexaflow deploy`.

### Options

```
  -h, --help   help for admin
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.dexaflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [dexaflow](/reference/cli/dexaflow/)	 - Dexaflow is a GitOps-first, container-native workflow orchestrator.
* [dexaflow admin dags](/reference/cli/dexaflow_admin_dags/)	 - Pause or unpause registered DAGs.
* [dexaflow admin drain](/reference/cli/dexaflow_admin_drain/)	 - Pause every DAG, then wait for active runs to finish (quiesce for maintenance).
* [dexaflow admin health](/reference/cli/dexaflow_admin_health/)	 - Report control-plane health; non-zero exit when unhealthy.
* [dexaflow admin runs](/reference/cli/dexaflow_admin_runs/)	 - Inspect DAG runs across the control plane.
* [dexaflow admin users](/reference/cli/dexaflow_admin_users/)	 - Inspect accounts on the running control plane.

