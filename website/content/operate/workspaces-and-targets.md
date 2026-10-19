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
| `dexaflow status` | As `deploy`: with no argument, the current folder when it is a project, else the DAGs `deploy --all` reads; a `dag_id` argument is looked up as for `deploy <dag_id>` |
| `dexaflow sync <dir>` | Every DAG project under `<dir>`, and the workspace file around `<dir>` for the targets, whatever the current folder; a folder with no project is refused. See [Syncing a target with the workspace](/operate/cicd-deploy/#syncing-a-target-with-the-workspace-gitops) |
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

Every server command (`deploy`, `status`, `push`, `dags`, `runs`, `connections`,
`variables`, `admin`, `auth`) accepts the global `--target <name>` flag. The
control plane a command calls is, in order:

1. `--server`, then `--server-url`;
2. `--target`, then `DEXAFLOW_TARGET` (or `LEOFLOW_TARGET`);
3. `DEXAFLOW_SERVER_URL`;
4. the target chosen with `dexaflow target use` for this workspace, then the
   workspace's `default_target`, then `current_target` in your config file;
5. `server_url` in your config file, then the local server: on a machine set
   up for Lite (`dexaflow setup`), the Lite server on the `lite_port` of your
   config file (`http://localhost:8088` unless you chose another port), else
   `http://localhost:8080`.

`dexaflow deploy` does not take that last default: with no server set by any
of the steps above it stops before building and says how to set one, unless
the machine is set up for Lite, where the local Lite server is the expected
one. Pass `--server` with the local URL (`--server http://localhost:8088` for
Lite on its default port) to deploy to a local server on purpose.

A name that matches no target is an error that lists the known ones; the
command never falls back to another server silently. `dexaflow auth login`
prints the server it signs in to, and what chose it, before it reads a
password; `dexaflow deploy` prints the server, registry, platforms and base
image it uses, each with its source, before it builds.

### Credentials

Tokens are never written to the workspace file. `dexaflow auth login` signs
in with the server's single sign-on in a browser when the server offers it
and no username or password is given, and with a password otherwise
([Signing in from the CLI](/operate/sso-google-workspace/#signing-in-from-the-cli)).
Either way it saves
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

`dexaflow promote <dag_id> --from <target> --to <target>` is the one command
that calls two targets, so it takes neither `--target` nor `DEXAFLOW_TOKEN`:
each control plane gets `--from-token` or `--to-token`, else the token saved
for its server. The `--from` token needs read on dags (an API token, the
`read` scope); the `--to` token needs only deploy. It registers on the `--to`
target the version the `--from` target runs, with the same image digest
([Promoting between environments](/operate/cicd-deploy/#promoting-between-environments)).

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

`remote_builder`, the remote builder for builds that need a real Dockerfile
build ([Cloud Build](/operate/builders/#building-on-cloud-build) or the
[in-cluster builder](/operate/builders/#building-on-the-in-cluster-builder)),
follows the same order: the
project's `build.remote_builder`, then the target's `remote_builder`, then
`defaults.remote_builder`. Targets saved in your config file
(`dexaflow target add`) and the cluster's defaults carry none.

```yaml
targets:
  prod:
    server: https://flow.acme.dev
    remote_builder:
      cloud_build: {project: acme-prod-ci, region: europe-west1}
  staging:
    server: https://flow-staging.acme.dev
    remote_builder:
      buildkit: {namespace: dexaflow-builder, context: staging}
defaults:
  remote_builder:
    cloud_build: {project: acme-ci}
```

## Cluster deploy defaults

The platform team can state the registry, the platforms, the base image and
the remote builder once for the whole cluster, so a repository needs no `registry:` block at all.
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
`os/arch`, platforms given as a string instead of a list, a base image with
whitespace, a quote, a backslash or `$`), so a typo fails the install instead
of every deploy. Every key is
listed under [`deploy.defaults.*`](/reference/configuration/#cluster-deploy-defaults-deploydefaults).

`dexaflow deploy` reads them from the server it deploys to, once per command,
and uses each one only for what every local source leaves unset. The full
order, first match wins:

| Value | Order |
|---|---|
| registry | `--registry`, `DEXAFLOW_REGISTRY`, the project's `registry.url`, the target, the workspace `defaults`, `registry:` in your config file, **the cluster**, none (deploy stops and says how to set one) |
| platforms | `--platform`, the project's `build.platforms`, the target, the workspace `defaults`, **the cluster**, `linux/amd64` |
| base image | the project's `base_image`, the target, the workspace `defaults`, **the cluster**, the built-in base image |
| source mode runtime image | `--no-source-mode` (builds), the target's `source_mode_image`, the workspace `defaults` (`none` clears it: the deploy builds), **the cluster** (`deploy.defaults.sourceMode`, only when no local source set a registry; a local image that differs is refused), none (the deploy builds) |
| remote builder | the project's `build.remote_builder`, the target's `remote_builder`, the workspace `defaults`, **the cluster** (`deploy.defaults.remoteBuilder`), none (the local daemon) |

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

With `deploy.defaults.sourceMode: true` (which needs source mode on), the
cluster also advertises its runtime image as `source_mode_image`, and a deploy
that names none of its own and resolves no registry locally registers
`dag.py` on it with no build or push
([Deploying in source mode](/operate/source-mode/#deploying-in-source-mode)).

### Remote builder, task service account and warm pools

The cluster can also name the remote builder for builds no daemonless path
covers ([Builders](/operate/builders/)), and tell a deploy which
ServiceAccount its tasks run as and what its warm pool will be:

```yaml
deploy:
  defaults:
    remoteBuilder:
      cloudBuild:
        project: acme-ci        # required with any other cloudBuild value
        region: us-central1
    taskServiceAccount: true    # advertise taskServiceAccount.name
    warmPools: true             # advertise execution.warmPoolsEnabled, minIdleWorkers, maxPoolSize
```

The remote builder is a default like the others: a project, target or
workspace that names its own keeps it, and the builder line of the deploy
names where the one it used came from (`the remote builder from the deploy
defaults of https://flow-dev.acme.dev builds the whole image`). The person
deploying still signs in with `gcloud` and needs the Cloud Build role on that
project.

A cluster that runs the [in-cluster builder](/operate/builders/#building-on-the-in-cluster-builder)
(`builder.enabled`) advertises it instead, with no `remoteBuilder` value: the
chart sets its namespace and pod selector, unless `builder.advertise` is
false. The chart refuses both at once. The person deploying needs `buildctl`
and `kubectl` pointed at the cluster, and a binding in `builder.subjects`;
without them the deploy says why and builds with the local daemon.

The task service account and the warm pools are the server's own settings,
served as they are when their switch is on, so nothing is copied by hand. The
deploy only reports them, and writes neither into `dag.json`: the server
already applies its task ServiceAccount to every task that sets no
`execution.service_account`, and its warm pool settings to every version.

```
task SA:    dexaflow-task (from the deploy defaults of https://flow-dev.acme.dev), for tasks that set no execution.service_account
warm pool:  on, 2 idle workers kept for the current version (min_idle_workers from /work/acme-dags/dags/orders_report/dexaflow.yaml)
```

The warm pool line resolves the number the way the server does: the
project's `min_idle_workers` when it sets one, else the cluster's
`execution.minIdleWorkers`, capped at `execution.maxPoolSize` (the line then
says so). With warm pools off it says every task runs on a dedicated pod, and
that a `min_idle_workers` the project sets keeps no worker warm.
`taskServiceAccount: true` needs a task ServiceAccount on the server
(`taskServiceAccount.create`); the chart and the server refuse it otherwise.

A server from before this endpoint answers 404, and the deploy then resolves
exactly as it did before, without a message. Any other failure to read the
defaults (an error status, an answer that is not usable) prints a warning and
the deploy goes on with the local settings only.

`build --push` reads them too, from the server a deploy would go to (the
target in use, `DEXAFLOW_SERVER_URL` or `server_url`), the same way, because
the content label hashes the resolved platforms and base image: an image it
pushes then carries the label `deploy --skip-build` to that server looks up.
With no server configured it uses the local sources alone. `compile` and a
`build` without `--push` never ask a server, so on a cluster that advertises
platforms or a base image their label can differ from the deploy's. A deploy
sent with another `--server` or `--target` than the one `build --push` used
can resolve other cluster defaults, and then looks up another label.

## Status of the workspace on a target

`dexaflow status` says, for each DAG of the workspace, what the target runs
and whether your working tree is what it runs. It only reads: it never
builds, pushes or registers anything, and never asks a question, so it is
safe in CI and in a pre-merge check.

```bash
dexaflow status                          # every DAG, on the target in use
dexaflow status orders_report            # one DAG, by dag_id or project folder
dexaflow status --target prod            # another target
dexaflow status --all-targets            # every target of the workspace
dexaflow status --output json            # one JSON document on stdout
```

```
workspace: /work/acme-dags

target dev: https://flow-dev.acme.dev (target dev, from default_target in /work/acme-dags/dexaflow.workspace.yaml)
  DAG            STATUS                                    CURRENT VERSION           REGISTERED        PAUSED  LAST RUN
  billing        drift: local changes not deployed         v1.4.0-9f2c41a0be11 (#7)  2026-10-12 09:15  no      scheduled__2026-10-14 success 2026-10-14 00:00
  orders_report  in sync                                   v1.4.0-03ccb2f38b60 (#3)  2026-10-10 14:02  yes     none
  refunds        behind: local equals v1.3.0-5af5b6c549c7  v1.4.0-27226eab1280 (#5)  2026-10-11 08:40  no      manual__1760000000 failed 2026-10-11 09:01
  shipping       not deployed                              -                         -                 -       -

1 in sync, 1 behind, 1 drift, 1 not deployed
```

The DAGs are the ones `deploy` would read: with no argument, the project in
the current folder, else every DAG of the workspace (as `deploy --all`); each
argument is a project folder or a `dag_id`, looked up as `deploy <dag_id>`
does. For each DAG it reports:

| Column | What it is |
|---|---|
| STATUS | `in sync`: the local content label is the version new runs use, so a deploy would build and register nothing. `behind: local equals <label>`: the local content is an older registered version; `dexaflow rollback <dag_id> --to <label>` makes it current again. `drift: local changes not deployed`: the local content is no registered version; a deploy registers it. `not deployed`: the server does not know the DAG (or holds no current version of it). |
| CURRENT VERSION | The label and number of the version new runs use. |
| REGISTERED | When that version was registered. |
| PAUSED | The DAG's `is_paused`. |
| LAST RUN | The run with the latest logical date: its id, state and start time, or `none`. |

The local label is computed by the same code as `dexaflow deploy`, with the
target's, the workspace's and the cluster's deploy defaults, so `status` and
`deploy` cannot disagree. Two consequences follow from what the label
covers. A DAG deployed with an explicit `--dag-version` shows as drift,
since only a content label says anything about content. And the label starts
with `git describe` of the project, so a new commit moves it even when the
DAG's files did not change, exactly as it does for a deploy. A deploy given
`--registry`, `--platform` or `--dockerfile` labels with them; pass the same
flags to `status` (or set `DEXAFLOW_REGISTRY`), since they are part of the
label.

`--all-targets` repeats the report for every target of the workspace file,
each with its own server and only the token saved for that server: the
`token` older versions saved without a `server_url` is not sent, and neither
is a `token` saved for another server. A target that cannot be reached, or
answers `401` (no token, or a refused one), gets one error line that says how
to sign in to it, and the others are still reported. A `403` is reported on
each DAG it applies to, since a role can allow some DAGs and not others. It
refuses `--server`, `--target`, `--token` and `DEXAFLOW_TOKEN`, which would
point every target at one server or send one token to all of them.

The reads are the Airflow-compatible `GET /api/v2/dags/{dag_id}/details`
(the current label, `bundle_version`, and `is_paused`), every page of
`GET /api/v2/dags/{dag_id}/dagVersions` and
`GET /api/v2/dags/{dag_id}/dagRuns`, plus `GET /api/v2/deployDefaults`: the
same reads deploy's check before a build makes, so the two agree on which
version is current. A DAG on the server that is not in the workspace is not
listed.

### Exit codes

| Code | Meaning |
|---|---|
| `0` | Every DAG is in sync on every target reported. |
| `1` | At least one DAG drifted, is behind or is not deployed, and everything could be read. |
| `2` | A target or a DAG could not be read (unreachable server, a `401`, a read the server answered with an error, a project whose label cannot be computed), or the command was used wrongly. |

In CI, `dexaflow status --target prod` fails the job when the branch is not
what production runs.

### JSON output

`--output json` prints one document on stdout. Its fields only ever grow:

```json
{
  "workspace": "/work/acme-dags",
  "targets": [
    {
      "target": "dev",
      "server": "https://flow-dev.acme.dev",
      "source": "target dev, from default_target in /work/acme-dags/dexaflow.workspace.yaml",
      "dags": [
        {
          "dag_id": "refunds",
          "path": "/work/acme-dags/dags/refunds",
          "local_version": "v1.3.0-5af5b6c549c7",
          "state": "behind",
          "current_version": {"label": "v1.4.0-27226eab1280", "version_number": 5, "created_at": "2026-10-11T08:40:00Z"},
          "matched_version": {"label": "v1.3.0-5af5b6c549c7", "version_number": 4, "created_at": "2026-10-01T10:00:00Z"},
          "is_paused": false,
          "last_run": {"run_id": "manual__1760000000", "state": "failed", "start_date": "2026-10-11T09:01:00Z"}
        }
      ]
    }
  ],
  "summary": {"in_sync": 0, "behind": 1, "drift": 0, "not_deployed": 0, "errors": 0},
  "exit_code": 1
}
```

`state` is one of `in_sync`, `behind`, `drift`, `not_deployed` and `error`
(with an `error` field). `current_version`, `is_paused` and `last_run` are
`null` when the server has none; `matched_version` is present only for
`behind`. A target that could not be read has an `error` field and an empty
`dags` list. `workspace` is empty for DAGs named by folder outside a
workspace, and `target` is empty when no target chose the server (`--server`,
`DEXAFLOW_SERVER_URL`, `server_url`).

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
  lab:
    server: https://flow-lab.acme.dev
    # optional: deploy dag.py on this runtime image, with no build (source mode)
    source_mode_image: ghcr.io/dexadata/dexaflow-runtime:py3.11-{{< latest-version v >}}@sha256:<digest>
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
