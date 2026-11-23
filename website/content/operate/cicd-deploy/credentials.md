---
title: "Credentials and the CLI in CI"
linkTitle: "Credentials and CLI"
weight: 20
description: "The control-plane credentials a pipeline uses, CI OIDC without a stored token, Python on the runner and installing the CLI."
---

What a CI runner needs before it can run [`dexaflow deploy`](/operate/cicd-deploy/): a credential for the control plane, Python and the CLI.

## Prerequisites

- The `dexaflow` CLI on the runner (download the release binary, or `go install`).
- **Python 3.11 to 3.13 on the runner** (`dexaflow compile` invokes the
  stdlib-only parser shim, [ADR 0024](/project/adrs/0024-dag-parsing-structural-shim/),
  to turn `dag.py` into `dag.json`). See [Python on the runner](#python-on-the-runner) below.
- A container registry your cluster can pull from, and a credential for it on
  the runner: the `gcloud`, `aws` or `az` session for Artifact Registry, ECR and
  ACR, the docker config (`docker login`, or a login action) for the others,
  and the container tool's own login when it builds
  ([Registry credentials](/operate/builders/#registry-credentials)).
- The control plane URL, passed as `--server` or exported as
  `DEXAFLOW_SERVER_URL` (the pre-rename `LEOFLOW_SERVER_URL` still works). The
  CLI reads no other variable for it: a pipeline that exports something else
  (`DEXAFLOW_SERVER`, for instance) silently targets the default,
  `http://localhost:8080`.
- A CI credential for the control plane, below.

## Credentials for the control plane

A CI job authenticates with a **deploy token**: a long-lived API token that
registers DAG versions and nothing else. A control plane that trusts your CI
provider takes the job's own OIDC token instead, and no token is stored
([Deploying without a stored token](#deploying-without-a-stored-token-ci-oidc)). Store it as a CI secret named
`DEXAFLOW_TOKEN`; `deploy`, `push`, `rollback` and the other commands read that
variable (the pre-rename `LEOFLOW_TOKEN` still works).

1. Once, as an admin, create a service user for CI with the built-in
   **`deployer`** role. It reads DAGs and their versions, registers versions
   and sets the current version (rollback), and nothing else: it cannot delete
   or pause a DAG, trigger a run, or touch connections, variables or users.

   ```bash
   dexaflow auth create-user --email ci-deploy@example.com --password-stdin \
     --role deployer < password.txt
   ```

2. Create a deploy token for that user. Sign in as the service user (the
   command signs in with `--username` and the password, or uses the session
   `dexaflow auth login` saved), name the token after the pipeline that will
   hold it, and choose its lifetime:

   ```bash
   dexaflow auth create-token --server https://cluster.example.com \
     --username ci-deploy@example.com --password-stdin \
     --scope deploy --name github-my-dags --ttl 90d < password.txt
   ```

   The token (it starts with `dxf_`) is printed **once**, alone on stdout; the
   name, id, scopes and expiry go to stderr. Store it as the `DEXAFLOW_TOKEN`
   secret of the pipeline. The server keeps only its SHA-256 hash, so a lost
   token cannot be shown again: create a new one.

3. Set `DEXAFLOW_SERVER_URL` and `DEXAFLOW_TOKEN` in the job and run
   `dexaflow deploy`, as in the examples below.

What a deploy token can do, and how it ends:

- **Scope.** `--scope deploy` carries `dexaflow:deploy`: the token reads
  DAGs, DAG details and versions, registers versions and sets the current
  version, and is refused everything else, whatever its owner's roles: no
  variables, connections, logs, XComs, runs, users or event log. It never does
  more than its owner's roles allow either, and the owner's roles are read
  again on every request. `--scope read` makes a token that reads everything
  its owner can read, so do not give one to CI. `deploy --trigger` needs to
  trigger a run, which a deploy token cannot do.
- **Lifetime.** `--ttl` takes days (`90d`) or a duration (`12h`), from one hour
  to 365 days; the default is 90 days. An expired token answers `401`.
- **Listing.** `dexaflow auth tokens list` shows your tokens with their id,
  name, scopes, expiry and when each was last used (recorded at most once every
  five minutes). An admin (whoever holds `write:user`) sees every token of the
  tenant, and never a token of another tenant.
- **Revoking.** `dexaflow auth tokens revoke <id>` stops a token on its next
  request. Deactivating the owner stops all of the owner's tokens.
- **In the UI.** **User > API tokens** lists the same tokens as
  `dexaflow auth tokens list`: name, scope, owner, when it was created, when it
  expires and when it was last used, never the token itself. **Revoke** asks
  for a confirmation and then stops the token, like
  `dexaflow auth tokens revoke`, and the revoke is recorded in the audit log as
  `api_token.revoke`. The screen does not create tokens: the secret is shown
  once, by `dexaflow auth create-token`.
- **Limits.** A token cannot create, list or revoke tokens. A user holds at
  most 50 live tokens, and token names are unique among a user's live tokens:
  1 to 64 letters, digits, dots, dashes or underscores. Once a token has
  expired, a new token can take its name: the create retires the expired token
  and records it in the audit log as `api_token.retire_expired`, with the old
  token's id, when it expired and the id of the token that took its name, so
  it is not mistaken for a manual revoke (`api_token.revoke`). The token is
  sent in the `Authorization` header only; it never works as a browser session
  cookie.
- **Turned off.** An operator can turn API tokens off with
  `auth.api_tokens.disabled: true` (Helm: `auth.apiTokens.disabled`), for
  example where a platform in front of Dexaflow decides at each sign-in who
  may reach it and no credential should outlive that decision. The token
  routes and the **API tokens** screen are then not served, and every `dxf_`
  token answers `401`, including one created before; it works again if the
  tokens are turned back on before it expires. Deploy through CI OIDC or the
  platform's issuer instead.

Creating, listing and revoking tokens is also available through the API
(`POST`, `GET /api/v2/apiTokens` and `DELETE /api/v2/apiTokens/{id}`).

{{% alert title="Without a deploy token: a password per job" color="info" %}}
A session token from `dexaflow auth create-token` without `--scope` **expires
after one hour** (`auth.jwt.token_ttl_seconds`, 3600 by default), so it cannot
be stored as a CI secret. On a control plane older than deploy tokens, store
the service user's password instead (`DEXAFLOW_CI_PASSWORD` below) and mint a
session token at the start of each job:

```bash
export DEXAFLOW_SERVER_URL=https://cluster.example.com
DEXAFLOW_TOKEN="$(printf '%s\n' "$DEXAFLOW_CI_PASSWORD" |
  dexaflow auth create-token --username ci-deploy@example.com --password-stdin)"
export DEXAFLOW_TOKEN
```

That token lives one hour: a build that can take longer should run the steps
apart (`dexaflow compile --build --push`, then mint the token, then
`dexaflow push`, which registers the digest `compile` wrote into `dag.json`).
{{% /alert %}}

## When deploys go through your platform's issuer

A control plane with `auth.trusted_issuer.deploy_audience` set takes deploys
only from its trusted issuer
([deploys through the issuer only](/reference/configuration/#deploys-through-the-issuer-only)):
a deploy token, a session token and a password login all get `403` on
`dexaflow deploy`, `dexaflow push` and `dexaflow rollback`, and `deploy` says
that the control plane takes deploys only through its trusted issuer. Deploy
through the platform that owns deployments instead. When that platform runs
`dexaflow deploy` itself, it passes a bearer its issuer minted for the deploy
audience, with the `dexaflow:deploy` scope and a user linked to the issuer,
as `DEXAFLOW_TOKEN` or `--token`, its `aud` being the deploy audience alone.
With `--paused` or `--active`, which set `is_paused`, the token also needs
the `dexaflow:run` scope (and the user `write` on dags). The token is
short-lived (at most `auth.trusted_issuer.bearer_max_lifetime_seconds`), so
mint it for each job. Deleting a DAG goes through the issuer the same way.

## Deploying without a stored token (CI OIDC)

A deploy token is a secret that lives for months in the CI system. When the
control plane trusts the CI provider instead, nothing long-lived is stored:
each job asks its provider for an OIDC token minted for that one job,
`dexaflow auth ci-token` exchanges it at `POST /api/v2/auth/ciToken` for a
deploy session of the control plane, and the session expires an hour later
(`auth.ci_oidc.session_ttl_seconds`). GitHub Actions, GitLab CI and Google
Cloud Build are supported, and any other provider that publishes its keys
([ADR 0092](/project/adrs/0092-ci-deploys-with-oidc-tokens/)).

## Trusting a CI provider

List the providers the control plane trusts under `auth.ci_oidc.issuers`. Each
entry is one provider for one tenant:

```yaml
auth:
  ci_oidc:
    session_ttl_seconds: 3600
    issuers:
      - name: github-acme
        provider: github
        issuer: https://token.actions.githubusercontent.com
        audience: https://dexaflow.acme.example
        tenant: acme
        rules:
          - claims:
              repository: acme/dags
              ref: refs/heads/main
              environment: production
            role: deployer
      - name: gitlab-acme
        provider: gitlab
        issuer: https://gitlab.com
        audience: https://dexaflow.acme.example
        tenant: acme
        rules:
          - claims: {project_path: acme/dags, ref: main, ref_protected: "true"}
            role: deployer
      - name: cloudbuild-acme
        provider: cloudbuild
        issuer: https://accounts.google.com
        audience: https://dexaflow.acme.example
        tenant: acme
        rules:
          - claims: {email: dags-builder@acme-ci.iam.gserviceaccount.com}
            role: deployer
```

With the Helm chart, the same list goes under `auth.ciOidc.issuers` and the
lifetime under `auth.ciOidc.sessionTtlSeconds`; the chart passes the list to
the server as JSON in `LEOFLOW_AUTH_CI_OIDC_ISSUERS` (`DEXAFLOW_AUTH_CI_OIDC_ISSUERS`
works too, and wins over the file). The server checks every entry at boot and
refuses to start, naming the field, when one is wrong.

| Field | What it is |
|---|---|
| `name` | 1 to 40 lowercase letters, digits or `-`. It names the entry in logs, metrics, audit records and the sessions it issues. |
| `provider` | `github`, `gitlab`, `cloudbuild` or `other`. It decides which identity claim each rule must pin. |
| `issuer` | The exact `iss` of the provider's tokens, `https` (`http` only on a loopback host). The keys are read from its discovery document, whose `issuer` must be the same. |
| `jwks_url` | Optional: where the keys are, instead of discovery. |
| `audience` | The `aud` the job asks its token for. One issuer and audience pair belongs to one entry, so give each tenant its own audience. |
| `tenant` | The tenant every session of this entry acts in. It is never read from a claim: a token that claims another tenant still gets this one. |
| `max_lifetime_seconds` | The longest `exp - iat` a token may have, `0` (one hour) to 86400. GitHub and Cloud Build mint tokens for minutes; raise it for a GitLab job token that lives as long as a long job. |
| `rules` | Which jobs may deploy, and as what. The first rule whose claims all match decides; a token that matches none is refused with `403`. |

A rule has `claims`, a `role` and optional `scopes`:

- **`claims`** maps claim names to values. A value is matched against a string
  claim of the token, exactly or as a pattern (`*` and `?` within a segment, so
  `acme/*` matches `acme/dags` but not `acme/dags/sub`, and `[a-z]` ranges). A
  pattern must name something: `*` alone is refused. Each rule pins an identity
  claim of its provider, so no rule admits every job on the provider:
  `repository`, `repository_id`, `repository_owner` or `repository_owner_id`
  on GitHub, `project_path`, `project_id`, `namespace_path` or `namespace_id`
  on GitLab, `email` or `sub` on Cloud Build. Pin the branch (`ref`) and, on
  GitHub, the `environment` too, so a pull request or a feature branch cannot
  deploy.
- **`role`** is a role of the entry's tenant, `deployer` for most pipelines.
  `admin` is refused. The role must exist when the token is exchanged (the
  exchange answers `403` otherwise), and its permissions are read again on
  every request, so changing the role changes what live sessions can do.
- **`scopes`** narrow the session, as the scopes of an API token do:
  `dexaflow:deploy` (the default), `dexaflow:run` (for `deploy --trigger` and
  `--paused`/`--active`) or `dexaflow:read`.

## What the pipeline does

`dexaflow ci init github|gitlab|cloudbuild` asks the control plane the
pipeline deploys to which issuers it offers your tenant (`GET
/api/v2/deployDefaults`, with your CLI session), and when one matches the
system it writes the tokenless pipeline:

- **GitHub**: the workflow gets `id-token: write`, and the deploy step asks
  the runner for a token for the entry's audience, with no `DEXAFLOW_TOKEN`
  secret.
- **GitLab**: the job declares `id_tokens: DEXAFLOW_OIDC_TOKEN` with the
  entry's audience, with no `DEXAFLOW_TOKEN` variable.
- **Cloud Build**: the step runs `gcloud auth print-identity-token
  --audiences=<audience> --include-email` as the build's service account, and
  the Secret Manager secret is gone.

`ci/dexaflow-deploy.sh` then exchanges that token with `dexaflow auth ci-token`
and deploys with the session it prints. A `DEXAFLOW_TOKEN` set in the CI system still works when
`DEXAFLOW_OIDC_TOKEN` is not set, so a pipeline can go back to a deploy token
without being written again. The pre-rename `LEOFLOW_OIDC_TOKEN` works too.

`--auth` picks the path: `auto` (the default) writes the tokenless pipeline
when the server offers an issuer for the system and the deploy token path
otherwise, with a note saying why (no issuer for the system, a server that
does not answer, a CLI release before `v0.5.5`); `oidc` fails instead of
falling back; `token` never asks. The `generic` script has no provider to
mint a token, so it keeps the deploy token, unless your CI system sets
`DEXAFLOW_OIDC_TOKEN` and runs `dexaflow auth ci-token` itself.

## Watching the exchange

- **Metric.** `dexaflow_ci_oidc_exchange_total{issuer, outcome}` counts the
  exchanges by entry and outcome: `issued`, `invalid_token` (signature,
  `alg`, audience, expiry or lifetime), `unknown_issuer`, `no_rule`,
  `role_missing`, `keys_unavailable` and `error`. A token that names no entry
  is counted under `issuer="unknown"`.
- **Logs.** Each exchange logs the entry, the tenant, the job's `sub` claim,
  the role and the outcome; a refusal adds the reason. Neither the token nor
  its signature is logged.
- **Audit.** Each exchange is recorded in the entry's tenant as
  `ci_oidc.exchange`, with the entry, the subject, the role and the outcome.
- **Answers.** `401` for a token that is not valid or names no entry, `403`
  when no rule matches or the role is missing, `503` when the provider's keys
  cannot be read (try again), `429` after 30 refused exchanges in a minute
  from one address (issued sessions do not count).

A session carries the entry's name and a fingerprint of the entry: changing
or removing the entry ends its live sessions on their next request. A session
works in the `Authorization` header only, never as a browser cookie.

## Python on the runner

The `dexaflow compile` step (which `deploy` runs) needs Python 3.11, 3.12, or
3.13 on `PATH` as `python3` to parse `dag.py`. **Bring your own Python** on the
runner; the managed CPython that `dexaflow setup` can download is meant for a
developer laptop, not for build pipelines, where it adds ~50 MB to every run and
bypasses your runner's pin/caching.

The recommended path on each runner type:

| Runner | Recipe |
|---|---|
| **GitHub Actions** | Add `actions/setup-python@v5` with `python-version: '3.12'` before installing dexaflow. Cached automatically. |
| **GitLab CI** | Run the job in the [`dexaflow-cli` image](#the-dexaflow-cli-image) (the job `dexaflow ci init gitlab` writes, from `v0.5.4`), or on `python:3.12-slim`. A job that needs Docker in Docker runs on `docker:27` and installs `python3` with `apk add python3`. |
| **Argo CD, Argo Workflows, Flux, Tekton, Jenkins on Kubernetes** | Run the job in the [`dexaflow-cli` image](#the-dexaflow-cli-image): it ships Python 3.13, git and uv next to the CLI ([recipes](/operate/cicd-deploy/recipes/#recipes-for-argo-workflows-tekton-jenkins-buildah-and-kaniko)). |
| **Cloud Build** | Run the step on an image with Python 3.11 or later: the generated step installs Debian's `python3` on `gcr.io/google.com/cloudsdktool/cloud-sdk:slim`, which also has the gcloud session the CLI reads for Artifact Registry. `gcr.io/cloud-builders/docker` is Ubuntu 20.04, whose `python3` is 3.8. |
| **CodeBuild** | Select Python 3.11 or later under `runtime-versions` in the buildspec. |
| **Self-hosted runners** | Pin Python via your image baseline (`apt install python3.12` or `pyenv`) and version-lock in your runner provisioning. |
| **Generic Docker-in-Docker** | Base your build container on `python:3.12-slim` (gives you Python + a Debian userland for the `docker build` shell). |

Older Python (≤3.10) fails the compile cleanly: `dexaflow compile` errors out
with the version requirement, not a confusing traceback.

## No `dexaflow setup` in CI

`dexaflow compile` and `dexaflow deploy` extract the parser bundled in the binary
into `~/.dexaflow/pysrc/` on their own the first time they run (and again after a
binary upgrade), so a runner needs no setup step. Install the binary with
`DEXAFLOW_NO_SETUP=1`: `dexaflow setup` would also prepare Lite (credentials,
datastore) and may download a managed Python, none of which a pipeline uses.

Caching `~/.dexaflow/pysrc/` between runs is optional; the extraction takes a
few seconds.

## Installing the CLI in a pipeline

A pipeline pins the CLI it runs, so a release does not change what the next
run deploys. From `v0.5.4` on, the release ships two ways to do that without
piping `install.sh` into a shell: a GitHub Action, and a container image for
every system that runs a job in a container.

## The setup-dexaflow action

```yaml
      - uses: dexadata/dexaflow/.github/actions/setup-dexaflow@<VERSION>
        with:
          version: <VERSION>
          server-url: https://cluster.example.com   # optional
```

The action installs the release named by `version` for the runner's OS and
architecture (Linux or macOS, amd64 or arm64), and puts `dexaflow` on `PATH`
for the rest of the job. On every run it installs cosign v2.4.3 (and uses
that one, not a cosign already on the runner), verifies the keyless
signature of the release's `checksums.txt` against the release workflow at
the `v`-prefixed tag, and checks the archive against `checksums.txt`; only
then is the binary extracted. The release files (`checksums.txt`, its
signature and certificate, the archive) are cached with `actions/cache` per
version, platform and `verify-signature` setting. A cache hit saves the
downloads only: the cached files are verified again like downloaded ones.

| Input | Default | What it does |
|---|---|---|
| `version` | the ref of the action, only when it is a release tag | The release to install, `vX.Y.Z` or `vX.Y.Z-rc.N`, any release from `v0.5.0` on. Set it: with a commit SHA, a branch or a local path the action fails without it, and `latest` is refused. |
| `server-url` | none | Exported as `DEXAFLOW_SERVER_URL` for the later steps. |
| `verify-signature` | `true` | `false` skips the signature check and the cosign install (the checksum is still checked), for a runner that cannot reach Sigstore. |

| Output | Value |
|---|---|
| `version` | The installed tag (`v0.5.4`). |
| `path` | The absolute path of the `dexaflow` binary. |
| `cache-hit` | `true` when the release files came from the cache (they were verified all the same). |

To pin the action by commit, as for any third-party action:
`uses: dexadata/dexaflow/.github/actions/setup-dexaflow@<sha> # v0.5.4` with
`version: v0.5.4`.

The action installs only the CLI. `compile` still needs a Python 3.11 to 3.13
on `PATH` ([Python on the runner](#python-on-the-runner)), which GitHub's
hosted runners have; `actions/setup-python` pins one.

## The dexaflow-cli image

`ghcr.io/dexadata/dexaflow-cli:<VERSION>` (also tagged without the `v`) is
the CLI of that release for `linux/amd64` and `linux/arm64`, with what
`dexaflow deploy` runs next to it: Python 3.13 for `compile`, git for the
version label and `--changed-since`, [uv](/operate/builders/#building-the-dependency-image-from-wheels)
for the daemonless dependency layer, and the CA certificates. It has no
Docker CLI: a deploy from it takes the `skip`, `code-layer`,
`dependency-layer` or `remote` [builder](/operate/builders/), so a DAG whose
image needs a real build (a package with no wheel, `system_packages`, its own
`Dockerfile`) needs a remote builder, a job with a daemon (the GitLab
recipe under [Examples](/operate/cicd-deploy/pipelines/#examples-what-dexaflow-ci-init-writes)), or buildah
or kaniko in the cluster ([recipes](/operate/cicd-deploy/recipes/#recipes-for-argo-workflows-tekton-jenkins-buildah-and-kaniko)). The Cloud Build remote builder runs
`gcloud`, which the image does not carry. The GitLab job that
`dexaflow ci init gitlab` writes runs in this image, pinned by digest, when no
project needs a daemon.

The entrypoint is `dexaflow`, and the image runs as UID 65532 with
`HOME=/home/dexaflow`. It is signed like the other images; the identity
names the `v`-prefixed tag, the ref the release workflow ran at:

```bash
cosign verify ghcr.io/dexadata/dexaflow-cli:<VERSION> \
  --certificate-identity "https://github.com/dexadata/dexaflow/.github/workflows/release.yaml@refs/tags/<VERSION>" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

The CLI reads registry credentials from the docker config
([Registry credentials](/operate/builders/#registry-credentials)), so a job
writes one from its CI variables. In GitLab CI, with no Docker daemon and no
privileged runner:

```yaml title=".gitlab-ci.yml"
deploy_dags:
  image:
    name: ghcr.io/dexadata/dexaflow-cli:<VERSION>
    entrypoint: [""]                   # GitLab runs the script in a shell
  variables:
    DEXAFLOW_SERVER_URL: https://cluster.example.com
    GIT_DEPTH: "0"                     # git describe needs history and tags
  rules:
    - if: $CI_COMMIT_BRANCH == "main"
  script:
    - mkdir -p "$HOME/.docker"
    - printf '{"auths":{"%s":{"auth":"%s"}}}' "$CI_REGISTRY" "$(printf '%s:%s' "$CI_REGISTRY_USER" "$CI_REGISTRY_PASSWORD" | base64 -w0)" > "$HOME/.docker/config.json"
    - dexaflow deploy --all --yes      # DEXAFLOW_TOKEN is a masked CI/CD variable
```

The job writes the compiled artifacts under the checkout, which GitLab's
Docker executor leaves writable for any user. The image trusts every
checkout for git (`safe.directory`), since the CI system clones it as another
user.

As an Argo CD hook, the image checks a target against the repository after
each sync; `--dry-run` exits 1 when something would change, which fails the
hook and marks the sync. Drop `--dry-run` and add `--yes` to make the target
match instead ([Syncing a target with the workspace](/operate/cicd-deploy/workflows/#syncing-a-target-with-the-workspace-gitops)):

```yaml title="dexaflow-sync-hook.yaml"
apiVersion: batch/v1
kind: Job
metadata:
  generateName: dexaflow-sync-
  annotations:
    argocd.argoproj.io/hook: PostSync
    argocd.argoproj.io/hook-delete-policy: HookSucceeded
spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      securityContext: { runAsNonRoot: true }
      containers:
        - name: sync
          image: ghcr.io/dexadata/dexaflow-cli:<VERSION>
          command: ["sh", "-c"]
          args:
            - git clone https://github.com/your-org/dags /tmp/dags &&
              dexaflow sync /tmp/dags --dry-run --prune
          env:
            - name: DEXAFLOW_SERVER_URL
              value: https://cluster.example.com
            - name: DEXAFLOW_TOKEN
              valueFrom: { secretKeyRef: { name: dexaflow-deploy-token, key: token } }
```

Flux has no hooks, and it does not run a finished Job again while its spec is
unchanged. Run the same container from a Kubernetes `CronJob`, or from the
pipeline that pushes to the repository.
