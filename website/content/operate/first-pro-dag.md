---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /first-pro-dag.html
# --- end AUTO redirect aliases ---
title: Deploy your first Pro DAG
weight: 10
description: Take a DAG from Lite to a Kubernetes control plane — your first Pro deployment.
---

This is the end-to-end Pro path: take one DAG from source to a running task on a
control plane — in **one command**. Where the 2-minute [Lite loop](/contribute/local-dev-loop/)
hides the artifact boundary so you can iterate, Pro makes it explicit, because that
boundary is what `dexaflow deploy` automates for you.

A DAG is an **immutable artifact** — a `dag.json` + a container image, versioned
together ([ADR 0003](/project/adrs/0003-dag-as-image/)). `dexaflow deploy` moves it across
every boundary in one shot:

```mermaid
flowchart LR
  A[dag.py + dexaflow.yaml] --> B[compile → dag.json]
  B --> C[build DAG image<br/>FROM leoflow-runtime]
  C --> D[push image → your registry]
  D --> E[re-pin by digest<br/>+ register → control plane]
  E --> F[runs in a pod]
```

{{% alert title="Two versions of this walkthrough" color="info" %}}
- **The simple path (below)** — a minimal DAG with a `Dockerfile`, deployed
  today with `dexaflow deploy`. Everything here runs as-is.
