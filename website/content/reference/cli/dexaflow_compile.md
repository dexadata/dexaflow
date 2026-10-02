---
aliases:
  - /cli/leoflow_compile.html
  - /reference/cli/leoflow_compile/
title: "dexaflow compile"
linkTitle: "compile"
weight: 17
---

Compile a DAG project into dag.json via the Python parser.

```
dexaflow compile [path] [flags]
```

### Options

```
      --build                build the DAG container image (requires --image)
      --builder string       image build tool to shell out to (e.g. docker, podman, nerdctl) (default "docker")
      --dag-version string   DAG version label (default: git describe, else dev)
      --dockerfile string    Dockerfile path relative to the DAG directory (default "Dockerfile")
  -h, --help                 help for compile
      --image string         container image reference for the DAG
  -o, --output string        path to write the compiled dag.json (default <project>/dag.json)
      --parser-cmd string    override the parser command (default from config)
      --push                 push the built image to its registry (requires --build)
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.dexaflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [dexaflow](/reference/cli/dexaflow/)	 - Dexaflow is a GitOps-first, container-native workflow orchestrator.

