---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /deploy.html
# --- end AUTO redirect aliases ---
title: "CI/CD & deploy examples"
linkTitle: "CI/CD & deploy"
weight: 20
description: "Build, push and register DAGs from CI — GitHub Actions, GitLab CI, Cloud Build."
---

{{% alert title="New to Pro? Start with the walkthrough" color="success" %}}
[**Your first Pro DAG (≈10 min)**](/operate/first-pro-dag/) takes one DAG from source
to a running pod with **one command** (`leoflow deploy`), so you see each
artifact boundary before you automate it. This page is the CI recipes for the
same pipeline.
{{% /alert %}}

Deploying a Leoflow DAG is the same everywhere because a DAG is an **immutable
artifact** — a `dag.json` + a container image, versioned together (ADR 0003).

**By hand or for a team without a pipeline,** one command does it all:

```bash
leoflow auth login --server "$DEXAFLOW_SERVER"   # once; stores the token
leoflow deploy --yes                            # compile → build → push → register
```

`leoflow deploy` is the [pipeline-less promotion](/project/adrs/0041-leoflow-deploy-pipelineless/)
— it cross-builds for the cluster, pins the image by digest, and registers the
artifact. `--yes` skips the confirmation prompt (use it in automation).

**In a pipeline,** the same three boundaries are explicit so each can be cached,
gated, and audited independently:

```mermaid
flowchart LR
  E[edit dag.py + dexaflow.yaml] --> C[leoflow compile --build]
  C --> P[push image → registry]
  P --> R[leoflow push dag.json → control plane]
```

1. **`leoflow compile --build`** — parse `dag.py`, overlay `dexaflow.yaml`, run the
   **guardrails** (unknown `task_id`, unsupported operator, duplicate keys), and
   build the DAG image.
2. **push the image** to your registry, tagged by git SHA (immutable).
3. **`leoflow push dag.json`** — register the artifact with the control plane.

{{% alert title="`deploy` vs. the three explicit steps" color="info" %}}
They do the same thing. A pipeline can run the one-liner (`leoflow deploy
--yes`) just as well; the explicit steps are shown because most CI systems want
the build and the register as separate, individually-cacheable stages.
{{% /alert %}}

{{% alert title="The guardrails are your CI gate" color="success" %}}
The same checks that warn you locally in `leoflow lite` fail the CI build, so a
bad `dag_id`/`task_id` binding or an unsupported operator never reaches prod.
{{% /alert %}}

{{% alert title="First time? Do it by hand once" color="info" %}}
The [**first Pro DAG walkthrough**](/operate/first-pro-dag/) runs these three steps
manually so you can watch the DAG cross each boundary. Come back here to wire
the same steps into CI.
{{% /alert %}}

{{% alert title="A CI run failing? Check the prerequisites" color="info" %}}
Registry auth, RBAC scope, non-root images, private-registry pulls, and the
`--dag-version`/`409` gotcha are exactly the errors CI hits first, since a
runner has none of a laptop's ambient state (no `docker login`, no
`leoflow auth login` session, no git history for `--dag-version` to resolve
against). **[Deploy prerequisites & why shortcuts fail](/operate/deploy-prerequisites/)**
has the exact error and fix for each.
{{% /alert %}}

## Your repo of DAGs (CI is the recommended path)

In Pro you keep your DAGs in a Git repository — one directory per DAG — and **CI**
turns each into an artifact when it changes. This is the recommended path: a push
triggers the pipeline, no one builds by hand. Each DAG is just **two files** — no
Dockerfile, no `requirements.txt`:

```text
my-dags/                      # your Git repo
├── .github/workflows/
│   └── deploy-dag.yml        # the CI below (the recommended path)
└── dags/
    ├── my_pipeline/
    │   ├── dag.py            # the DAG (TaskFlow / operators)
    │   └── dexaflow.yaml      # id, python_version, dependencies, registry
    └── another_pipeline/
        ├── dag.py
        └── dexaflow.yaml
```

`leoflow compile --build` **synthesizes the image from `dexaflow.yaml`** — `FROM`
the published Leoflow base, your deps/connectors installed, your DAG copied in.
No Dockerfile to maintain. (Ship your own `Dockerfile` only if you want full
control; it is then used verbatim. Our [examples](/author-dags/examples/) ship one so you can
`docker build` and inspect them by hand.)

