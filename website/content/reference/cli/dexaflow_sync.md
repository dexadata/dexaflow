---
aliases:
  - /cli/leoflow_sync.html
  - /reference/cli/leoflow_sync/
title: "dexaflow sync"
linkTitle: "sync"
weight: 65
---

Make the target run the DAGs of a workspace: deploy what changed, report or pause what is gone (Cluster).

### Synopsis

Compares the DAG projects under <dir> with the DAGs registered on the target in use and makes the target match:

  - in sync: the local content label is the current version; nothing is done.
  - drift or not deployed: the content label is not registered; the DAG is deployed exactly as `dexaflow deploy --all` deploys it (build or reuse the image, push, register). A new DAG is paused or not as the server decides for new DAGs; a redeploy never changes is_paused.
  - behind: the content label is a registered version older than the current one, so the target runs something newer than this checkout (it was deployed from a newer commit). It is reported and left alone; with --rollback that older version is made current again, as `dexaflow rollback --to <label>` does, with nothing built and is_paused kept.
  - not in workspace: the target holds a DAG no project under <dir> defines. It is reported, and with --prune it is paused (reversible with `dexaflow admin dags unpause`). Nothing is ever deleted. Every DAG the token can see on the target is a candidate: sync one folder per target.

--prune applies nothing while a project cannot be read (a dexaflow.yaml that does not parse hides its dag_id), or when it would pause more DAGs than --max-prune; without a terminal it needs --yes.

Targets, tokens and deploy defaults resolve as for a command run in <dir>: --server, --target, then the target selected for the workspace around <dir>, its default_target, then the config file. The labels are computed as deploy computes them (git describe, the content and the CLI version), so pass the --registry, --platform and --dockerfile a deploy is given.

--dry-run builds, pushes, registers and changes nothing, so it can run as a CI check or an Argo CD hook.

Exit codes: 0 when the target matches the workspace (--dry-run) or every change was applied; 1 when --dry-run found something to change, or a behind DAG was left alone without --rollback; 2 when the target or a project could not be read, a change failed or was refused, the confirmation was declined, or the command was used wrongly. A DAG not in the workspace counts as a change only with --prune, and only while it is not paused.

```
dexaflow sync <dir> [flags]
```

### Options

```
      --builder string      image build tool to shell out to (e.g. docker, podman, nerdctl) (default "docker")
      --dockerfile string   Dockerfile path relative to the DAG directory (default "Dockerfile")
      --dry-run             say per DAG what sync would do and change nothing; exit 1 when something would change
  -h, --help                help for sync
      --max-prune int       with --prune, apply nothing when more than this many DAGs would be paused (default: the number of DAG projects under <dir>) (default -1)
      --output string       output format: text, or json for one JSON document on stdout (human lines go to stderr) (default "text")
      --platform strings    image platform to build for, os/arch (repeatable or comma-separated), over build.platforms in dexaflow.yaml, the target and the workspace defaults (default linux/amd64)
      --prune               pause the DAGs the target holds and the workspace does not (never deletes)
      --registry string     image registry prefix to push to, over every other source (then DEXAFLOW_REGISTRY, dexaflow.yaml, the target, the workspace defaults, the config file, the cluster's deploy defaults)
      --rollback            make current again the older registered version a behind DAG's content equals (default: report it and leave it)
      --server string       control plane base URL, over --target (default: the target in use, then server_url from the config file)
      --token string        JWT bearer token (default: DEXAFLOW_TOKEN, then the token saved for the server)
  -y, --yes                 skip the confirmation prompt (for automation; required for --prune without a terminal)
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

