---
title: Workspaces and targets
weight: 11
description: Keep many DAGs in one repository and deploy them to named control planes, with no wrapper scripts.
---

A **workspace** is a folder, usually a git repository, that holds many DAG
projects and the facts every deploy from it shares. A **target** is a named
control plane (Lite on your laptop, a dev cluster, production). Together they
replace the shell scripts teams wrote around the CLI to remember which server,
which token and which registry go with which repository.

## From an empty folder to a deploy

```bash
dexaflow workspace init acme-dags --git     # layout, .gitignore, git init
cd acme-dags
dexaflow target add dev https://flow-dev.acme.dev --registry ghcr.io/acme
dexaflow auth login --target dev            # once; the token stays on your machine
dexaflow new orders_report                  # dags/orders_report/{dexaflow.yaml,dag.py}
dexaflow lite                               # run every DAG of the workspace locally
dexaflow deploy orders_report               # build, push and register on dev
```

`workspace init` writes:

```
acme-dags/
  dexaflow.workspace.yaml   # targets and shared defaults; commit it
  dags/                     # one folder per DAG
  .gitignore                # dag.json and Python caches
```

It works without git; `--git` only adds `git init`. An existing `.gitignore`
keeps its lines, and a second `init` over a workspace refuses without
`--force`.

`dexaflow new <dag_id>` scaffolds `dags/<dag_id>/` in the workspace found from
the current folder. It never overwrites a file without `--force`, refuses a
`dag_id` another project already uses, and its next steps name the target a
deploy would go to. `dexaflow init <path>` still creates a single project
anywhere, as before.

## How commands find the workspace

Every command walks up from the current folder to the first
`dexaflow.workspace.yaml` (or `leoflow.workspace.yaml`, the name from before
the rename). Running a command in `dags/orders_report/sql/` targets the same
workspace as running it at the root.

| Command | Where it reads DAGs |
|---|---|
| `dexaflow deploy --all`, `dexaflow deploy <dag_id>` | The workspace root; without a workspace file, the git repository around the current folder; else the current folder when it is a DAG project (a workspace of one); else the Lite workspace (`workspace:` in the config file, or `~/dexaflow`) |
| `dexaflow deploy` with no argument | The current folder when it is a project; at a workspace root, every DAG of the workspace |
| `dexaflow lite` with no argument | The workspace root; `~/dexaflow` (or the config's `workspace:`) only when no workspace file is found |

## Targets

Inside a workspace, `dexaflow target add` writes the target into the
workspace file, so everyone who clones the repository gets it. Outside a
workspace, or with `--global`, it goes into your config file
(`~/.dexaflow/config.yaml`). The first target added becomes the default.

```bash
dexaflow target add prod https://flow.acme.dev --registry ghcr.io/acme/prod
dexaflow target list                 # * marks the target in use, and why
dexaflow target use prod             # your choice for this workspace only
dexaflow target use prod --workspace-default   # change default_target for everyone
dexaflow target remove prod --logout # also forget the saved token
```

Every server command (`deploy`, `push`, `dags`, `runs`, `connections`,
`variables`, `admin`, `auth`) accepts the global `--target <name>` flag. The
control plane a command calls is, in order:

1. `--server`, then `--server-url`;
2. `--target`, then `DEXAFLOW_TARGET` (or `LEOFLOW_TARGET`);
3. `DEXAFLOW_SERVER_URL`;
4. the target chosen with `dexaflow target use` for this workspace, then the
   workspace's `default_target`, then `current_target` in your config file;
5. `server_url` in your config file, then `http://localhost:8080`.

A name that matches no target is an error that lists the known ones; the
command never falls back to another server silently.

### Credentials

Tokens are never written to the workspace file. `dexaflow auth login --target
<name>` saves one token per server URL in your config file (kept at `0600`), and
every command sends a server only the token saved for it. The token a command
sends is, in order: `--token`, `DEXAFLOW_TOKEN` (or `LEOFLOW_TOKEN`), the token
saved for that server, then the single `server_url`/`token` pair older
versions saved. That pair keeps working exactly as before, but it is only sent
to a target whose server is the one it was issued for: a target name in a
shared file can point anywhere, and a token must not follow it.

A saved token is renewed in place when it nears expiry. A token from `--token`
or an environment variable is never rewritten.

## Registry, platforms and base image

A workspace states its registry once instead of in every `dexaflow.yaml`. When
a project sets no `registry.url`, deploy, compile and build take it from, in
order:

1. `--registry` (deploy);
2. `DEXAFLOW_REGISTRY` (or `LEOFLOW_REGISTRY`);
3. `registry.url` in the project's `dexaflow.yaml`;
4. the target's `registry`;
5. `defaults.registry` in the workspace file;
6. `registry:` in your config file.

The image name defaults to the `dag_id` (lowercased), and `deploy` prints the
registry it uses and where it came from before it builds. `build.platforms`
and `base_image` follow the same idea: the project's own value, then the
target's, then the workspace defaults. Lite applies the workspace defaults
but never a target's.

## The workspace file

```yaml
schema_version: "1.0"
targets:
  dev:
    server: https://flow-dev.acme.dev
    registry: ghcr.io/acme/dev       # optional
  prod:
    server: https://flow.acme.dev
    registry: ghcr.io/acme/prod
    platforms: [linux/amd64]         # optional
    base_image: ghcr.io/acme/base:2  # optional
default_target: dev
defaults:                            # optional, for every DAG in the workspace
  registry: ghcr.io/acme
  platforms: [linux/amd64, linux/arm64]
  base_image: ghcr.io/acme/base:1
```

Target names use lowercase letters, digits, `-` and `_`. Servers must be
absolute `http://` or `https://` URLs. Unknown keys are refused with the file
named, like in `dexaflow.yaml`. `target add`, `use --workspace-default` and
`remove` edit the file in place and keep its comments.

## What stays in your config file

`~/.dexaflow/config.yaml` keeps everything personal: `sessions` (one token per
server), `targets` and `current_target` for targets outside workspaces,
`workspace_targets` (your `target use` choice per workspace), and the
`server_url`, `token` and `registry` keys older versions read. Other keys in
the file are left as they are.