The built image is **your** artifact — push it wherever you like (Docker Hub, ECR,
Artifact Registry, ACR, GHCR, a private registry), via the `registry:` block in
`dexaflow.yaml` or `--image`. The only image Leoflow owns is the base your DAG
layers on.

**The mental model:** a push that touches `dags/my_pipeline/**` triggers CI for
*that* DAG only (the `paths:` filter), which compiles → builds (from yaml) →
pushes the image to your registry → registers `dag.json`. The control plane runs
the new version on the next trigger. One DAG per pipeline keeps blast radius
small: a broken `another_pipeline` never blocks `my_pipeline`.

{{% alert title="Building several DAGs at once: `leoflow build`" color="info" %}}
The per-DAG pipeline above stays the recommendation, and the `paths:` filter is
the point of it — one DAG per pipeline is what keeps a broken DAG from blocking
its neighbours.

`leoflow build [workspace]` is for the times you are **not** in that pipeline:
after a base-image bump that every DAG must pick up, when you clone the repo onto
a new machine, or when you want to see all the images a change produces before
pushing anything. It walks the workspace and builds each project with the image
that project's own `registry:` block derives — the same derivation `deploy` uses,
so nothing about naming is new:

```bash
leoflow build my-dags/           # build every project under my-dags/
leoflow build my-dags/ --push    # ...and push each to its registry
```

Two behaviours matter more than the loop itself:

- A project that declares **no `registry:`** is reported by name, not skipped
  quietly. A command that covers less than the workspace without saying so leaves
  you believing images exist.
- A failure **stops** the run, names the project, and lists what was already
  built. A half-built workspace — some images new, some stale — is invisible
  afterwards.

With no `--dag-version` / `--sha` it derives them the same way `deploy` does —
`git describe`, and the short commit hash. (`deploy` has no `--sha` flag at all;
it always derives the sha.)

Using it **as** your CI, in place of per-DAG pipelines, is a deliberate trade:
one job rebuilds everything on any change, so blast radius and build time both
grow with the repo. Choose it only if you want that.
{{% /alert %}}

{{% alert title="Tag images immutably" color="success" %}}
The recipes tag by git SHA (`my_pipeline:$GIT_SHA`), never `:latest`. The
image a `dag.json` points at is then frozen — re-running an old DAG version
pulls exactly the bytes it shipped with, and a rollback is a re-`push` of the
older `dag.json`.
{{% /alert %}}

