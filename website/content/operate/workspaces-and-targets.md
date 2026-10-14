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
keeps its lines. A second `init` over a workspace, or an `init` inside another
workspace, refuses without `--force`.

`dexaflow new <dag_id>` scaffolds `dags/<dag_id>/` in the workspace found from
the current folder. It never overwrites a file without `--force`, refuses a
`dag_id` another project already uses, and its next steps name the target a
deploy would go to. `dexaflow init <path>` still creates a single project
anywhere, as before.

## How commands find the workspace

Every command walks up from the current folder (symlinks resolved) to the
first `dexaflow.workspace.yaml` (or `leoflow.workspace.yaml`, the name from
before the rename). Running a command in `dags/orders_report/sql/` targets the
same workspace as running it at the root.

A workspace file owned by another user is ignored with a warning, the way git
ignores a repository it does not trust: the file decides which server your
token goes to and which registry your images go to, and a parent folder on a
shared machine is not necessarily yours. To use such a file, take ownership
of it (`chown`) or trust its folder:

- `DEXAFLOW_TRUSTED_WORKSPACES` (or `LEOFLOW_TRUSTED_WORKSPACES`): workspace
  roots separated by the OS path-list separator (`:` on Linux and macOS), or
  `*` for every workspace. A CI container that runs as root over a checkout
  the runner owns typically sets `DEXAFLOW_TRUSTED_WORKSPACES=*`.
- `trusted_workspaces:` in `~/.dexaflow/config.yaml`, a list of roots (or
  `"*"`). It is read from that default path, not from a `--config` file.

