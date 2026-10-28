---
aliases:
  - /cli/leoflow_dags.html
  - /reference/cli/leoflow_dags/
title: "dexaflow dags"
linkTitle: "dags"
weight: 34
---

Manage registered DAGs.

### Options

```
  -h, --help   help for dags
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
* [dexaflow dags delete](/reference/cli/dexaflow_dags_delete/)	 - Clear a DAG's run history, or fully deregister it with --deregister.
* [dexaflow dags list](/reference/cli/dexaflow_dags_list/)	 - List registered DAGs.
* [dexaflow dags versions](/reference/cli/dexaflow_dags_versions/)	 - List a DAG's registered versions, marking the current one.

