---
aliases:
  - /cli/leoflow_deploy.html
  - /reference/cli/leoflow_deploy/
title: "dexaflow deploy"
linkTitle: "deploy"
weight: 34
---

Build, push, and register a DAG to a control plane (Pro).

```
dexaflow deploy [path | dag_id] [flags]
```

### Options

```
      --all                  deploy every DAG project in the workspace
      --builder string       image build tool to shell out to (e.g. docker, podman, nerdctl) (default "docker")
      --dag-version string   DAG version label (default: git describe, else dev)
      --dockerfile string    Dockerfile path relative to the DAG directory (default "Dockerfile")
  -h, --help                 help for deploy
      --server string        control plane base URL (default: config server_url)
      --skip-build           reuse the existing image (skip docker build/push) but still recompile dag.json from dexaflow.yaml/dag.py
      --token string         JWT bearer token (default: config token)
      --trigger              trigger a run immediately after registering
  -y, --yes                  skip the confirmation prompt (for automation)
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.dexaflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [dexaflow](/reference/cli/dexaflow/)	 - Dexaflow is a GitOps-first, container-native workflow orchestrator.

