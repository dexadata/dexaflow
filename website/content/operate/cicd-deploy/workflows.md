---
title: "Change, roll back and promote"
linkTitle: "Change, roll back, promote"
weight: 10
description: "Deploy only what changed, roll back, find where a version was built, sync a target with GitOps, promote between environments and require digest pins."
---

How a pipeline built on [`dexaflow deploy`](/operate/cicd-deploy/) deploys only what changed, rolls back, records provenance, syncs a target and promotes between environments.

## Deploying only what changed

In a repository with many DAG projects, `deploy --all --changed-since <ref>`
deploys only the projects whose files differ from a git ref, so a commit that
touches one DAG does not compile the others:

```bash
dexaflow deploy --all --changed-since origin/main --yes
```

```text
==> orders: dags/orders/dag.py changed since origin/main
==> skipping billing (unchanged since origin/main)
==> deploying orders (/repo/dags/orders)
...
Deployed 1 DAG(s) from /repo (1 unchanged since origin/main, skipped)
```

A project counts as changed when a file its image or its version label is
built from changed: the DAG source, the `include_paths` (shared code included),
the dbt project and `dbt_groups` directories, `dbt.manifest`, its
`dexaflow.yaml` (or `leoflow.yaml`), its `.dockerignore`, and its own
Dockerfile (with a project Dockerfile, every file of the project directory the
`.dockerignore` keeps). These are the files the content label hashes, so a
skipped project is one whose label would not change. A file that `exclude_paths`
leaves out, or that the generated Dockerfile does not copy (a `README.md` next
to `dag.py`, say), changes nothing. A change to the workspace file
(`dexaflow.workspace.yaml`, which holds the shared defaults and the targets)
counts as a change to every project.

How the comparison works:

- **The ref is compared with the working tree**, not with `HEAD` and not from a
  merge base: committed, staged, unstaged and untracked files all count, and so
  do submodules, whatever `diff.ignoreSubmodules` says. A change on the ref's
  side since your branch started counts too, so the comparison errs toward
  deploying.
- **A file git ignores cannot be compared.** A project built from one (a
  generated module under `include_paths`, dbt `deps` output) always counts as
  changed, and the output names the file. Inside a submodule, the submodule's
  own ignore rules decide. So does a project built from a file
  outside the repository, or one whose folder resolves outside it.
- **The ref must be in the clone.** A shallow clone (the default of
  `actions/checkout`) stops with a message saying to fetch the history: use
  `fetch-depth: 0`, or `git fetch --unshallow`. A ref that does not exist, or a
  folder outside a git repository, stops the deploy too; nothing is deployed
  by guess.
