---
aliases:
  - /cli/leoflow_push.html
  - /reference/cli/leoflow_push/
title: "dexaflow push"
linkTitle: "push"
weight: 53
---

Register a compiled dag.json with the control plane.

### Synopsis

Register a compiled dag.json with the control plane.

The image is registered by digest: an image given as a tag, in dag.json or with --image, is resolved in its registry first (a read of up to 30s), with the credentials `dexaflow deploy` reads and the registry.auth_method of the project (--project). A pinned image is used as given and reads nothing. When the registry cannot be read, a tag from dag.json is registered as written, with a warning that says why; a tag given with --image must resolve.

```
dexaflow push <dag.json> [flags]
```

### Options

```
  -h, --help             help for push
      --image string     image to register instead of the one in dag.json; a tag is resolved to its digest, a <repository>@sha256:<digest> is used as given
      --project string   project directory whose dexaflow.yaml gives registry.auth_method (default: the dag.json's directory when it holds one, else the working directory)
      --server string    control plane base URL, over --target (default: the target in use, then server_url from the config file)
      --token string     JWT bearer token (default: the session saved for the server)
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

