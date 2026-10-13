---
aliases:
  - /cli/leoflow_target.html
  - /reference/cli/leoflow_target/
title: "dexaflow target"
linkTitle: "target"
weight: 53
---

Manage named control planes (targets) to deploy to and query.

### Synopsis

A target names a control plane. Inside a workspace, targets are kept in
dexaflow.workspace.yaml and shared with everyone who clones it; outside a
workspace, or with --global, they are kept in your config file.

Every server command takes --target <name> (or DEXAFLOW_TARGET). Without
either, the target selected with `dexaflow target use` is used, then the
workspace's default_target, then the config file's server_url.

Tokens are never stored in the workspace file: `dexaflow auth login --target
<name>` saves one per server in your config file.

### Options

```
  -h, --help   help for target
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
* [dexaflow target add](/reference/cli/dexaflow_target_add/)	 - Add a target (to the workspace file, or your config with --global).
* [dexaflow target list](/reference/cli/dexaflow_target_list/)	 - List the targets, marking the one in use.
* [dexaflow target remove](/reference/cli/dexaflow_target_remove/)	 - Remove a target (from the workspace file, or your config with --global).
* [dexaflow target use](/reference/cli/dexaflow_target_use/)	 - Select the target commands use when no --target is given.

