---
aliases:
  - /cli/leoflow_promote.html
  - /reference/cli/leoflow_promote/
title: "dexaflow promote"
linkTitle: "promote"
weight: 52
---

Register on one target the exact version another target runs (same digest, no build).

### Synopsis

Reads a registered version of the DAG from the --from target (its current version, or the one --version names: a label first, then a version number from `dexaflow dags versions`) and registers the same dag.json, with the same image digest, on the --to target. Nothing is built.

The image must be pinned by digest. When the destination pulls from another registry (--registry, else the registry of the --to target, else the deploy defaults its control plane advertises), the image is copied to that registry by digest, byte for byte, keeping its repository path, and the version registered with the copy (a tag that already names another digest there is never moved); otherwise the same reference is registered.

The destination runs the register's dry run first, so a refusal stops the promote before any copy. A DAG the promote creates starts paused as a deploy's would, and an existing DAG keeps is_paused. The register's audit entry records the source target and server.

Each control plane gets its own token: --from-token and --to-token, else the session saved for its server by `dexaflow auth login --target <name>`. DEXAFLOW_TOKEN is not used, so one token never goes to two control planes. The source side reads each version's spec, which needs read on dags (an API token with the read scope); the destination side needs only what a deploy needs.

```
dexaflow promote <dag_id> --from <target> --to <target> [flags]
```

### Options

```
      --dry-run             say what would be copied and registered, with the destination's dry run of the register, without doing it
      --from string         target to read the version from (required)
      --from-token string   bearer token for the --from control plane (default: the session saved for its server)
  -h, --help                help for promote
      --output string       output format: text, or json for one JSON document on stdout (human lines go to stderr) (default "text")
      --registry string     registry prefix the destination pulls from, over the --to target's registry and its cluster's deploy defaults
      --to string           target to register the version on (required)
      --to-token string     bearer token for the --to control plane (default: the session saved for its server)
      --version string      version to promote: matched first as a label, then as a version number (default: the source's current version)
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

