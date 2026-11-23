---
title: "Pipelines from dexaflow ci init"
linkTitle: "dexaflow ci init"
weight: 30
description: "The pipeline files dexaflow ci init writes for GitHub Actions, GitLab CI, Google Cloud Build and any other CI."
---

`dexaflow ci init` writes a ready pipeline that runs [`dexaflow deploy`](/operate/cicd-deploy/) for the DAGs a commit changed.

## Writing the pipeline: `dexaflow ci init`

`dexaflow ci init <system>` writes a working deploy pipeline for the workspace
around the current folder, at the top of its git repository, where the CI
system reads it:

| System | Files written |
|---|---|
| `github` | `.github/workflows/dexaflow-deploy.yaml` and `ci/dexaflow-deploy.sh` |
| `gitlab` | `.gitlab-ci.yml` and `ci/dexaflow-deploy.sh` |
| `cloudbuild` | `cloudbuild.yaml` and `ci/dexaflow-deploy.sh` |
| `generic` | `ci/dexaflow-deploy.sh` alone, to run from any other CI system ([Argo Workflows, Tekton, Jenkins](/operate/cicd-deploy/recipes/#recipes-for-argo-workflows-tekton-jenkins-buildah-and-kaniko)) |

```bash
dexaflow ci init github
```

```text
wrote .github/workflows/dexaflow-deploy.yaml
wrote ci/dexaflow-deploy.sh

Next:
  1. create a deploy token, and store it as the CI secret DEXAFLOW_TOKEN:
       dexaflow auth create-token --scope deploy --name ci-<repo> --ttl 90d
  2. set DEXAFLOW_SERVER_URL in the CI system to the control plane URL
  3. commit the files and push to main
```

Every pipeline runs `ci/dexaflow-deploy.sh`, which holds the deploy itself, so
the logic is the same on every system. On each push to the branch (`--branch`,
`main` by default) it:

1. installs the CLI at the version that wrote it, with the `install.sh` of that
   release tag, which checks the archive against the release's checksums and,
   when `cosign` is on the runner, requires its signature. Pass
   `--cli-version vX.Y.Z` to pin another release, `v0.5.0` or later (a CLI
   built from source must); `DEXAFLOW_CLI_VERSION` overrides it in the job. On
   GitHub the step named `Install dexaflow` uses the
   [setup-dexaflow action](/operate/cicd-deploy/credentials/#the-setup-dexaflow-action) of the same release
   instead, pinned by the commit of its tag, so the workflow needs `v0.5.4` or
   later. The commit is this CLI's own, or for another `--cli-version` the one
   `git ls-remote` finds for the tag on GitHub. On GitLab the job runs in the
   [`dexaflow-cli` image](/operate/cicd-deploy/credentials/#the-dexaflow-cli-image) of the same release, which
   already holds the CLI, so the script installs nothing;
2. installs [uv](/operate/builders/#building-the-dependency-image-from-wheels)
   when the runner has none, from PyPI with `--require-hashes` against the
   wheel hashes the script carries, so dependency images are built from wheels
   without a container daemon;
3. reads the control plane from `DEXAFLOW_SERVER_URL`, or from the workspace's
   target: with `--target <name>`, or a `default_target` in
   `dexaflow.workspace.yaml`, the script exports `DEXAFLOW_TARGET` and the
   pipeline needs no URL (a target of your own config file is not in the
   repository, so it is not used). The deploy token comes from the CI secret
   `DEXAFLOW_TOKEN` ([Credentials for the control plane](/operate/cicd-deploy/credentials/#credentials-for-the-control-plane)).
   The pre-rename `LEOFLOW_TOKEN`, `LEOFLOW_SERVER_URL` and `LEOFLOW_TARGET`
   still work;
4. runs `dexaflow deploy --all --changed-since <base> --yes --output json`,
   where the base is the commit of the last successful deploy on GitHub, and
   on GitLab with an API token, or the commit before the push otherwise
   ([which commit, and why](/operate/cicd-deploy/workflows/#deploying-only-what-changed)), or
   `dexaflow deploy --all --yes --output json` when there is none (a first
   run, a new branch, a force push, a source without `.git`);
5. writes a summary of the JSON report: the job summary on GitHub
   (`$GITHUB_STEP_SUMMARY`), the `dexaflow-deploy.md` and
   `dexaflow-deploy.json` artifacts on GitLab, the build log elsewhere. Set
   `DEXAFLOW_SUMMARY_FILE` and `DEXAFLOW_REPORT_FILE` (absolute paths) to put
   them somewhere else. The job fails when a DAG fails to deploy.

The registry login is the CI system's own: `GITHUB_TOKEN` for `ghcr.io`,
workload identity for Artifact Registry and an IAM role for ECR on GitHub, the
job token (`$CI_REGISTRY_USER`) on GitLab, and the build's service account on
Cloud Build. Another registry gets a login from the secrets
`DEXAFLOW_REGISTRY_USERNAME` and `DEXAFLOW_REGISTRY_PASSWORD`. The comments at
the top of each file list what to set up. The registries are the ones the
workspace's projects push to (`registry.url`, the target's `registry`, the
workspace `defaults`); with none, the file says where to add your login.

On GitHub the job runs in an environment (`--environment`, `production` by
default), so `DEXAFLOW_TOKEN` can be an environment secret and the
environment's protection rules (required reviewers, allowed branches) apply to
each deploy; secrets and variables of the repository work too. A manual run
(`workflow_dispatch`) deploys only from the deploy branch. Every action is
pinned by commit SHA, with its version in a comment, and the checkout keeps no
token in the clone. On GitLab, without `--branch`, the job follows the
project's default branch (`$CI_DEFAULT_BRANCH`). The container images of the
GitLab job and the Cloud Build step are pinned by digest. Run
`dexaflow ci init <system> --force` after upgrading the CLI to pick up the
pins of the new release.

The CLI picks the [builder](/operate/builders/) at deploy time, as it does on
a laptop, and Docker is the fallback. On GitHub the runner has Docker, so
nothing changes in the workflow. On GitLab the job needs no daemon, and runs
in the [`dexaflow-cli` image](/operate/cicd-deploy/credentials/#the-dexaflow-cli-image), unless a project needs a real image build (its own
`Dockerfile`, `system_packages`, a dependency that is not a requirement by
name) and no [remote builder](/operate/builders/#building-on-cloud-build) is
configured: then it runs Docker in Docker, and the file names the projects
and why. On Cloud Build, the step installs Docker only in that case too.

When the control plane trusts the CI provider, the pipeline signs in with
the job's OIDC token instead of `DEXAFLOW_TOKEN`, and step 1 of `Next:` names
the issuer it uses; `--auth` picks the path
([Deploying without a stored token](/operate/cicd-deploy/credentials/#deploying-without-a-stored-token-ci-oidc)).

An existing file is never overwritten: the command names it and writes
nothing. Pass `--force` to replace it, or `--stdout` to print the files
instead of writing them. A `.gitlab-ci.yml` holds the project's other jobs,
so merge the `dexaflow-deploy` job into it by hand. After upgrading the CLI,
run `dexaflow ci init <system> --force` to pin the new version.

## Examples: what `dexaflow ci init` writes

These are the files `dexaflow ci init` writes for a workspace whose projects
push to the registry each system logs in to natively (`ghcr.io` on GitHub,
`registry.gitlab.com` on GitLab, Artifact Registry on Cloud Build), with
`vX.Y.Z` in place of the CLI version. The test suite checks that they match
the command's output.

{{< tabpane text=true >}}
{{% tab header="GitHub Actions" %}}

<!-- ci-init: github -->
```yaml title=".github/workflows/dexaflow-deploy.yaml"
# Deploys the DAGs changed on main since its last successful deploy to Dexaflow.
# Written by `dexaflow ci init github` for dexaflow vX.Y.Z; the deploy
# itself is in ci/dexaflow-deploy.sh.
#
# Set up once, in the repository settings: an environment named production
# (Settings, Environments) holding
#   - the variable DEXAFLOW_SERVER_URL: the control plane URL;
#   - the secret DEXAFLOW_TOKEN: a deploy token, created with
#     `dexaflow auth create-token --scope deploy --name ci-<repo> --ttl 90d`
# Repository-level variables and secrets of the same names work too. Protection
# rules on the environment (required reviewers, a branch policy) apply to
# every deploy.
#
# Each run deploys what changed since the commit of the last successful run of
# this workflow on main, so a failed or cancelled run is caught up by the
# next one; with no successful run yet, every DAG is deployed.
name: Deploy DAGs to Dexaflow
on:
  push:
    branches: ["main"]
  workflow_dispatch:
concurrency:
  group: dexaflow-deploy
  cancel-in-progress: false
permissions:
  contents: read
  actions: read # gh run list: the last successful deploy
  packages: write
jobs:
  deploy:
    # A manual run from another branch deploys nothing.
    if: github.ref == 'refs/heads/main'
    runs-on: ubuntu-latest
    environment: production
    env:
      DEXAFLOW_SERVER_URL: ${{ vars.DEXAFLOW_SERVER_URL }}
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          fetch-depth: 0 # --changed-since and the version label need the history
          persist-credentials: false
      - uses: actions/setup-python@5fda3b95a4ea91299a34e894583c3862153e4b97 # v7.0.0
        with:
          python-version: "3.12"
      # Installs the pinned CLI, after checking its signature and checksum.
      - name: Install dexaflow
        uses: dexadata/dexaflow/.github/actions/setup-dexaflow@<sha> # vX.Y.Z
        with:
          version: vX.Y.Z
      - name: Log in to ghcr.io
        uses: docker/login-action@dbcb813823bdd20940b903addbd779551569679f # v4.6.0
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      - name: Find the commit of the last successful deploy
        id: base
        env:
          GH_TOKEN: ${{ github.token }}
          BRANCH: main
        run: |
          sha="$(gh run list --repo "$GITHUB_REPOSITORY" --workflow dexaflow-deploy.yaml --branch "$BRANCH" \
            --status success --limit 1 --json headSha --jq '.[0].headSha // ""')" || sha=""
          echo "sha=$sha" >>"$GITHUB_OUTPUT"
      # The CLI picks the builder: an image already in the registry, the DAG
      # code as a layer on the dependency image, dependencies from wheels with
      # uv, the remote builder when one is configured, and the runner's Docker
      # only when none of these applies.
      - name: Deploy the DAGs changed since then
        env:
          DEXAFLOW_TOKEN: ${{ secrets.DEXAFLOW_TOKEN }}
          DEXAFLOW_BASE_REF: ${{ steps.base.outputs.sha }}
        run: sh ci/dexaflow-deploy.sh
```

[`setup-dexaflow`](/operate/cicd-deploy/credentials/#the-setup-dexaflow-action) installs the release named by
`version`, checks its signature and checksum, and caches it per version.

GHCR refuses image names with upper-case letters, so keep `registry.url` and
`registry.image_name` lower case.

Do not add `docker/setup-buildx-action` to this job. It makes a
`docker-container` builder the default, whose builds stay in the builder's
cache instead of the local image store; the CLI runs a plain `docker build`
without `--load` when it falls back to Docker, so the `docker push` that
follows finds no image.

For a pull request, `--plan` says what merging would deploy. Pass the base
branch through `env:` rather than inline in `run:`:

```yaml
      - name: What would this pull request deploy
        env:
          BASE_REF: ${{ github.base_ref }}
        run: dexaflow deploy --all --changed-since "origin/$BASE_REF" --plan
```

{{% /tab %}}
{{% tab header="GitLab CI" %}}

<!-- ci-init: gitlab -->
```yaml title=".gitlab-ci.yml"
# Deploys the DAGs a push to the default branch changed to Dexaflow.
# Written by `dexaflow ci init gitlab` for dexaflow vX.Y.Z; the deploy
# itself is in ci/dexaflow-deploy.sh. To add it to an existing .gitlab-ci.yml, copy
# the dexaflow-deploy job into that file by hand.
#
# Set up once, in Settings, CI/CD, Variables:
#   - DEXAFLOW_SERVER_URL: the control plane URL;
#   - DEXAFLOW_TOKEN, masked and protected: a deploy token, created with
#     `dexaflow auth create-token --scope deploy --name ci-<repo> --ttl 90d`
#   - DEXAFLOW_GITLAB_API_TOKEN, masked and protected (recommended): a project
#     access token with the read_api scope, see below.
# The job pushes to the project's container registry with the job token
# ($CI_REGISTRY_USER and $CI_REGISTRY_PASSWORD).
#
# What a run deploys: with DEXAFLOW_GITLAB_API_TOKEN, what changed since the
# commit of the last successful push pipeline on the branch, so a failed or
# cancelled pipeline is caught up by the next one. The job token cannot read
# the pipelines API, so without that variable a run deploys what changed since
# the previous commit of its push ($CI_COMMIT_BEFORE_SHA), and the changes of a
# failed, cancelled or skipped pipeline wait for the next change to the same
# DAG: rerun that pipeline, or run one by hand (Build, Pipelines, Run
# pipeline), which deploys every DAG. Turn off "Auto-cancel redundant
# pipelines" for the branch, and set the process mode of the resource group
# dexaflow-deploy to oldest_first (PUT /projects/:id/resource_groups/dexaflow-deploy
# with process_mode=oldest_first), so pipelines deploy in push order.
#
# No container daemon is needed: the CLI pushes the DAG code as a layer on the
# dependency image, and builds that image from wheels with uv. The job runs in
# the dexaflow-cli image of vX.Y.Z, pinned by digest, which carries the
# CLI, Python, git and uv, so nothing is installed in the job. The CLI is the
# image's, so DEXAFLOW_CLI_VERSION does not apply: to change the release,
# write this file again with `dexaflow ci init gitlab --force` (--cli-version
# picks another release).
dexaflow-deploy:
  image:
    name: ghcr.io/dexadata/dexaflow-cli:vX.Y.Z@sha256:<digest>
    entrypoint: [""] # GitLab runs the script in a shell
  resource_group: dexaflow-deploy
  variables:
    GIT_DEPTH: "0" # --changed-since and the version label need the history
    DEXAFLOW_SUMMARY_FILE: $CI_PROJECT_DIR/dexaflow-deploy.md
    DEXAFLOW_REPORT_FILE: $CI_PROJECT_DIR/dexaflow-deploy.json
  rules:
    - if: $CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH && ($CI_PIPELINE_SOURCE == "push" || $CI_PIPELINE_SOURCE == "web")
  script:
    - |
      mkdir -p "$HOME/.docker"
      b64() { printf '%s:%s' "$1" "$2" | base64 | tr -d '\n'; }
      printf '{"auths":{"%s":{"auth":"%s"}}}\n' \
        "$CI_REGISTRY" "$(b64 "$CI_REGISTRY_USER" "$CI_REGISTRY_PASSWORD")" \
        >"$HOME/.docker/config.json"
    - |
      DEXAFLOW_BASE_REF="$CI_COMMIT_BEFORE_SHA"
      if [ -n "${DEXAFLOW_GITLAB_API_TOKEN:-}" ]; then
        DEXAFLOW_BASE_REF="$(python3 -c '
      import json, os, urllib.parse, urllib.request
      e = os.environ
      q = urllib.parse.urlencode({"ref": e["CI_COMMIT_BRANCH"], "source": "push", "status": "success", "per_page": 1})
      r = urllib.request.Request(e["CI_API_V4_URL"] + "/projects/" + e["CI_PROJECT_ID"] + "/pipelines?" + q,
                                 headers={"PRIVATE-TOKEN": e["DEXAFLOW_GITLAB_API_TOKEN"]})
      with urllib.request.urlopen(r, timeout=30) as resp:
          p = json.load(resp)
      print(p[0]["sha"] if p else "")
      ')" || DEXAFLOW_BASE_REF=""
      fi
      export DEXAFLOW_BASE_REF
    - sh ci/dexaflow-deploy.sh
  artifacts:
    when: always
    paths: [dexaflow-deploy.md, dexaflow-deploy.json]
    expire_in: 30 days
```

The job runs in the [`dexaflow-cli` image](/operate/cicd-deploy/credentials/#the-dexaflow-cli-image) of the
release `ci init` pins, by tag and digest: the command reads the digest from
`ghcr.io` when it writes the file, and stops, writing nothing, when it cannot
(no network, for example). The image carries the CLI, Python, git and uv, so
the job installs nothing, and `entrypoint: [""]` lets GitLab run the script in
a shell instead of the image's `dexaflow` entrypoint. The image runs as UID
65532, which writes to the checkout because GitLab's Docker executor leaves it
writable for any user. The CLI is the image's, so `DEXAFLOW_CLI_VERSION` does
not apply: run `dexaflow ci init gitlab --force` (with `--cli-version` for
another release) to move the job to another release. A `--cli-version` older
than `v0.5.4`, which has no `dexaflow-cli` image, writes a job on
`python:3.12-slim` (by digest) that installs git and curl with `apt-get`, and
the CLI and uv with the script.

When a project needs a real image build and no remote builder is configured,
the job runs Docker in Docker instead: `image: docker:27` and
`services: [docker:27-dind]` (by digest), the TLS variables (`DOCKER_HOST`,
`DOCKER_TLS_CERTDIR`) and `apk add --no-cache python3 git curl`. That image
has no pip, so uv is not installed and the daemon builds the dependency
images too. `docker:dind`
runs only on a runner configured with `privileged = true` (and, for the TLS
variables, `volumes = ["/certs/client"]`) in its `config.toml`; GitLab's shared
SaaS runners are. `registry.url` in `dexaflow.yaml` or the workspace defaults
is your project's `$CI_REGISTRY_IMAGE`, written out (for example
`registry.gitlab.com/your-group/my-dags`).

{{% /tab %}}
{{% tab header="Google Cloud Build" %}}

Build, push and register on Cloud Build; the DAG image runs as task pods on your
cluster. The build's service account pushes to Artifact Registry in the same
project through the gcloud session the CLI reads, so `registry.url` is
`us-central1-docker.pkg.dev/<project>/dags`. The deploy token lives in Secret
Manager as `dexaflow-deploy-token` and reaches the step as `DEXAFLOW_TOKEN`.

<!-- ci-init: cloudbuild -->
```yaml title="cloudbuild.yaml"
# Deploys the DAGs of this workspace to Dexaflow on Cloud Build.
# Written by `dexaflow ci init cloudbuild` for dexaflow vX.Y.Z; the
# deploy itself is in ci/dexaflow-deploy.sh.
#
# Set up once:
#   - a Secret Manager secret dexaflow-deploy-token holding a deploy token,
#     created with `dexaflow auth create-token --scope deploy --name ci-<repo> --ttl 90d`,
#     readable by the build's service account;
#   - the substitution _DEXAFLOW_SERVER_URL: the control plane URL;
#   - a trigger on pushes to main, for example
#     gcloud builds triggers create github --repo-owner=<owner> --repo-name=<repo> --branch-pattern='^main$' --build-config=cloudbuild.yaml \
#       --service-account=projects/<project>/serviceAccounts/<builder>@<project>.iam.gserviceaccount.com
# The step signs in to Artifact Registry with the build's service account
# (the gcloud session the CLI reads), which needs roles/artifactregistry.writer.
# A source uploaded by `gcloud builds submit` has no .git, so every DAG is
# deployed; DAGs whose content is already the current version register
# nothing. When the source is a git clone, the step fetches its full history
# and tags, and _DEXAFLOW_BASE_REF deploys only what changed since a commit.
steps:
  - id: deploy
    name: gcr.io/google.com/cloudsdktool/cloud-sdk:slim@sha256:ffd36b9b80bb08b2dea60b58ca2b60dfa3a40a3c00f3e5732d6d113f4e7ffa45
    entrypoint: bash
    args:
      - -c
      - |
        set -e
        apt-get update -qq
        apt-get install -y -qq --no-install-recommends python3 python3-pip git curl ca-certificates >/dev/null
        if [ -d .git ]; then git fetch --unshallow --tags || true; fi
        sh ci/dexaflow-deploy.sh
    secretEnv: [DEXAFLOW_TOKEN]
    env:
      - DEXAFLOW_SERVER_URL=${_DEXAFLOW_SERVER_URL}
      - DEXAFLOW_BASE_REF=${_DEXAFLOW_BASE_REF}
      - PIP_BREAK_SYSTEM_PACKAGES=1
substitutions:
  _DEXAFLOW_SERVER_URL: https://dexaflow.example.com
  _DEXAFLOW_BASE_REF: ""
availableSecrets:
  secretManager:
    - versionName: projects/$PROJECT_ID/secrets/dexaflow-deploy-token/versions/latest
      env: DEXAFLOW_TOKEN
options:
  logging: CLOUD_LOGGING_ONLY
```

`gcloud builds submit` uploads the source without `.git`, so the version label
starts from `dev` and the step deploys every DAG. When the source is a git
clone, the step fetches its full history and tags first
(`git fetch --unshallow --tags`). The label still carries the
hash of each DAG's content, so a DAG that did not change registers nothing,
and one that changed gets a new label.

{{% alert title="Restricted networks (Cloud Shell): run the build inside Cloud Build" color="success" %}}
When the local Docker daemon can't reach Google's IPs (in Cloud Shell, for
example), don't build locally at all. The `cloudbuild.yaml` above runs the
whole deploy **inside Cloud Build** (Google's network, not your machine):
serverless, still yaml-driven, no Dockerfile, no local egress. Trigger it from
Cloud Shell with:

```bash
gcloud builds submit --config cloudbuild.yaml .
```

Cloud Build receives the source, builds, pushes to Artifact Registry and
registers there; your machine never touches a Docker daemon. (Use
`gcloud builds submit --tag …` only if you ship your own Dockerfile; the
yaml-driven path has none for it to build.)
{{% /alert %}}

{{% /tab %}}
{{% tab header="Generic / any CI" %}}

`dexaflow ci init generic` writes the script alone. Run it from any CI system
with **Python 3.11+** and git on the runner (see
[Python on the runner](/operate/cicd-deploy/credentials/#python-on-the-runner)), after the registry login, with
`DEXAFLOW_SERVER_URL`, `DEXAFLOW_TOKEN` and `DEXAFLOW_BASE_REF` set:

```bash
export DEXAFLOW_SERVER_URL=https://cluster.example.com
export DEXAFLOW_TOKEN="$DEPLOY_TOKEN_FROM_YOUR_SECRET_STORE"   # a deploy token
export DEXAFLOW_BASE_REF="$COMMIT_BEFORE_THIS_PUSH"             # empty deploys every DAG
sh ci/dexaflow-deploy.sh
```

<!-- ci-init: generic -->
```sh title="ci/dexaflow-deploy.sh"
#!/bin/sh
# Deploys the DAGs of this workspace that changed to Dexaflow.
# Written by `dexaflow ci init` for dexaflow vX.Y.Z; write it again with
# `dexaflow ci init <system> --force` after upgrading the CLI.
#
# Usage: sh ci/dexaflow-deploy.sh [deploy|install]
#   deploy   (the default) install the CLI and uv when missing, then deploy
#   install  only install the pinned CLI
#
# Environment:
#   DEXAFLOW_SERVER_URL    the control plane URL (required)
#   DEXAFLOW_TOKEN         a deploy token (required), created once with
#                          `dexaflow auth create-token --scope deploy --name ci-<repo> --ttl 90d`
#   DEXAFLOW_BASE_REF      the commit the branch was on before this push: only
#                          the DAGs changed since it are deployed. Empty, all
#                          zeros (a new branch) or missing from the clone
#                          deploys every DAG.
#   DEXAFLOW_SUMMARY_FILE  a file to append a Markdown summary to (default:
#                          $GITHUB_STEP_SUMMARY, else the log)
#   DEXAFLOW_REPORT_FILE   a file to copy the `dexaflow deploy --output json`
#                          report to (optional)
#   DEXAFLOW_CLI_VERSION   the release to install instead of vX.Y.Z (a tag
#                          such as vX.Y.Z, v0.5.0 or later)
# The pre-rename LEOFLOW_TOKEN, LEOFLOW_SERVER_URL and LEOFLOW_TARGET still work.
#
# The CLI is installed by the install.sh of its release tag, which checks the
# archive against the release's checksums.txt, and its cosign signature when
# cosign is on PATH (then required). uv comes from PyPI, checked against the
# wheel hashes below.
set -eu

cli_version="${DEXAFLOW_CLI_VERSION:-vX.Y.Z}"
DEXAFLOW_TOKEN="${DEXAFLOW_TOKEN:-${LEOFLOW_TOKEN:-}}"
DEXAFLOW_SERVER_URL="${DEXAFLOW_SERVER_URL:-${LEOFLOW_SERVER_URL:-}}"
export DEXAFLOW_TOKEN DEXAFLOW_SERVER_URL
uv_version="0.12.24"
workspace_dir="."

die() {
	echo "error: $*" >&2
	exit 1
}

# The version ends up in a URL and in install.sh: only a release tag is taken.
case "$cli_version" in
v[0-9]*.[0-9]*.[0-9]*)
	case "$cli_version" in
	*[!0-9A-Za-z.-]*) die "DEXAFLOW_CLI_VERSION=$cli_version is not a release tag such as vX.Y.Z" ;;
	esac
	;;
*) die "DEXAFLOW_CLI_VERSION=$cli_version is not a release tag such as vX.Y.Z" ;;
esac

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# install_dexaflow installs the pinned CLI with the release's install.sh,
# unless that version is already on PATH.
install_dexaflow() {
	if command -v dexaflow >/dev/null 2>&1 &&
		[ "$(dexaflow version 2>/dev/null | awk '{print $2}')" = "${cli_version#v}" ]; then
		return 0
	fi
	dir="${DEXAFLOW_INSTALL_DIR:-$HOME/.dexaflow/bin}"
	url="https://raw.githubusercontent.com/dexadata/dexaflow/$cli_version/install.sh"
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL "$url" -o "$tmp/install.sh"
	else
		wget -qO "$tmp/install.sh" "$url"
	fi
	if command -v cosign >/dev/null 2>&1; then
		DEXAFLOW_INSTALL_REQUIRE_SIGNATURE=1
		export DEXAFLOW_INSTALL_REQUIRE_SIGNATURE
	fi
	DEXAFLOW_VERSION="$cli_version" DEXAFLOW_NO_SETUP=1 DEXAFLOW_NO_PATH=1 \
		DEXAFLOW_INSTALL_DIR="$dir" sh "$tmp/install.sh"
	PATH="$dir:$PATH"
	export PATH
	if [ -n "${GITHUB_PATH:-}" ]; then
		echo "$dir" >>"$GITHUB_PATH"
	fi
}

# install_uv puts uv on PATH, so dependency images are built from wheels
# without a container daemon. Without it the remote builder or the local
# daemon builds them.
install_uv() {
	command -v uv >/dev/null 2>&1 && return 0
	echo "uv==$uv_version --hash=sha256:e47005957dca320272ad68960b591b854b6bf0c3288368efb096d9ccd06b8404 --hash=sha256:94c9ed22c1b5d6861a72639e23c5707bb07a354b67ec0462e9a0169a9665b5fd --hash=sha256:0fc14fabb78c55d140a01125d5cd038afb4fed317eb0c3868f3fba87b8cefe16 --hash=sha256:b6fe9cd01f5c576a640f17829cb465899fffb5d6d8dfdd576e38d4f1e40dd438" >"$tmp/uv.txt"
	if python3 -m pip install --quiet --disable-pip-version-check --only-binary :all: \
		--require-hashes -r "$tmp/uv.txt" >/dev/null 2>&1 &&
		command -v uv >/dev/null 2>&1; then
		return 0
	fi
	echo "note: uv could not be installed; dependency images are built by the remote builder or the container daemon" >&2
}

# base_ref prints the commit to compare with, or nothing to deploy every DAG.
base_ref() {
	base="${DEXAFLOW_BASE_REF:-}"
	case "$base" in
	*[!0]*) ;;
	*) return 0 ;;
	esac
	if ! git rev-parse --git-dir >/dev/null 2>&1; then
		echo "note: not a git checkout, so every DAG is deployed" >&2
		return 0
	fi
	if ! git cat-file -e "$base^{commit}" 2>/dev/null; then
		echo "note: $base is not in this clone (a force push, or a shallow clone), so every DAG is deployed" >&2
		return 0
	fi
	echo "$base"
}

# summarize prints a Markdown summary of the deploy report $1.
summarize() {
	python3 - "$1" <<'PY'
import json
import sys

try:
    with open(sys.argv[1]) as f:
        report = json.load(f)
except (OSError, ValueError):
    print("Dexaflow deploy wrote no report; see the job log.")
    sys.exit(0)


def cell(value):
    return str(value or "").replace("|", "\\|").replace("\n", " ")


where = report.get("server") or ""
if report.get("target"):
    where += " (target %s)" % report["target"]
print("### Dexaflow deploy to %s" % cell(where))
print()
dags = report.get("dags") or []
if dags:
    print("| DAG | Status | Version | Image | Note |")
    print("| --- | --- | --- | --- | --- |")
    for d in dags:
        note = d.get("error") or d.get("reason") or d.get("next")
        print("| %s | %s | %s | %s | %s |" % tuple(cell(v) for v in (
            d.get("dag_id"), d.get("status"), d.get("version"), d.get("image"), note)))
else:
    print("No DAG changed, so nothing was deployed.")
for p in report.get("removed") or []:
    print()
    print("- %s (%s) was removed and is still registered. If its DAG should go, remove it with "
          "`dexaflow dags delete %s --deregister`." % (cell(p.get("path")), cell(p.get("dag_id")), cell(p.get("dag_id"))))
for p in report.get("moved") or []:
    print()
    print("- %s moved from %s to %s." % (cell(p.get("dag_id")), cell(p.get("from")), cell(p.get("to"))))
PY
}

deploy() {
	[ -n "${DEXAFLOW_TOKEN:-}" ] ||
		die "DEXAFLOW_TOKEN is not set: store a deploy token as a CI secret (dexaflow auth create-token --scope deploy)"
	[ -n "${DEXAFLOW_SERVER_URL:-}" ] ||
		die "DEXAFLOW_SERVER_URL is not set: set it to the control plane URL"
	base="$(base_ref)"
	report="$tmp/report.json"
	status=0
	if [ -n "$base" ]; then
		dexaflow deploy --all --changed-since "$base" --yes --output json >"$report" || status=$?
	else
		dexaflow deploy --all --yes --output json >"$report" || status=$?
	fi
	summarize "$report" >"$tmp/summary.md" || true
	out="${DEXAFLOW_SUMMARY_FILE:-${GITHUB_STEP_SUMMARY:-}}"
	if [ -n "$out" ]; then
		cat "$tmp/summary.md" >>"$out"
	else
		cat "$tmp/summary.md"
	fi
	if [ -n "${DEXAFLOW_REPORT_FILE:-}" ]; then
		cp "$report" "$DEXAFLOW_REPORT_FILE"
	fi
	return "$status"
}

cd "$(dirname "$0")/.."
cd "$workspace_dir"
case "${1:-deploy}" in
install) install_dexaflow ;;
deploy)
	install_dexaflow
	install_uv
	deploy
	;;
*) die "usage: sh ci/dexaflow-deploy.sh [deploy|install]" ;;
esac
```

Split into a build stage and a register stage, the same pipeline registers the
image **tag** rather than its digest (see the warning at the top):

```bash
dexaflow compile dags/my_pipeline --build --push -o dag.json   # image from registry: in dexaflow.yaml
dexaflow push dag.json
```
{{% /tab %}}
{{< /tabpane >}}
