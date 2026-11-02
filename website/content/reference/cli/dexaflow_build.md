---
aliases:
  - /cli/leoflow_build.html
  - /reference/cli/leoflow_build/
title: "dexaflow build"
linkTitle: "build"
weight: 20
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
      --dag-version string   version recorded in each dag.json and used by the registry tag strategy (default: per project, git describe of its directory, or dev outside git, plus a hash of its content)
      --dry-run              check every project and print the image each would be built as, then stop without building
  -h, --help                 help for build
      --platform strings     image platform to build for, os/arch (repeatable or comma-separated), over build.platforms in dexaflow.yaml, the target and the workspace defaults (default linux/amd64)
      --push                 push each built image to its registry
      --rebuild              build each dependency image again even when the registry holds one for its key (with --push)
      --registry string      image registry prefix to push to, over every other source (then DEXAFLOW_REGISTRY, dexaflow.yaml, the target, the workspace defaults, the config file)
      --sha string           commit sha for the git_sha tag strategy (default: git rev-parse --short HEAD in each project's directory)
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

