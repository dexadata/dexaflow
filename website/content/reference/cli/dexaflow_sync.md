---
aliases:
  - /cli/leoflow_sync.html
  - /reference/cli/leoflow_sync/
title: "dexaflow sync"
linkTitle: "sync"
weight: 59
---

Make the target run the DAGs of a workspace: deploy what changed, report or pause what is gone (Pro).

### Synopsis

Compares the DAG projects under <dir> with the DAGs registered on the target in use and makes the target match:

  - in sync: the local content label is the current version; nothing is done.
  - drift or not deployed: the content is not registered; it is deployed exactly as `dexaflow deploy --all` deploys it (build or reuse the image, push, register). A new DAG is paused or not as the server decides for new DAGs; a redeploy never changes is_paused.
  - behind: the content is an older registered version; that version is made current again, as `dexaflow rollback --to <label>` does, with nothing built.
  - not in workspace: the target holds a DAG no project under <dir> defines. It is reported, and with --prune it is paused (reversible with `dexaflow admin dags unpause`). Nothing is ever deleted.

Targets, tokens and deploy defaults resolve as for a command run in <dir>: --target, then the target selected for the workspace around <dir>, its default_target, then the config file. The labels are computed as deploy computes them, so pass the --registry, --platform and --dockerfile a deploy is given.

--dry-run builds, pushes, registers and changes nothing, so it can run as a CI check or an Argo CD or Flux hook.

Exit codes: 0 when the target matches the workspace (--dry-run) or every change was applied; 1 when --dry-run found something to change; 2 when the target or a DAG could not be read, a change failed, or the command was used wrongly. A DAG not in the workspace counts as a change only with --prune, and only while it is not paused.

```
dexaflow sync <dir> [flags]
```

### Options

```
      --builder string      image build tool to shell out to (e.g. docker, podman, nerdctl) (default "docker")
      --dockerfile string   Dockerfile path relative to the DAG directory (default "Dockerfile")
      --dry-run             say per DAG what sync would do and change nothing; exit 1 when something would change
  -h, --help                help for sync
      --output string       output format: text, or json for one JSON document on stdout (human lines go to stderr) (default "text")
      --platform strings    image platform to build for, os/arch (repeatable or comma-separated), over build.platforms in dexaflow.yaml, the target and the workspace defaults (default linux/amd64)
      --prune               pause the DAGs the target holds and the workspace does not (never deletes)
      --registry string     image registry prefix to push to, over every other source (then DEXAFLOW_REGISTRY, dexaflow.yaml, the target, the workspace defaults, the config file, the cluster's deploy defaults)
      --server string       control plane base URL, over --target (default: the target in use, then server_url from the config file)
      --token string        JWT bearer token (default: the token saved for the server)
  -y, --yes                 skip the confirmation prompt (for automation)
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