- **[The complete path](#the-complete-path--your-own-dag-yaml-driven)** — author
  your own DAG with **no Dockerfile** (the build is synthesized from
  `dexaflow.yaml`) and real `connectors:`. That richer flow is still landing;
  it is shown at the end so you can see where this is going.
{{% /alert %}}

## Prerequisites

- **`docker`** (or `podman`/`nerdctl` — pass `--builder`). Used to build and push
  the DAG image. On Docker Desktop, cross-building for the cluster "just works".
- The **`leoflow` CLI** and Python 3.11+ on your machine (`leoflow setup` once).
- **A container registry your cluster can pull from** — anywhere: Docker Hub, GHCR,
  Amazon ECR, Google Artifact Registry, Azure ACR, or a private one. You push the
  DAG image there; the control plane pulls it. (Lite needs none — this is a Pro
  thing.)
- **A reachable Pro control plane.** A throwaway one comes from the
  [Helm chart](/operate/helm-chart/).

## Step 0 — log in once

```console
$ dexaflow auth login --server https://pro.example.com
Username: admin
Password:
Logged in to https://pro.example.com (token saved to ~/.dexaflow/config.yaml)
```

The token is stored, so every later `dexaflow deploy` needs **no auth flags**. The
password is read hidden — it never lands in your shell history. (This is the
control-plane login; it is unrelated to `docker login`, which authenticates your
*builder* to the registry — do that once too: `docker login ghcr.io`.)

## Step 1 — a project (`dag.py` + `dexaflow.yaml` + `Dockerfile`)

```python title="dag.py"
from airflow.sdk import DAG, task

@task
def extract() -> dict:
    return {"rows": 42}

@task
def load(data: dict) -> None:
    print(f"loaded {data['rows']} rows")

with DAG("first_pro_dag", schedule=None) as dag:
    load(extract())
```

```yaml title="dexaflow.yaml"
dag_id: first_pro_dag
python_version: "3.11"
dependencies:
  - requests==2.32.3
registry:
  # Wherever you want the artifact to live — this is just an example.
  # Docker Hub: docker.io/your-user · ECR: <acct>.dkr.ecr.<region>.amazonaws.com
  # Artifact Registry: <region>-docker.pkg.dev/<project>/<repo> · GHCR: ghcr.io/your-org
  url: ghcr.io/your-org
  image_name: first-pro-dag
```

```dockerfile title="Dockerfile"
FROM ghcr.io/dexadata/dexaflow-runtime:py3.11
RUN pip install --no-cache-dir requests==2.32.3
COPY dag.py /home/leoflow/dag.py
ENV PYTHONPATH=/home/leoflow
```

{{% alert title="The base image is ours; you never build it" color="success" %}}
Your image layers `FROM` the **published Dexaflow task base**
(`ghcr.io/dexadata/dexaflow-runtime:py3.11`) — it bundles the `leoflow-agent`
(PID 1, talks gRPC to the control plane) and the `leoflow_runtime` helper, is
multi-arch and signed, and is built by our CI. You only add your deps and copy
your DAG in. (In [the complete path](#the-complete-path--your-own-dag-yaml-driven)
even this Dockerfile goes away — it is synthesized from `dexaflow.yaml`.)
{{% /alert %}}

## Step 2 — deploy (one command)

```console
$ dexaflow deploy
Deploy first_pro_dag -> https://pro.example.com? [y/N] y
…  (compile → build for linux/amd64 → push → register)
Deployed first_pro_dag -> https://pro.example.com
  image ghcr.io/your-org/first-pro-dag@sha256:9f2c…
  registered version 1a2b3c4
```

That one command crossed every boundary:

1. **compile** — parsed `dag.py`, overlaid `dexaflow.yaml`, ran the guardrails
   (unknown `task_id`, unsupported operator, duplicate keys), wrote `dag.json`.
2. **build** — built the image from your `Dockerfile`, **for the cluster's
   architecture** (`linux/amd64` by default, so a macOS/arm64 laptop produces an
   image the cluster can actually run).
3. **push** — pushed it to your `registry:`.
4. **re-pin + register** — captured the image **digest** and wrote
   `…@sha256:…` into `dag.json`, then registered that with the control plane. Pro
   now pulls **exactly** the bytes you built — no `:latest` drift.

{{% alert title="It registers, it doesn't run — by default" color="info" %}}
`deploy` publishes the artifact; a scheduled DAG then runs on its schedule.
Add `--trigger` to kick a run immediately, or trigger from the Airflow UI.
{{% /alert %}}

## Step 3 — trigger and watch it run

```console
$ dexaflow deploy --trigger
…
Deployed first_pro_dag -> https://pro.example.com
  image ghcr.io/your-org/first-pro-dag@sha256:9f2c…
  registered version 1a2b3c4
  triggered run manual__1717525200
```

The scheduler pulls your image, runs each task in its own pod, and the Airflow UI
shows state and logs. That is the whole Pro lifecycle, in one verb.

## When it doesn't work

`deploy` fails loudly and tells you the fix. The four you'll actually hit:

| Symptom | Cause | Fix |
|---|---|---|
| `deploy requires a container registry…` | no `registry:` in `dexaflow.yaml` | add the `registry:` block (Step 1) and `docker login <registry>` |
| `denied` / `unauthorized` on push | builder not logged in to the registry | `docker login ghcr.io` (registry auth ≠ control-plane auth) |
| Task pod: `exec format error` | image arch ≠ cluster arch | already handled — deploy builds `linux/amd64`; for a Graviton cluster pass `--platform linux/arm64` |
| DAG runs but a task fails on a missing `conn_id` | connections live in the control plane, **not** in the image | deploy prints `note: this DAG expects connection(s): …` — create them on Pro (UI/API) first; see [Variables & Connections](/author-dags/variables-connections/) |

Hit something not on this list — an unsupported task type, `CreateContainerConfigError`,
a `401`/`403`, a private-registry `ErrImagePull`, or a `409` on a second deploy?
**[Deploy prerequisites & why shortcuts fail](/operate/deploy-prerequisites/)**
collects every gate `deploy`/`push` enforce, with the exact error and the fix.

## Deploy more than one DAG

```bash
dexaflow deploy <dag_id>     # a specific DAG in a multi-DAG workspace
dexaflow deploy --all        # every DAG in the workspace (best-effort; non-zero exit if any fail)
dexaflow deploy --skip-build # reuse the existing image (skip docker build/push); dag.json is still recompiled from dexaflow.yaml/dag.py
```

## Build in one CI job, deploy in another

`dexaflow deploy` builds, pushes and registers in one command, and that is the
path to use when one machine does all three. A CI/CD pipeline usually splits
them: a build job that produces the image, and a deploy job, often on a
different runner with different credentials, that only registers it.

```bash
# build job
dexaflow compile . --output dag.json --build --push --dag-version "$VERSION"

# deploy job, later, possibly elsewhere
dexaflow deploy . --skip-build --dag-version "$VERSION"
```

The two steps have to name the **same image**, and the name comes from
`registry.tag_strategy` in `dexaflow.yaml`. Pass the same `--dag-version` to
both, and run both from the same commit when the strategy is `git_sha`.

{{% alert title="Prefer tag_strategy: git_sha for a split pipeline" color="info" %}}
With the default `version` strategy the tag is the DAG version label, which
comes from `git describe`, so two runs of the same pipeline on the same commit
produce the same tag and a rebuild silently replaces what is already in the
registry. `git_sha` ties the tag to the commit instead, which is what makes a
deploy reproducible and a rollback a matter of pointing at an older tag.

Either way the artifact is re-pinned by digest after the push, so the tag is a
convenience and not the integrity boundary.
{{% /alert %}}

If the deploy job reports that it cannot find the image the build job pushed,
check `--dag-version` and the commit first. The error names the image it looked
for, which reads like a failed push but is more often the two steps disagreeing
about the tag.

## The complete path — your own DAG, yaml-driven

{{% alert title="Not yet the default path" color="warning" %}}
The flow below is the **complete, Dockerfile-free** authoring experience. The
yaml-driven build (synthesizing the image from `dexaflow.yaml`) is still
landing. On the current release, keep the `Dockerfile` from Step 1.
This section shows where the happy path is going.
{{% /alert %}}

When the yaml-driven build lands, the project is **two files** — no Dockerfile, no
`requirements.txt`:

```python title="dag.py"
from airflow.sdk import DAG, task
from airflow.providers.postgres.hooks.postgres import PostgresHook  # inside a @task

@task
def load() -> int:
    rows = PostgresHook(postgres_conn_id="warehouse").get_first("SELECT count(*) FROM orders")[0]
    print(f"orders: {rows}")
    return rows

with DAG("orders_report", schedule="@daily") as dag:
    load()
```

```yaml title="dexaflow.yaml"
dag_id: orders_report
python_version: "3.11"
connectors:
  - postgres            # installs the provider; the form renders in the UI
registry:
  url: ghcr.io/your-org
  image_name: orders-report
```

```console
$ dexaflow auth login --server https://pro.example.com   # once
$ dexaflow deploy --trigger
note: this DAG expects connection(s): warehouse
      create them on the control plane (UI or API) before the run.
Deployed orders_report -> https://pro.example.com
  image ghcr.io/your-org/orders-report@sha256:… · triggered run manual__…
```

`dexaflow compile --build` synthesizes the image from the yaml — `FROM` our base,
your `connectors:`/`dependencies:` installed, your DAG copied in. The only
Dockerfiles in the repo are the ones under `examples/`.

## From here

- **Automate it.** `dexaflow deploy` is exactly what a pipeline runs on every push —
  see [CI/CD & deploy examples](/operate/cicd-deploy/) for GitHub Actions / GitLab / Cloud Build
  recipes (and the Python-on-the-runner notes).
- **The design.** [ADR 0041](/project/adrs/0041-leoflow-deploy-pipelineless/) records why
  deploy works the way it does (registry mandatory, digest pinning, two-tier path).
