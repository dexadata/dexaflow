---
aliases:
  - /cli/leoflow_deploy.html
  - /reference/cli/leoflow_deploy/
title: "dexaflow deploy"
linkTitle: "deploy"
weight: 41
---

Build, push, and register a DAG to a control plane (Cluster).

```
dexaflow deploy [path | dag_id] [flags]
```

### Options

```
      --active                 leave the DAG active (unpaused) after this deploy, new or not
      --all                    deploy every DAG project in the workspace
      --builder string         image build tool to shell out to (e.g. docker, podman, nerdctl) (default "docker")
      --changed-since string   with --all, deploy only the projects whose image or version label inputs differ between this git ref and the working tree (uncommitted files included); the others are skipped and listed
      --dag-version string     DAG version label (default: git describe of the project directory, or dev outside git, plus a hash of the DAG's content)
      --dockerfile string      Dockerfile path relative to the DAG directory (default "Dockerfile")
      --follow                 follow the run: print task state changes and logs, the UI link, and exit with the run's result
      --force                  build, push and register even when the default content label is already deployed
  -h, --help                   help for deploy
      --no-source-mode         build and push an image even when a target, the workspace defaults or the cluster set source_mode_image
      --output string          output format: text, or json for one JSON document on stdout (human lines go to stderr) (default "text")
      --paused                 leave the DAG paused after this deploy, new or not (default: a new DAG follows is_paused_upon_creation, else the server's dags_are_paused_at_creation; a redeploy keeps is_paused)
      --plan                   say per DAG what would be built, pushed and registered, and why, without doing any of it
      --platform strings       image platform to build for, os/arch (repeatable or comma-separated), over build.platforms in dexaflow.yaml, the target and the workspace defaults (default linux/amd64)
      --rebuild                build and push the image even when the registry already holds one for this content label, and build its dependency image again
      --registry string        image registry prefix to push to, over every other source (then DEXAFLOW_REGISTRY, dexaflow.yaml, the target, the workspace defaults, the config file, the cluster's deploy defaults)
      --server string          control plane base URL, over --target (default: the target in use, then server_url from the config file)
      --skip-build             register the image already in the registry (no build or push; the digest is read from the registry) but still recompile dag.json from dexaflow.yaml/dag.py
      --timeout duration       with --follow, stop following after this long (e.g. 30m) and exit 3; the run keeps going (default: no limit)
      --token string           JWT bearer token (default: config token)
      --trigger                trigger a run immediately after registering
      --watch                  keep running after the deploy and redeploy each project when one of its files is saved, until Ctrl-C (asks once, applies --paused or --active to the first deploy only)
  -y, --yes                    skip the confirmation prompt (for automation)
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

