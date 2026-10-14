---
aliases:
  - /cli/leoflow_target_add.html
  - /reference/cli/leoflow_target_add/
title: "dexaflow target add"
linkTitle: "target add"
weight: 64
---

Add a target (to the workspace file, or your config with --global).

```
dexaflow target add <name> <server-url> [flags]
```

### Options

```
      --base-image string   base image for DAGs that set none, when deploying to this target
      --force               replace a target of the same name
      --global              keep the target in your config file even inside a workspace
  -h, --help                help for add
      --platform strings    image platform for deploys to this target (repeatable, e.g. linux/amd64)
      --registry string     image registry prefix deploys to this target push to (e.g. ghcr.io/acme)
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.dexaflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
      --target name         name of the control plane to use (default: DEXAFLOW_TARGET, then the target selected for the workspace; see dexaflow target list)
```

### SEE ALSO

* [dexaflow target](/reference/cli/dexaflow_target/)	 - Manage named control planes (targets) to deploy to and query.

