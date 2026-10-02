---
aliases:
  - /cli/leoflow_build.html
  - /reference/cli/leoflow_build/
title: "dexaflow build"
linkTitle: "build"
weight: 16
---

Build the container image of every DAG project in a workspace.

### Synopsis

Compiles and builds each project found under the workspace, using the image reference that project's own `registry:` block derives.

A project that declares no registry cannot be built and is reported by name rather than skipped quietly — a build that covers less than the workspace, silently, leaves you believing images exist.

```
dexaflow build [workspace] [flags]
```

### Options

```
      --builder string       image build tool to shell out to (e.g. docker, podman, nerdctl) (default "docker")
      --dag-version string   version recorded in each dag.json and used by the registry tag strategy (default: git describe, else dev)
  -h, --help                 help for build
      --push                 push each built image to its registry
      --sha string           commit sha for the git_sha tag strategy (default: git rev-parse --short HEAD)
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.dexaflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [dexaflow](/reference/cli/dexaflow/)	 - Dexaflow is a GitOps-first, container-native workflow orchestrator.

