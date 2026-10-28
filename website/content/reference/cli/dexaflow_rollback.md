---
aliases:
  - /cli/leoflow_rollback.html
  - /reference/cli/leoflow_rollback/
title: "dexaflow rollback"
linkTitle: "rollback"
weight: 56
---

Make an earlier registered version of a DAG current again.

### Synopsis

Makes a registered version of the DAG the one new runs use, with that version's schedule. Without --to it is the version registered before the current one; --to takes a label (bundle_version) or a version number from `dexaflow dags versions`, matched as a label first and then as a number. Runs that already exist keep the version they started with, and the DAG stays paused or unpaused as it is. Nothing is rebuilt: the version's pinned image is used as registered.

```
dexaflow rollback <dag_id> [flags]
```

### Options

```
  -h, --help            help for rollback
      --server string   control plane base URL (default: config server_url)
      --to string       version to make current: matched first as a label, then as a version number (default: the one before the current version)
      --token string    JWT bearer token (default: config token)
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