| Command | Where it reads DAGs |
|---|---|
| `dexaflow deploy <dag_id>` | The workspace root; without a workspace file, the whole git repository around the current folder (so two projects anywhere in it with the same `dag_id`, test fixtures for example, make the lookup fail: add a workspace file or pass the project's path); else the current folder when it is a DAG project; else the Lite workspace (`workspace:` in the config file, or `~/dexaflow`) |
| `dexaflow deploy --all` | The workspace root; without a workspace file, the current folder when it is a DAG project (a workspace of one). Inside a git repository with no workspace file it refuses rather than deploy every DAG of the repository; outside one it keeps the Lite workspace |
| `dexaflow deploy` with no argument | The current folder when it is a project; at the workspace root itself, every DAG of the workspace. Any other folder is an error that suggests `deploy <dag_id>` or `deploy --all` |
| `dexaflow lite` with no argument | The workspace root; `~/dexaflow` (or the config's `workspace:`) only when no workspace file is found |

Deploy prints the workspace it read and why (`workspace: <root> (from
<source>)`) before it starts.

## Targets

Inside a workspace, `dexaflow target add` writes the target into the
workspace file, so everyone who clones the repository gets it. Outside a
workspace, or with `--global`, it goes into your config file
(`~/.dexaflow/config.yaml`). The first target added to a workspace always
becomes its `default_target`, since the file is shared and must not depend on
one author's config; when your config file has a `server_url`, `target add`
says that commands run inside this workspace now use the target instead
(outside it, `server_url` still applies). The first target added to your
config file becomes `current_target` only when it has no `server_url`;
otherwise commands keep using `server_url` until you run
`dexaflow target use <name>`.

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

`dexaflow deploy` does not take that last default: with no server set by any
of the steps above it stops before building and says how to set one, unless
the machine is set up for Lite (`dexaflow setup`), where the local server is
the expected one. Pass `--server http://localhost:8080` to deploy to a local
server on purpose.

A name that matches no target is an error that lists the known ones; the
command never falls back to another server silently. `dexaflow auth login`
prints the server it signs in to, and what chose it, before it reads a
password; `dexaflow deploy` prints the server, registry, platforms and base
image it uses, each with its source, before it builds.

### Credentials

Tokens are never written to the workspace file. `dexaflow auth login` saves
one token per server URL in your config file, which is rewritten atomically at
`0600`, and every command sends a server only the token saved for it. The
token a command sends is, in order: `--token`, `DEXAFLOW_TOKEN` (or
`LEOFLOW_TOKEN`), the token saved for that server, then the single
`server_url`/`token` pair older versions saved. That pair is only sent to the
server it was saved with, whatever chose the server (`--server`,
`DEXAFLOW_SERVER_URL` or a target); calling another server without a token of
its own fails with a `401` that says so. A login without a target also keeps
writing that pair, so older tools that read it keep working.
`dexaflow target remove <name> --logout` forgets the token saved for the
target's server, the pair included when it is for that server.

A saved token is renewed in place when it nears expiry. A token from `--token`
or an environment variable is never rewritten.

## Registry, platforms and base image

A workspace states its registry once instead of in every `dexaflow.yaml`.
`deploy`, `compile` and `build` take the registry url from the first of these
that is set, so all three derive the same image:

1. `--registry`;
2. `DEXAFLOW_REGISTRY` (or `LEOFLOW_REGISTRY`), which overrides even the
   project's own value;
3. `registry.url` in the project's `dexaflow.yaml`;
4. the target's `registry`;
5. `defaults.registry` in the workspace file;
6. `registry:` in your config file.

The image name defaults to the `dag_id` (lowercased), and `deploy` prints the
registry it uses and where it came from before it builds. `build.platforms`
and `base_image` follow the same idea: the project's own value, then the
target's, then the workspace defaults, and `--platform` on `compile`, `build`
and `deploy` wins over all of them for the platform
([Builders](/operate/builders/#choosing-the-platform)). Lite applies the workspace defaults
but never a target's. With a target, the workspace is the one around the
current folder (the one the target came from), and its defaults apply only to
projects inside it.

## Cluster deploy defaults

The platform team can state the registry, the platforms and the base image
once for the whole cluster, so a repository needs no `registry:` block at all.
They set them in the Helm values:

```yaml
deploy:
  defaults:
    registry: ghcr.io/acme             # image prefix; the image name defaults to the dag_id
    platforms: [linux/amd64]
    baseImage: ghcr.io/acme/base:1
```

The server serves them on `GET /api/v2/deployDefaults` to any signed-in user
who may read DAGs, and to an API token with the `dexaflow:read` or
`dexaflow:deploy` scope, so a CI deploy token reads them too. They are the
same for every tenant of the cluster and are not secrets: registry credentials
never go there. The chart and the server refuse a value no deploy could use (a
registry with a scheme, credentials or a tag, a platform that is not
`os/arch`), so a typo fails the install instead of every deploy. Every key is
listed under [`deploy.defaults.*`](/reference/configuration/#cluster-deploy-defaults-deploydefaults).

`dexaflow deploy` reads them from the server it deploys to, once per command,
and uses each one only for what every local source leaves unset. The full
order, first match wins:

| Value | Order |
|---|---|
| registry | `--registry`, `DEXAFLOW_REGISTRY`, the project's `registry.url`, the target, the workspace `defaults`, `registry:` in your config file, **the cluster**, none (deploy stops and says how to set one) |
| platforms | `--platform`, the project's `build.platforms`, the target, the workspace `defaults`, **the cluster**, `linux/amd64` |
| base image | the project's `base_image`, the target, the workspace `defaults`, **the cluster**, the built-in base image |

The cluster comes after every local source on purpose: each of those is a
choice of the person deploying, so a cluster that starts advertising defaults
changes no deploy that already resolved a value. Before it builds, the deploy
prints each value and its source:

```
server:     https://flow-dev.acme.dev (target "dev" in /work/acme-dags/dexaflow.workspace.yaml)
registry:   ghcr.io/acme/orders_report (from the deploy defaults of https://flow-dev.acme.dev)
platforms:  linux/amd64 (from the deploy defaults of https://flow-dev.acme.dev)
base image: ghcr.io/acme/base:1 (from the deploy defaults of https://flow-dev.acme.dev)
```

A server from before this endpoint answers 404, and the deploy then resolves
exactly as it did before, without a message. Any other failure to read the
defaults (an error status, an answer that is not usable) prints a warning and
the deploy goes on with the local settings only. `compile` and `build` never
ask a server: they use the local sources alone.

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
absolute `http://` or `https://` URLs without credentials in them. Unknown keys are refused with the file
named, like in `dexaflow.yaml`. `target add`, `use --workspace-default` and
`remove` edit the file in place and keep its comments and blank lines.

## What stays in your config file

`~/.dexaflow/config.yaml` keeps everything personal: `sessions` (one token per
server), `targets` and `current_target` for targets outside workspaces,
`workspace_targets` (your `target use` choice per workspace),
`trusted_workspaces`, and the
`server_url`, `token` and `registry` keys older versions read. Other keys in
the file are left as they are.
