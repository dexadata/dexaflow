---
aliases:
  - /cli/leoflow_compile.html
  - /reference/cli/leoflow_compile/
title: "dexaflow compile"
linkTitle: "compile"
weight: 23
---

Compile a DAG project into dag.json via the Python parser.

```
dexaflow compile [path] [flags]
```

### Options

```
      --build                build the DAG container image (requires --image)
      --builder string       image build tool to shell out to (e.g. docker, podman, nerdctl) (default "docker")
      --dag-version string   DAG version label (default: git describe of the project directory, or dev outside git, plus a hash of the DAG's content)
      --dockerfile string    Dockerfile path relative to the DAG directory (default "Dockerfile")
  -h, --help                 help for compile
      --image string         container image reference for the DAG
  -o, --output string        path to write the compiled dag.json (default <project>/dag.json)
      --parser-cmd string    override the parser command (default from config)
      --platform strings     image platform to build for, os/arch (repeatable or comma-separated), over build.platforms in dexaflow.yaml, the target and the workspace defaults (default linux/amd64)
      --push                 push the built image to its registry and write its digest into dag.json (requires --build)
      --rebuild              build the dependency image again even when the registry holds one for its key (with --push)
      --registry string      image registry prefix to push to, over every other source (then DEXAFLOW_REGISTRY, dexaflow.yaml, the target, the workspace defaults, the config file)
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