## Prerequisites
- The `leoflow` CLI on the runner (download the release binary, or `go install`).
- **Python 3.11+ on the runner** (`leoflow compile` invokes the stdlib-only
  parser shim — [ADR 0024](/project/adrs/0024-dag-parsing-structural-shim/) — to turn `dag.py`
  into `dag.json`). See [Python on the runner](#python-on-the-runner) below.
- A container registry your cluster can pull from.
- `DEXAFLOW_SERVER` (control plane URL) and `DEXAFLOW_TOKEN` (a push token) as CI secrets.

## Python on the runner

The `leoflow compile` step needs Python 3.11, 3.12, or 3.13 to parse `dag.py`.
**Bring your own Python** on the runner — do not rely on `leoflow setup` to
download a managed CPython in CI (that path is designed for first-touch on a
developer laptop, not for build pipelines, where it adds ~50 MB to every run
and bypasses your runner's pin/caching).

The recommended path on each runner type:

| Runner | Recipe |
|---|---|
| **GitHub Actions** | Add `actions/setup-python@v5` with `python-version: '3.12'` before installing leoflow. Cached automatically. |
| **GitLab CI** | Use a `python:3.12-slim` (or `python:3.12-bookworm`) base image instead of a bare `alpine`/`ubuntu`. |
| **Cloud Build / CodeBuild** | Use a `python:3.x-slim` build step, or one of the cloud-provider's "python3.12" images. |
| **Self-hosted runners** | Pin Python via your image baseline (`apt install python3.12` or `pyenv`) and version-lock in your runner provisioning. |
| **Generic Docker-in-Docker** | Base your build container on `python:3.12-slim` (gives you Python + a Debian userland for the `docker build` shell). |

Older Python (≤3.10) fails the compile cleanly — `leoflow compile` errors out
with the version requirement, not a confusing traceback. Newer Python (3.14+)
is accepted by the upper end of the detection range; the range is bumped per
release once the parser shim is re-verified against it.

### One more step: `leoflow setup` extracts the parser

After the `leoflow` binary lands on the runner and Python is in scope, run
`leoflow setup` ONCE per runner. The CLI ships the parser source embedded;
`setup` extracts it under `~/.dexaflow/pysrc/parser/` and writes a config
file pointing the `compile` command at the chosen interpreter. Without this
step `leoflow compile` fails with `No module named leoflow_parser` (the
runner's Python has no idea where the parser lives).

The snippets below all show `leoflow setup` as the step after the install,
before `leoflow compile`. The follow-up to make this implicit (auto-bootstrap
on first compile, or embed the parser execution inside the Go binary) is
tracked separately; for now, calling it explicitly is the
recommended path because it's the operation that decides whether managed
CPython is downloaded, and that's a step CI operators should consciously opt
into.

{{% alert title="Cache `~/.dexaflow/` between CI runs" color="success" %}}
Ephemeral CI runners re-extract the parser on every build (~3-5 s).
Cache the directory keyed by the leoflow binary version (or the leoflow
release URL) to skip it on warm runs:
`actions/cache` on GitHub Actions, `cache:` keys on GitLab CI, the
workspace cache on Cloud Build. With Python on PATH, the cache hit makes
`leoflow setup` a near-no-op. Skip the cache if your runner image
already bakes `~/.dexaflow/pysrc/` in (some self-hosted setups do).
{{% /alert %}}

## Examples

{{< tabpane text=true >}}
{{% tab header="GitHub Actions" %}}

```yaml title=".github/workflows/deploy-dag.yml"
name: Deploy DAG
on:
  push:
    branches: [main]
    paths: ["dags/my_pipeline/**"]
jobs:
  deploy:
    runs-on: ubuntu-latest
    permissions: { contents: read, packages: write }
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-python@v5     # BYO Python — see #python-on-the-runner
        with: { python-version: '3.12' }
      - uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      - name: Install leoflow
        run: curl -fsSL https://github.com/dexadata/leoflow/releases/latest/download/leoflow-linux-amd64 -o /usr/local/bin/leoflow && chmod +x /usr/local/bin/leoflow
      - name: Bootstrap the parser (uses the BYO Python from above)
        run: leoflow setup
      - name: Compile + build + push image
        run: |
          IMAGE=ghcr.io/${{ github.repository }}/my_pipeline:${{ github.sha }}
          leoflow compile dags/my_pipeline --image "$IMAGE" --build --push -o dag.json
      - name: Register with the control plane
        env: { DEXAFLOW_TOKEN: ${{ secrets.DEXAFLOW_TOKEN }} }
        run: leoflow push dag.json --server ${{ secrets.DEXAFLOW_SERVER }}
```
{{% /tab %}}
{{% tab header="GitLab CI" %}}

```yaml title=".gitlab-ci.yml"
deploy_dag:
  # Default docker:27 is Alpine-based; install python3 before leoflow compile.
  # Alternative: a custom base image that bakes Python+Docker together.
  # See #python-on-the-runner for the rationale.
  image: docker:27
  services: [docker:27-dind]
  before_script:
    - apk add --no-cache python3   # 3.12 on Alpine 3.20+; see #python-on-the-runner
  rules:
    - if: $CI_COMMIT_BRANCH == "main"
      changes: ["dags/my_pipeline/**/*"]
  variables:
    IMAGE: $CI_REGISTRY_IMAGE/my_pipeline:$CI_COMMIT_SHA
  script:
    - echo "$CI_REGISTRY_PASSWORD" | docker login -u "$CI_REGISTRY_USER" --password-stdin "$CI_REGISTRY"
    - wget -qO /usr/local/bin/leoflow https://github.com/dexadata/leoflow/releases/latest/download/leoflow-linux-amd64 && chmod +x /usr/local/bin/leoflow
    - leoflow setup        # extracts the parser into ~/.dexaflow/ using the python3 from before_script
    - leoflow compile dags/my_pipeline --image "$IMAGE" --build --push -o dag.json
    - leoflow push dag.json --server "$DEXAFLOW_SERVER"   # DEXAFLOW_TOKEN from CI vars
```
{{% /tab %}}
{{% tab header="Google Cloud Build + Cloud Run" %}}

Build/push on Cloud Build; register against a control plane on Cloud Run. The
DAG image runs as task pods on GKE (pods are the execution unit, not Cloud Run).

```yaml title="cloudbuild.yaml"
steps:
  - name: gcr.io/cloud-builders/docker
    entrypoint: bash
    args:
      - -c
      - |
        # BYO Python — Cloud Builders' docker image is Debian; install python3.
        # See #python-on-the-runner for the rationale.
        apt-get update -qq && apt-get install -y --no-install-recommends python3
        curl -fsSL https://github.com/dexadata/leoflow/releases/latest/download/leoflow-linux-amd64 -o /usr/bin/leoflow && chmod +x /usr/bin/leoflow
        leoflow setup    # extracts the parser into ~/.dexaflow/ using the python3 just installed
        IMAGE="$_REGION-docker.pkg.dev/$PROJECT_ID/dags/my_pipeline:$SHORT_SHA"
        leoflow compile dags/my_pipeline --image "$$IMAGE" --build --push -o dag.json
        leoflow push dag.json --server "$_DEXAFLOW_SERVER"
substitutions:
  _REGION: us-central1
  _DEXAFLOW_SERVER: https://leoflow.run.app
options: { logging: CLOUD_LOGGING_ONLY }
```

{{% alert title="Restricted networks (Cloud Shell): run the build inside Cloud Build" color="success" %}}
{{% /tab %}}
{{< /tabpane >}}

When the local Docker daemon can't reach Google's IPs — e.g. in Cloud
Shell — don't build locally at all. The `cloudbuild.yaml` above runs
`leoflow compile --build --push` **inside Cloud Build** (Google's network,
not your machine): serverless, still yaml-driven, no Dockerfile, no local
egress. Trigger it from Cloud Shell with:

```bash
gcloud builds submit --config cloudbuild.yaml .
```

Cloud Build checks out the source, runs compile + build + push there, and
pushes to Artifact Registry — your machine never touches a Docker daemon.
(Use `gcloud builds submit --tag …` only if you ship your own Dockerfile;
the yaml-driven path has none for it to build.)
    {{% /alert %}}

{{< tabpane text=true >}}
{{% tab header="Generic / Makefile" %}}

Any runner with Docker, **Python 3.11+**, and the `leoflow` CLI
(see [Python on the runner](#python-on-the-runner)):

```bash
leoflow setup    # one-shot per runner: extracts the parser into ~/.dexaflow/
IMAGE="$REGISTRY/my_pipeline:$(git rev-parse --short HEAD)"
leoflow compile dags/my_pipeline --image "$IMAGE" --build --push -o dag.json
leoflow push dag.json --server "$DEXAFLOW_SERVER" --token "$DEXAFLOW_TOKEN"
```
{{% /tab %}}
{{< /tabpane >}}

## Control-plane deployment (Helm chart, in validation)

Deploying the control plane itself (Helm chart, published `leoflow-server`/
`leoflow-migrate` images, TLS on the agent channel, keyless cloud auth) is the
**Pro** track. One command installs the chart with auto-generated TLS and no
cert-manager — from its published OCI artifact
(`helm install leoflow oci://ghcr.io/dexadata/charts/dexaflow --version <VERSION>`),
or from source on `main` for the bleeding edge
(`helm install lf ./helm/dexaflow …`). See [Install Pro](/get-started/installation/#install-pro).
The chart is installable today and in validation — see the
[Helm chart](https://github.com/dexadata/leoflow/blob/main/helm/dexaflow/README.md), the reproducible
[Kubernetes test setup](https://github.com/dexadata/leoflow/blob/main/deploy/k8s/README.md)
(the `deploy/k8s` recipe is cloud-portable — it runs unchanged on EKS / GKE / AKS),
[Operating modes](/concepts/editions/),
and the [Roadmap](/project/roadmap/). The product proves itself in **Lite** first.

See also: [DAG authoring](/author-dags/dag-authoring/) · [Operating modes](/concepts/editions/).