- **A project removed since the ref** (its `dexaflow.yaml` or `dag.py` deleted)
  is reported with its `dag_id`, and nothing is deployed or deregistered for
  it: its DAG stays registered and scheduled until you pause it with
  `dexaflow admin dags pause <dag_id>` (or every such DAG at once with
  [`dexaflow sync <dir> --prune`](#syncing-a-target-with-the-workspace-gitops))
  or remove it with `dexaflow dags delete <dag_id> --deregister`. A project moved to another
  folder keeps its `dag_id`: it is reported as moved, with no removal advice,
  and deployed from its new folder. A `dag_id` changed in place is not reported:
  the project is deployed under its new id, and the DAG under the old id stays
  registered until you remove it.
- **It selects among the workspace's projects**, so it goes with `--all`, or a
  bare `dexaflow deploy` at the workspace root. With a `dag_id` or a path it is
  refused, rather than silently skipping the DAG you named.
- What git does not see is not compared: the CLI version, `--registry`,
  `--platform`, the target's settings and the config file. After upgrading the
  CLI or changing a target, deploy with `--all` alone once.

With `--output json`, every skipped project has an entry with `status:
"skipped"` and its `reason`, the removed ones are listed under `removed`
(`path`, `dag_id`, `reason`), and the moved ones under `moved` (`dag_id`,
`from`, `to`).

`--changed-since` and the content label solve different costs. Without the
flag, `deploy --all` already registers nothing for a project whose label is the
current version, and reuses an image the registry holds for its label
([ADR 0076](/project/adrs/0076-reuse-image-by-content-label/)); but it still
compiles every project and asks the server about each. `--changed-since`
skips those projects before any of that. Use it in the pipeline of a large
repository, on every push; use `--all` alone when the inputs git does not see
changed (above), or to check that every project is deployed.

The pipeline [`dexaflow ci init`](/operate/cicd-deploy/pipelines/#writing-the-pipeline-dexaflow-ci-init)
writes does this on every push to `main`, and the generated files are under
[Examples](/operate/cicd-deploy/pipelines/#examples-what-dexaflow-ci-init-writes). What it passes to
`--changed-since` decides what a failed or cancelled run leaves behind:

- **GitHub** passes the commit of the last successful run of the workflow on
  the branch (`gh run list --status success`). A run that failed, or one the
  `concurrency:` group cancelled while it waited, leaves that commit where it
  was, so the next run deploys its changes too. With no successful run yet,
  every DAG is deployed.
- **GitLab** passes the commit of the last successful push pipeline on the
  branch when the job has `DEXAFLOW_GITLAB_API_TOKEN`, a project access token
  with the `read_api` scope: the job token cannot read the pipelines API.
  Without it, the job passes the commit the branch was on before the push
  (`$CI_COMMIT_BEFORE_SHA`), and a failed, cancelled or skipped pipeline is
  not caught up by the next push, which compares with its own previous commit:
  rerun the failed pipeline, or run one by hand, which deploys every DAG. Turn
  off "Auto-cancel redundant pipelines" for the branch and set the process mode
  of the `dexaflow-deploy` resource group to `oldest_first`, so pipelines
  deploy in push order.
- **Cloud Build** has no previous commit to pass: it deploys every DAG, unless
  you set `_DEXAFLOW_BASE_REF` on a build whose source is a git clone.

Every system deploys all the DAGs when the base commit is all zeros (a new
branch) or missing from the clone (after a force push).

In a pipeline of your own, compare with the last commit that deployed rather
than with the previous push: move a ref after each successful deploy (for
example `git push -f origin HEAD:refs/tags/deployed-prod` in a last step) and
pass `--changed-since deployed-prod`, with a `concurrency:` group on the job so
two runs never move it out of order.

## Rolling back

A rollback makes a version the control plane already holds current again. It
rebuilds nothing: the version runs the image digest it was registered with.

```bash
dexaflow dags versions my_pipeline                 # what is registered, and which is current
dexaflow rollback my_pipeline                      # back to the version before the current one
dexaflow rollback my_pipeline --to v1.4.0-3fa2c1   # or to a label (or a version number) from the list
```

`dexaflow dags versions` lists each version, newest first: its number, its
label, a `*` on the current one, when it was registered, the id of the user
who registered it (`dexaflow admin users list` maps ids to emails), the
commit it was built from (see [Where a version was built](#where-a-version-was-built))
and the short image digest. `--to` matches a label first, then a version number. `dexaflow rollback` prints the version it moved from and
the one it moved to, and takes `--server` and `--token` like the other
commands. Both read and write through the API: the listing is
`GET /api/v2/dags/{dag_id}/dagVersions`, and the rollback is
`PUT /api/v2/dags/{dag_id}/currentVersion` with `{"version": "<label>"}` or
`{"version_number": <n>}`, which needs the same permission as registering a
version (scope `dexaflow:deploy` for a scoped token).

A rollback passes the checks a register of the version would pass today.
Under `deploy.require_digest: enforce`, a version registered with a tag
cannot be made current again: redeploy its sources with `dexaflow deploy`, or
set the setting to `warn` (see
[Require images pinned by digest](#require-images-pinned-by-digest)).

What a rollback changes, in one transaction:

- **New runs** use the version made current, from the next scheduler tick.
- **The schedule** and the other DAG settings (catchup, start date, max active
  runs, description, owner, tags) become that version's.
- **Runs already created keep their version**, including runs in flight.
- **Pause state does not change.** A paused DAG stays paused, an unpaused one
  stays unpaused.

What the UI shows after a rollback follows Airflow, which has no rollback and
always runs its newest version:

- **The DAG header's Latest Dag Version** and the version the **Code** tab
  opens on are still the newest version, not the current one. The Code tab's
  version picker shows the source registered with each version
  (`GET /api/v2/dagSources/{dag_id}?version_number=<n>`; without
  `version_number` or with `0`, the newest version's source).
- **A run, and each of its task instances, shows the version it ran with**, so
  a run created after the rollback shows the version made current, and its
  Code tab opens on that version's source.
- **The Clear dialog offers "Run with latest bundle version"** even on a run
  that already uses the current version, because the UI compares the run's
  version with the newest one. In Dexaflow that option re-pins the run to the
  current version, so on such a run it changes nothing. On a finished run
  whose version adds or drops a task, the clear creates the added tasks' task
  instances and marks the dropped ones `removed`. A run that is still running
  keeps its version, and the API response says the option was not applied
  (the dialog does not show it): see
  [Clearing a run that is still running](/author-dags/dag-authoring/#clearing-a-run-that-is-still-running).
- **Which version is current** is the `*` in `dexaflow dags versions`, and the
  `is_current` field of each version in `GET /api/v2/dags/{dag_id}/dagVersions`
  and of `latest_dag_version` in the DAG details. The MCP `dag://source`
  resource serves the current version's source, as `dag://spec` serves its
  spec.

The version must still pass what a register of it would pass today: the
operator's execution policy, the tenant limits, and its declared Variables and
Connections. A version that no longer does is refused with `400` (or `403` for
a tenant limit), and the current version stays. Naming the version that is
already current changes nothing. Every rollback is recorded in the audit log as
`dag.version.set_current`, with the user and the from and to versions.

Two things that look like a rollback are not:

- Re-running `dexaflow push` with an older `dag.json` is a no-op: content the
  control plane already has answers `created: false`, and the newer version
  stays current, schedule included.
- Retagging an image in the registry changes nothing: a version registered by
  `deploy` names its image by digest.

`dexaflow deploy` of code that matches a version registered earlier (after a
`git revert`, for example) stops before building and suggests the
`dexaflow rollback <dag_id> --to <label>` command for that version.

## Where a version was built

Every `dexaflow deploy` and `dexaflow push` records where the version was
built. `GET /api/v2/dags/{dag_id}/dagVersions` (and `.../dagVersions/{n}`, and
the answer of a rollback) returns it as `build_info` next to Airflow's fields,
`null` for a version registered without it; the `dag_version` embedded in task
instance, run and DAG details answers leaves it out:

| Field | Where the CLI reads it |
|---|---|
| `commit` | `git rev-parse HEAD` in the project directory, when it is in a git work tree. |
| `repo` | The `origin` remote of that work tree, as an `https` URL: credentials and a trailing `.git` are dropped, and an ssh remote (`git@host:org/repo.git`) becomes `https://host/org/repo`. Outside git, `GITHUB_SERVER_URL`/`GITHUB_REPOSITORY` or GitLab's `CI_PROJECT_URL`. |
| `pipeline_url` | The CI run: on GitHub Actions `GITHUB_SERVER_URL/GITHUB_REPOSITORY/actions/runs/GITHUB_RUN_ID`, else `CI_PIPELINE_URL` (GitLab), `BUILD_URL` (Jenkins), `BUILDKITE_BUILD_URL` or `CIRCLE_BUILD_URL`, without a query string or fragment. |
| `builder` | The builder that produced the image, as the deploy prints it (`code-layer`, `local-daemon (docker)`, ...). Absent when the deploy built nothing: with `--skip-build`, and when it reused an image the registry already held for this content label. |
| `base_image` | The base image of the generated Dockerfile, which carries the task agent. Absent when the project ships its own Dockerfile, and when the deploy built nothing (`--skip-build`, or a reused image). |
| `cli_version` | The version of the CLI that built and registered the version. |

What cannot be read is left out; a deploy never fails for it. `dexaflow promote`
forwards the source version's `build_info` to the destination.

Build info is metadata of the register, not part of `dag.json`: it changes
neither the spec, its hash nor the version label, so the same sources built by
two pipelines still compile to one version. A version is immutable, so a
re-push of content already registered creates nothing and keeps the build info
of its first register. The CLI sends it as the `build_commit`, `build_repo`,
`build_pipeline_url`, `build_builder`, `build_base_image` and
`build_cli_version` query parameters of
`POST /api/v2/dags/{dag_id}/versions`; another client can send them too. The
server checks them as external input (a commit is 7 to 64 lower case hex
characters, the two URLs are absolute `http` or `https` URLs of at most 2048
bytes without credentials, query string or fragment, every field is valid
UTF-8 without control characters) and
refuses a bad value with `400`. The register's `dag.version.register` audit
entry records the same `build_info`. It is the caller's claim, recorded as
sent and not verified: a deploy token can write any commit there.

## Syncing a target with the workspace (GitOps)

`dexaflow sync <dir>` makes the target in use run what a folder of DAG
projects holds, the way Argo CD or Flux make a cluster match a repository. It
compares every project under `<dir>` with the DAGs registered on the target,
says per DAG what it does, and does it:

```bash
dexaflow sync ./dags --dry-run --prune     # what would change; exit 1 if anything would
dexaflow sync ./dags --prune --yes         # make the target match
```

```text
workspace: /repo/dags
target prod: https://cluster.example.com (target prod, from --target)

  DAG      STATE             CURRENT VERSION             PAUSED  ACTION
  billing  drift             v1.3.0-4-g77aa01-1f0c (#4)  no      deploy v1.4.0-3fa2c1
  intake   not deployed      -                           -       deploy v1.4.0-9b0e12
  orders   behind            v1.4.1-c41d2e (#7)          no      none: registered content is newer than the workspace; run with --rollback to make v1.4.0-0d11f9 current
  reports  in sync           v1.4.0-5e6f70 (#2)          no      none
  legacy   not in workspace  -                           no      pause
  old_etl  not in workspace  -                           yes     none: already paused

dry run: 3 change(s) would be made (2 deploy, 1 pause); 1 behind, left as is (pass --rollback)
```

| State | What it means | What sync does |
|---|---|---|
| `in sync` | The local content label is the current version. | Nothing. |
| `drift` | The local content label is no registered version: the files changed, or the commit did (the label starts with `git describe`). | Deploys it. |
| `not deployed` | The server does not know the DAG, or holds no current version of it. | Deploys it. |
| `behind` | The local content label is a registered version older than the current one: the target was deployed from a newer commit than this checkout. | Reports it and leaves it. With `--rollback`, makes that version current again, as `dexaflow rollback --to <label>` does: nothing is built and `is_paused` is kept. |
| `not in workspace` | The target holds a DAG no project under `<dir>` defines. | Reports it; with `--prune`, pauses it. |
| `error` | The project could not be read (its `dexaflow.yaml` does not parse, say) or its DAG could not be read. | Nothing; the exit code is 2. |

A `git revert` does not make a DAG `behind`: the revert is a new commit, so
`git describe` and the label change, and the DAG is `drift` and deployed again
under the new label. A DAG is `behind` when the checkout itself is older than
what was deployed, for example when Argo CD syncs an earlier revision.

The comparison is the one [`dexaflow status`](/operate/workspaces-and-targets/)
makes, and the label is the one `dexaflow deploy` computes, so "in sync" means
a deploy would build and register nothing. What goes into the label decides
when sync agrees with the job that deployed:

- **`git describe` of the project.** A new commit moves the label even for a
  DAG whose files did not change: after each commit, `sync` deploys every DAG
  once, as `deploy --all` would. Run anywhere else (a hook, a second clone),
  sync must see the same history and tags as the job that deployed, or every
  DAG reads as `drift`.
- **The CLI version.** Pin the same `dexaflow` version in CI and in the hook;
  after an upgrade every DAG reads as `drift` once.
- **`--registry`, `--platform` and `--dockerfile`**, and the target's, the
  workspace's and the cluster's deploy defaults. Pass `sync` the flags a
  deploy is given.
- **Only the default label.** Sync deploys with the content label. A DAG
  deployed with `--dag-version` never reads as `in sync`, and sync would
  register it again under the content label: a team that labels its versions
  by hand should keep using `deploy` and use `sync --dry-run --prune` only to
  find the DAGs to retire.

How each change is made:

- **Deploys** go through the same code as `dexaflow deploy --all`: compile,
  build and push (or reuse the image the registry already holds for the
  content label), pin the digest, register. A new DAG is paused or active as
  the server decides for new DAGs (`is_paused_upon_creation`, then
  `dags_are_paused_at_creation`); a redeploy never changes `is_paused`.
- **Rollbacks** (`--rollback` only) make the matching version current with
  `PUT /api/v2/dags/{dag_id}/currentVersion`, as in [Rolling back](#rolling-back).
- **`--prune` pauses**, it never deletes: `PATCH /api/v2/dags/{dag_id}` with
  `is_paused: true`, undone with `dexaflow admin dags unpause <dag_id>`. A DAG
  that comes back to the workspace later is deployed again but stays paused,
  since a deploy never changes `is_paused`: unpause it yourself. To remove a
  DAG for good, use `dexaflow dags delete <dag_id> --deregister`.
- A change that fails is reported on its DAG and the other changes still go
  through.
- At a terminal, sync shows the plan and asks before it changes anything
  (`Apply 2 deploys and 1 pause on https://cluster.example.com? [y/N]`); `--yes`
  skips the question. `--output json` never asks: pass `--yes` or `--dry-run`.

What `--prune` guards against:

- **Every DAG the token can see is a candidate.** Nothing marks which
  workspace deployed a DAG yet, so every DAG on the target that no project
  under `<dir>` defines is `not in workspace`, and `--prune` pauses a DAG
  deployed to the same target from another repository. Sync one folder per
  target (or per tenant), the folder that holds all of that target's DAGs.
- **A project sync cannot read stops it.** A `dexaflow.yaml` that does not
  parse hides the project's `dag_id` (a project with a `dag.py` is found under
  its folder name, a dbt project not at all), so its DAG would read as
  `not in workspace`. With `--prune`, a project in `error` makes sync apply
  nothing at all and exit 2; without `--prune` the other changes go through.
- **A cap.** With `--prune`, sync applies nothing when it would pause more
  DAGs than `--max-prune` (by default, the number of projects under `<dir>`).
- **A confirmation.** Without a terminal, `--prune` needs `--yes`.
- **The DAG list.** With `--prune`, a list of the target's DAGs that cannot
  be read changes nothing; without it, that is a warning and the deploys and
  rollbacks go through.
- **An empty folder** is refused, so a wrong path never reads as "nothing
  should run".

Targets resolve as for a command run in `<dir>`, whatever folder sync runs
in: `--server`, `--target`, `DEXAFLOW_TARGET`, `DEXAFLOW_SERVER_URL`, the
target selected for the workspace around `<dir>` (`dexaflow target use`), its
`default_target`, then `server_url` in the config file. The token is
`--token`, then `DEXAFLOW_TOKEN`, then the one saved for that server, and the
token decides the tenant. Like a deploy, sync refuses to guess the built-in
`http://localhost:8080`.

**Credentials.** A [deploy token](/operate/cicd-deploy/credentials/#credentials-for-the-control-plane) can
read, deploy and roll back, so it is enough for `sync`, `sync --rollback` and
`sync --dry-run --prune`. Pausing needs `write` on DAGs (and the
`dexaflow:run` scope for a scoped token), which the `deployer` role and a
deploy token do not have: `sync --prune` with one reports each pause as
failed (`403`) and exits 2.

Exit codes, for a CI check or a hook:

| Code | Meaning |
|---|---|
| `0` | The target matches the workspace (`--dry-run`), or every change was applied. |
| `1` | `--dry-run` found something to change, or a `behind` DAG was left alone (no `--rollback`). |
| `2` | The target or a project could not be read, a change failed, `--prune` refused to apply, the confirmation was declined, or the command was used wrongly. |

A DAG that is `not in workspace` counts as a change only with `--prune`, and
only while it is not paused, so `sync --dry-run` without `--prune` passes on a
target that also runs DAGs from elsewhere.

`--output json` prints one JSON document on stdout (the deploy lines go to
stderr): `workspace`, `target`, `server`, `source`, `dry_run`, `prune`, an
`error` when the target could not be read, `refused` when `--prune` applied
nothing, `dags` (each with `dag_id`, `path` for a project, `state`,
`local_version`, `current_version`, `matched_version` for `behind`,
`is_paused`, `action` (`none`, `deploy`, `rollback` or `pause`), `result`
(`planned`, `applied` or `failed`), the `deploy` result of a deployed DAG,
and `error`), a `summary` (`in_sync`, `behind`, `deploy`, `rollback`,
`pause`, `not_in_workspace`, `errors`) and the `exit_code`. Fields are only
ever added.

**As a CI check.** Run the dry run on a schedule, or after the deploy job, to
catch a target changed by hand or a deploy that did not finish:

```yaml
      - name: Is prod running what main holds
        env:
          DEXAFLOW_SERVER_URL: https://cluster.example.com
          DEXAFLOW_TOKEN: ${{ secrets.DEXAFLOW_TOKEN }}   # a deploy token
        run: dexaflow sync ./dags --dry-run --prune
```

**As an Argo CD hook.** Argo CD runs a `Job` annotated as a hook after each
sync of an Application. The control plane never reads git (the DAGs are
built and registered from outside it), so the Job clones the repository at
the revision Argo CD synced and runs `dexaflow sync` itself. Argo CD hands a
Helm source the synced commit as `$ARGOCD_APP_REVISION` in its parameters, so
ship the Job as a small chart in the DAG repository, for example
`deploy/dexaflow-sync/`:

```yaml title="deploy/dexaflow-sync/Chart.yaml"
apiVersion: v2
name: dexaflow-sync
version: 0.1.0
```

```yaml title="deploy/dexaflow-sync/values.yaml"
revision: ""                       # set by Argo CD to the synced commit
repository: https://github.com/acme/dags
server: https://cluster.example.com
image: registry.example.com/tools/dexaflow-cli:1.4.0   # your image: the CLI, git and Python
```

```yaml title="deploy/dexaflow-sync/templates/sync-job.yaml"
apiVersion: batch/v1
kind: Job
metadata:
  generateName: dexaflow-sync-
  annotations:
    argocd.argoproj.io/hook: PostSync
    argocd.argoproj.io/hook-delete-policy: BeforeHookCreation
spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: sync
          image: {{ .Values.image | quote }}
          env:
            - name: DEXAFLOW_SERVER_URL
              value: {{ .Values.server | quote }}
            - name: DEXAFLOW_TOKEN
              valueFrom: { secretKeyRef: { name: dexaflow-sync, key: token } }
            - name: REPOSITORY
              value: {{ .Values.repository | quote }}
            - name: REVISION
              value: {{ required "revision is set by Argo CD" .Values.revision | quote }}
          command: [sh, -c]
          args:
            - git clone --quiet "$REPOSITORY" /work &&
              git -C /work checkout --quiet "$REVISION" &&
              dexaflow sync /work/dags --rollback --prune --yes
```

```yaml title="the Application"
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: dags
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/acme/dags
    targetRevision: main
    path: deploy/dexaflow-sync
    helm:
      parameters:
        - name: revision
          value: $ARGOCD_APP_REVISION
  destination:
    server: https://kubernetes.default.svc
    namespace: dexaflow-sync
```

Notes on the hook:

- **A full clone**, with history and tags, so the labels are the ones CI
  computed; the CLI image pins the same `dexaflow` version as CI.
- **`--rollback`**: syncing an earlier revision in Argo CD is a rollback, so
  the hook makes the older version current instead of stopping on `behind`.
- **No builder in a pod.** Let CI build and push the images
  (`dexaflow deploy --all`); the hook's deploys then reuse the image the
  registry holds for each content label. A DAG whose image is missing fails
  with exit 2, which fails the hook.
- **No published CLI image yet**: build one with the release binary, `git`
  and Python 3.11 to 3.13 (the compile parses `dag.py`).
- **`--prune` needs a token that may pause DAGs** (see Credentials above).
  With a deploy token, drop `--prune` from the hook and run
  `dexaflow sync ./dags --dry-run --prune` in CI to report the DAGs to retire.

**With Flux**, which has no hooks and no revision to hand a `Job`, run sync
in the CI job that pushes the commit Flux deploys, right after
`dexaflow deploy --all` (or in its place), with `--yes`; and run
`dexaflow sync ./dags --dry-run --prune` on a schedule to catch drift.


## Promoting between environments

`dexaflow promote` registers on one [target](/operate/workspaces-and-targets/) the exact
version another target runs: the same `dag.json` and the same image digest.
Nothing is compiled or built, so what reaches production is what ran in
staging.

```bash
dexaflow promote my_pipeline --from staging --to prod --dry-run   # what would be copied and registered
dexaflow promote my_pipeline --from staging --to prod             # the current version of staging
dexaflow promote my_pipeline --from staging --to prod --version v1.4.0-3fa2c1   # or another version
```

- **Which version.** Without `--version` it is the source's current version.
  `--version` matches a label first, then a version number from
  `dexaflow dags versions my_pipeline --target staging`.
- **Only images pinned by digest.** The version must run
  `<repo>@sha256:<digest>`, as every version `dexaflow deploy`, and
  `compile --build --push` followed by `push`, registers. A version registered
  with a tag (by a CLI from before #1703, or by a push that could not read the
  registry) is refused: a tag can name other bytes tomorrow.
- **The image, copied when needed.** The destination registry is `--registry`,
  else the `registry` of the `--to` target, else the
  `deploy.defaults.registry` its control plane advertises. When one is set and
  the image is not already in it, the image is copied there by digest, with
  every layer and platform, byte for byte (bytes that do not match their
  digest are refused), and the copy is checked in the destination afterwards.
  The repository path is kept: `ghcr.io/acme-dev/team-a/etl@sha256:…` promoted
  to a target whose registry is `registry.acme.dev/prod` becomes
  `registry.acme.dev/prod/acme-dev/team-a/etl@sha256:…` (same digest). The copy
  is also tagged with the version label, unless that tag already names another
  digest there, which refuses the promote: a promote never moves a tag. The
  version is registered with the copy, and only its `image` field differs from
  the source's `dag.json`. When the destination already holds this version,
  nothing is copied. Without a destination registry the same reference is
  registered, and the destination cluster must be able to pull it. The copy
  reads and pushes with the credentials of `docker login` and of a cloud CLI
  session, as a deploy does; a failure names the login command of each
  registry.
- **The normal register.** The destination gets the version through
  `POST /api/v2/dags/{dag_id}/versions`, after its dry run
  (`/versions/dry_run`): a refusal (the execution allowlist, the tenant
  limits, a label already registered there with other content), or any answer
  that is not a dry run other than an older server's `404` or `405`, stops
  the promote before anything is copied. A DAG the promote creates starts
  paused if the server or the DAG says so (`is_paused_upon_creation`, else
  `dags_are_paused_at_creation`), as a deploy's would, and an existing DAG
  keeps `is_paused`. Content the destination already has answers that nothing
  changed. When both servers run the same Dexaflow version, the destination
  records the same spec hash as the source when the image is not copied.
- **Audited, as claimed.** The register's `dag.version.register` audit entry
  carries `promoted_from` with the source target's name and server URL as the
  promoting CLI sent them (`promoted_from` and `promoted_from_server` on the
  register). The destination records this claim; it does not verify it. The
  source version's `build_info` (commit, pipeline, builder) is sent along, so
  the destination lists where the version was built.
- **A token for each control plane.** `--from-token` and `--to-token`, else
  the session saved for each server by `dexaflow auth login --target <name>`.
  `DEXAFLOW_TOKEN` is not read, so one token is never sent to two control
  planes. The source side reads each version's spec
  (`GET /api/v2/dags/{dag_id}/dagVersions/{version_number}/spec`), which holds
  the DAG's source, environment literals and params, so it needs read on dags
  and, for an API token, the `read` scope: a deploy-only token is refused
  there with `403`. The destination side needs only what a deploy needs: a
  `deployer` user or a token with the `deploy` scope.
- **For pipelines.** `--yes` skips the confirmation prompt, and
  `--output json` prints one JSON document on stdout (the source and
  destination targets, servers, version and images, whether the image was
  copied, and the status `registered`, `unchanged`, `planned` or `failed`),
  with the human lines on stderr.

In a pipeline, a promotion job after the staging deploy:

```yaml
      - name: Promote to prod
        env:
          STAGING_TOKEN: ${{ secrets.DEXAFLOW_STAGING_TOKEN }}
          PROD_TOKEN: ${{ secrets.DEXAFLOW_PROD_TOKEN }}
        run: |
          dexaflow promote my_pipeline --from staging --to prod --yes \
            --from-token "$STAGING_TOKEN" --to-token "$PROD_TOKEN"
```

## Require images pinned by digest

A version registered with a tag (`ghcr.io/acme/etl:v1`) runs whatever the tag
names when a node pulls it, so two nodes can run different bytes for one
version. The server setting `deploy.require_digest`
(`DEXAFLOW_DEPLOY_REQUIRE_DIGEST`, Helm `deploy.requireDigest`) decides what
the control plane does with an image that is not pinned as
`<repository>@sha256:<64 hex>` (sha256 only: another digest algorithm counts as
not pinned):

| Value | Register of an unpinned image | Making such a version current again |
|---|---|---|
| `off` (default) | Registered, as every release before the setting did. | Allowed, as before. |
| `warn` | Registered, with a warning in the answer's `warnings`, which `dexaflow push` and `dexaflow deploy` print on stderr. | Allowed, with a warning that `dexaflow rollback` prints on stderr. |
| `enforce` | Refused with a 400 that names the setting and how to pin. Nothing is registered. | Refused with a 400 that names the way out: redeploy the version's sources with `dexaflow deploy`, or set `warn`. |

Under `warn` and `enforce` the server logs each image it warns about
(`admitting an image not pinned by digest`, with the DAG, the version, the
image and the stage) and counts each warning and refusal in
`dexaflow_unpinned_image_total{stage="register|set_current",outcome="warned|refused"}`.
The `dag.version.register` and `dag.version.set_current` audit entries record
`require_digest` and `image_pinned`. The dry run of a register (the deploy
preflight) answers with the same warning or refusal, and records nothing.

**`dexaflow deploy`, `dexaflow promote` and `dexaflow compile --build --push`
followed by `dexaflow push` register pinned images** (#1703). `push` resolves
a tag in `dag.json` to its digest before registering it, so a file compiled by
an older CLI is pinned too, as long as the registry can be read from where
`push` runs. When it cannot, `push` registers the tag with a warning, and under
`enforce` the refusal names why the image could not be pinned. Resolving a
tag reads the registry (for up to 30 seconds), with the credentials of
`registry.auth_method` in the project's `dexaflow.yaml`: the one next to
`dag.json`, else the one in the working directory, else the one `--project
<dir>` names. Pin by hand with `--image`, which skips that read when it is
already a `<repository>@sha256:<digest>`; a tag given with `--image` must
resolve, or the push fails:

```bash
ref=ghcr.io/acme/my_pipeline:v1
digest=$(docker buildx imagetools inspect "$ref" --format '{{json .Manifest}}' | jq -r .digest)
repo=$ref
case ${ref##*/} in *:*) repo=${ref%:*} ;; esac   # drop the tag, never a registry port
dexaflow push dag.json --image "$repo@$digest"
```

A CLI from before #1703 registers the tag that `compile --build --push`
pushed: use `warn` to find the pipelines that still do, and upgrade their CLI
or move them to `dexaflow deploy`.

Set `enforce` only once nothing on the cluster registers a tag any more: no
increase of `outcome="warned"` while every pipeline has run.

A [source mode](/operate/source-mode/) version names the runtime image, which
the server requires pinned by digest, so it passes every mode. Under
`dexaflow lite`, the local dev loop registers a placeholder image: leave the
setting `off` there.
