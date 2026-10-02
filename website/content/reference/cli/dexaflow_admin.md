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

Operator commands for a running Leoflow control plane (Pro). These act over the /api/v2 API — checking health, pausing DAGs, draining the control plane before maintenance, and inspecting runs — and reuse the same --server/--token/config precedence as `leoflow deploy`.

### Options

```
  -h, --help   help for admin
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.leoflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [dexaflow](dexaflow.md)	 - Dexaflow is a GitOps-first, container-native workflow orchestrator.
* [dexaflow admin dags](dexaflow_admin_dags.md)	 - Pause or unpause registered DAGs.
* [dexaflow admin drain](dexaflow_admin_drain.md)	 - Pause every DAG, then wait for active runs to finish (quiesce for maintenance).
* [dexaflow admin health](dexaflow_admin_health.md)	 - Report control-plane health; non-zero exit when unhealthy.
* [dexaflow admin runs](dexaflow_admin_runs.md)	 - Inspect DAG runs across the control plane.
* [dexaflow admin users](dexaflow_admin_users.md)	 - Inspect accounts on the running control plane.

