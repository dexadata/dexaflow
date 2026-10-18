---
aliases:
  - /cli/leoflow_status.html
  - /reference/cli/leoflow_status/
title: "dexaflow status"
linkTitle: "status"
weight: 64
---

Show per DAG the version current on a target, its last run, and drift from the workspace.

### Synopsis

Reports, for each DAG of the workspace (or the DAGs named by dag_id or project folder), the version current on the target (its label and when it was registered), whether the local content is that version (in sync), an older registered version (behind) or neither (drift), is_paused and the last run. A DAG the server does not know is "not deployed".

The local label is computed exactly as `dexaflow deploy` computes it, with the target's and the cluster's deploy defaults, so "in sync" means a deploy would build and register nothing. Pass the --registry, --platform and --dockerfile a deploy is given, since they are part of the label.

Nothing is built, pushed or registered, and nothing is asked.

Exit codes: 0 when every DAG is in sync, 1 when a DAG drifted, is behind or is not deployed, 2 when a target or a DAG could not be read.

```
dexaflow status [path | dag_id ...] [flags]
```

### Options

```
      --all-targets         report every target of the workspace, each with its own server and saved token
      --dockerfile string   the --dockerfile a deploy is given, relative to the DAG directory (default "Dockerfile")
  -h, --help                help for status
      --no-source-mode      the --no-source-mode a deploy is given: the label of a build even where source_mode_image is set
      --output string       output format: text, or json for one JSON document on stdout (default "text")
      --platform strings    image platform to build for, os/arch (repeatable or comma-separated), over build.platforms in dexaflow.yaml, the target and the workspace defaults (default linux/amd64)
      --registry string     the --registry a deploy is given, which is part of the content label (then DEXAFLOW_REGISTRY, dexaflow.yaml, the target, the workspace defaults, the config file, the cluster's deploy defaults)
      --server string       control plane base URL, over --target (default: the target in use, then server_url from the config file)
      --token string        JWT bearer token (default: the token saved for the server)
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

