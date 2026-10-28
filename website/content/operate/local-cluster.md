---
title: Try Pro on a local cluster
linkTitle: Local cluster (testing only)
weight: 12
description: Run the Pro deployment path on a throwaway k3d cluster on your machine, from the Helm chart to a first DAG run. For local testing and evaluation, not for production.
---

Pro targets Kubernetes. Before you take it to a real cluster you may want to try
the same deployment path on your machine: to evaluate it, rehearse a demo, check
a chart change or reproduce an issue. This page puts the whole path on a local
[k3d](https://k3d.io/) cluster: a registry the cluster pulls from, throwaway
Postgres and Redis, the Helm chart, and a first DAG built, pushed and run in its
own pod.

{{% alert title="For local testing and evaluation only. Not for production." color="warning" %}}
Everything on this page is disposable by design:

- Postgres and Redis run as single pods on `emptyDir` storage, with a fixed
  password and no auth on Redis. A pod restart loses every DAG, run and user.
- The agent TLS is a self-signed CA the chart generates; nothing rotates it.
- The registry speaks plain HTTP and has no authentication.
- There are no backups, no high availability and no resource sizing.

For a real deployment, follow the [Helm chart](/operate/helm-chart/) page with
managed datastores, and [Deploy your first Pro DAG](/operate/first-pro-dag/)
against a real registry. If all you want is the authoring loop (write a DAG,
run it, look at the result), [Lite](/get-started/quickstart/) needs no cluster
at all.
{{% /alert %}}

## What this assembles

Each step reuses a piece the project already tests in CI, so the recipe does not
drift from what ships:

| Step | Taken from | Where it runs in CI |
|---|---|---|
| A k3d registry the cluster pulls from, `dexaflow deploy` pushing to it | [`test/e2e/deploy-e2e.sh`](https://github.com/dexadata/dexaflow/blob/main/test/e2e/deploy-e2e.sh) | the `E2E deploy (k3d + registry)` job of `ci.yaml` |
| Throwaway Postgres and Redis | [`.github/ci/kind-datastores.yaml`](https://github.com/dexadata/dexaflow/blob/main/.github/ci/kind-datastores.yaml) | the `kind install + upgrade smoke` job of `helm-ci.yaml` |
| `helm install` with an auto-generated agent TLS | the same `helm-ci.yaml` job | every chart change |

The e2e scripts run the control plane as a process on the host. This page runs
it in the cluster from the chart, which is the Pro shape. No CI job runs this
exact combination end to end yet, so if a step here fails while the scripts
above pass, please [open an issue](https://github.com/dexadata/dexaflow/issues).

## Prerequisites

- **Docker**, running. Give it about 4 CPUs, 6 GB of memory and 10 GB of free
  disk.
- **[k3d](https://k3d.io/) v5**, **kubectl** and **Helm** 3.16 or newer.
- The **`dexaflow` CLI** of the release you install, and Python 3.11 or newer
  (`dexaflow setup` once). See [Installation](/get-started/installation/).
- `openssl`, to generate the two secrets.

Pick the release once and reuse it in every step. It is the release tag without
the leading `v`:

```bash
export DEXAFLOW_VERSION={{< latest-version >}}   # the release you are testing
```

The CLI, the chart and the images should be the same release. A released CLI
builds DAG images `FROM ghcr.io/dexadata/dexaflow-runtime:py<python>-v<release>`,
so a CLI newer than the chart can produce an image the control plane was not
tested with.

## 1. A registry and a cluster

```bash
k3d registry create dexaflow-registry.localhost --port 5050
k3d cluster create dexaflow-local \
  --registry-use k3d-dexaflow-registry.localhost:5050 \
  --wait
```

k3d names the registry `k3d-dexaflow-registry.localhost`. The name is chosen so
that the same image reference works on both sides:

- On your machine, `*.localhost` resolves to the loopback, so `docker push
  k3d-dexaflow-registry.localhost:5050/...` reaches the registry over plain HTTP,
  which Docker allows for the loopback without any `insecure-registries`
  setting.
- In the cluster, `--registry-use` configures the node's container runtime to
  pull that same reference from the registry container.

If `docker push` later fails with `no such host`, your resolver does not map
`*.localhost` to the loopback. Add `127.0.0.1 k3d-dexaflow-registry.localhost`
to `/etc/hosts`.

k3d switches your current `kubectl` context to the new cluster
(`k3d-dexaflow-local`). Every `kubectl` and `helm` command below runs against it.

## 2. Throwaway Postgres and Redis

The chart refuses to install without a database and a Redis: Pro never falls
back to embedded datastores. Apply the minimal pair the chart CI uses:

```bash
kubectl create namespace dexaflow
kubectl -n dexaflow apply -f \
  "https://raw.githubusercontent.com/dexadata/dexaflow/v${DEXAFLOW_VERSION}/.github/ci/kind-datastores.yaml"
kubectl -n dexaflow rollout status deploy/postgres --timeout=180s
kubectl -n dexaflow rollout status deploy/redis --timeout=120s
kubectl -n dexaflow exec deploy/postgres -- pg_isready -U leoflow -d leoflow -t 60
```

This creates the Services `postgres` (user, password and database `leoflow`) and
`redis`. The last command holds until Postgres steadily accepts connections, so
the chart's migration Job does not race it on first boot.

## 3. Install the chart

```bash
helm install dexaflow oci://ghcr.io/dexadata/charts/dexaflow \
  --version "$DEXAFLOW_VERSION" \
  -n dexaflow \
  --set taskNamespace=dexaflow \
  --set database.url='postgres://leoflow:leoflow@postgres:5432/leoflow?sslmode=disable' \
  --set redis.url='redis://redis:6379/0' \
  --set auth.jwtSecret="$(openssl rand -hex 32)" \
  --set secretKey="$(openssl rand -hex 32)" \
  --set bootstrap.password=local-admin \
  --wait --timeout 300s
```

What each value does here:

- `taskNamespace=dexaflow` runs the task pods in the release namespace. The
  chart's default is `leoflow`, a namespace this recipe does not create.
- `database.url` and `redis.url` point at the Services from step 2.
- `secretKey` must be exactly 32 bytes; `openssl rand -hex 32` gives the
  64 hex character form. `auth.jwtSecret` signs the session tokens.
- `bootstrap.password` creates the first admin on the first install.
- The agent TLS is left to the chart's default, which generates a CA and a
  server certificate. Do not set `agentTLS.enabled=false`: the chart deploys
  the Pro edition, which refuses to start without TLS on the agent channel.

`helm install` runs the database migrations in a Job before the server starts,
then waits for the Deployment to be ready.

## 4. Reach the UI and the API

```bash
kubectl -n dexaflow port-forward svc/dexaflow 8080:8080
```

Leave it running in its own terminal, then check the control plane answers:

```bash
curl -fsS http://127.0.0.1:8080/readyz
```

Open [http://127.0.0.1:8080/](http://127.0.0.1:8080/) and sign in with
**`admin@dexaflow.local`** and the `bootstrap.password` from step 3
(`local-admin`). The username is an e-mail address: a bare `admin` is refused.

A port-forward is the simplest way in and works on every local cluster. The
chart's Service does not pin a node port, so a fixed port mapping on the
cluster (a kind `extraPortMappings` entry, for example) has nothing stable to
target.

## 5. Your first DAG on the cluster

Log the CLI in to the control plane once. The token is saved in
`~/.dexaflow/config.yaml`, so later commands need no flags:

```bash
printf '%s' local-admin | dexaflow auth login \
  --server http://127.0.0.1:8080 \
  --username admin@dexaflow.local \
  --password-stdin
```

Scaffold a project and point it at the local registry:

```bash
dexaflow init hello_local
cat >> hello_local/dexaflow.yaml <<'EOF'
registry:
  url: k3d-dexaflow-registry.localhost:5050
  image_name: hello-local
EOF
dexaflow validate hello_local
```

Then build, push, register and trigger it in one command, and unpause it:

```bash
dexaflow deploy hello_local --yes --trigger
dexaflow admin dags unpause hello_local
```

`dexaflow deploy` compiles `dag.py` into `dag.json`, builds the DAG image from
the published runtime base (no Dockerfile needed), pushes it to the registry,
pins `dag.json` to the digest the push returned and registers it with the
control plane. `--trigger` creates a run. A DAG registered for the first time
starts paused (the chart's `config.dagsArePausedAtCreation` defaults to
`true`), so the run waits in `queued` and `deploy` prints a note saying so;
the unpause starts it and turns on the DAG's `@daily` schedule, which also
creates one run for the latest tick (`catchup=False`), so a second run appears. See
[New DAGs start paused](/concepts/core-concepts/#new-dags-start-paused).

The image is built for your machine's architecture, which is also the k3d
node's. Do not copy a `build.platforms: [linux/amd64]` setting from a cloud
project onto an arm64 laptop: the task pod would fail with `exec format error`.

To stay with the run until it ends, `dexaflow runs logs hello_local -f`
prints the task states and logs; from the next deploy on, now that the DAG is
unpaused, `dexaflow deploy hello_local --yes --trigger --follow` does the same
and exits non-zero when the run fails. Or watch the run and the task pod:

```bash
dexaflow runs status hello_local
kubectl -n dexaflow get pods -w
```

Each task runs in its own pod, pulled from the local registry by digest. The run
also shows in the UI, with the task logs.

To deploy a change to the DAG, give it a new version:
`dexaflow deploy hello_local --yes --trigger --dag-version v2`. The version
defaults to `git describe`, and outside a git repository to `dev`; registering
`dev` again with different content is refused with a `409` (see
[the `dev` default and its 409](/operate/deploy-prerequisites/#8---dag-version-outside-a-git-repo--the-dev-default-and-its-409)).

## Test a change from a checkout

To try a chart change, or a server or runtime change that has no published image
yet, build the images from a clone of the repository and load them into the
cluster instead of pulling them. Run these from the repository root, on a
cluster made by step 1 with the datastores from step 2 (from a checkout you can
apply the local file, `kubectl -n dexaflow apply -f .github/ci/kind-datastores.yaml`).

```bash
docker build -t dexaflow-server:dev -f deploy/Dockerfile.server .
docker build -t dexaflow-migrate:dev -f deploy/Dockerfile.migrate .
k3d image import dexaflow-server:dev dexaflow-migrate:dev -c dexaflow-local

helm install dexaflow ./helm/dexaflow \
  -n dexaflow \
  --set taskNamespace=dexaflow \
  --set image.repository=dexaflow-server --set image.tag=dev \
  --set migrations.image.repository=dexaflow-migrate --set migrations.image.tag=dev \
  --set database.url='postgres://leoflow:leoflow@postgres:5432/leoflow?sslmode=disable' \
  --set redis.url='redis://redis:6379/0' \
  --set auth.jwtSecret="$(openssl rand -hex 32)" \
  --set secretKey="$(openssl rand -hex 32)" \
  --set bootstrap.password=local-admin \
  --wait --timeout 300s
```

The images keep the chart's `pullPolicy: IfNotPresent`, so the node uses the
imported copies. After a rebuild, import again and restart the Deployment
(`kubectl -n dexaflow rollout restart deploy/dexaflow`); for a chart change,
`helm upgrade dexaflow ./helm/dexaflow -n dexaflow --reuse-values`.

To test a change to the task runtime (the agent or the Python helper), build
the base image locally and name it in the DAG's `dexaflow.yaml`:

```bash
docker build -t dexaflow-runtime:py3.11-dev \
  --build-arg PYTHON_VERSION=3.11 -f runtime/Dockerfile .
```

```yaml
base_image: dexaflow-runtime:py3.11-dev
```

The base does not need to be in the registry: `dexaflow deploy` builds the DAG
image on top of it and pushes the complete image.

A CLI built from a checkout (`make build`) is not a release, so without
`base_image` its DAG images build from the moving
`ghcr.io/dexadata/dexaflow-runtime:py<python>` tag rather than a pinned
release.

## On kind instead of k3d

The chart part of this page (steps 2 to 4) is what the chart CI runs on
[kind](https://kind.sigs.k8s.io/), and it works there unchanged; load
self-built images with `kind load docker-image` instead of `k3d image import`.
What kind lacks is the built-in registry wiring of step 1: follow kind's
[local registry guide](https://kind.sigs.k8s.io/docs/user/local-registry/) and
use the registry address it gives you as `registry.url` in `dexaflow.yaml`.

## When it doesn't work

| Symptom | Cause | Fix |
|---|---|---|
| `helm install` fails because namespace `leoflow` is not found | `taskNamespace` left at its default | pass `--set taskNamespace=dexaflow`, as in step 3 |
| The server pod crash-loops with "the Pro edition requires TLS on the agent gRPC channel" | `agentTLS.enabled=false` was set | leave `agentTLS` at its default |
| The `dexaflow-migrate` Job fails on its first connection | Postgres was not accepting connections yet | wait with `pg_isready` (step 2), then `helm uninstall` and install again |
| `dexaflow auth login` answers "invalid credentials" | the username is the e-mail address | `--username admin@dexaflow.local` |
| `docker push` fails with `no such host` | your resolver does not map `*.localhost` | add `127.0.0.1 k3d-dexaflow-registry.localhost` to `/etc/hosts` |
| The task pod is in `ErrImagePull` | the cluster was created without `--registry-use`, or the image reference does not name `k3d-dexaflow-registry.localhost:5050` | recreate the cluster as in step 1; check `registry.url` |
| The task pod fails with `exec format error` | the DAG image was built for another architecture | remove `build.platforms` from `dexaflow.yaml` |
| Pods stay `Pending` and the node has a `disk-pressure` taint | the disk Docker uses is below the kubelet's eviction threshold (10 to 15% free by default) | free disk space, for example with `docker image prune` |

For the gates `dexaflow deploy` enforces and their exact errors, see
[Deploy prerequisites & why shortcuts fail](/operate/deploy-prerequisites/).

## Tear it down

```bash
k3d cluster delete dexaflow-local
k3d registry delete k3d-dexaflow-registry.localhost
```

Deleting the cluster deletes the datastores and every run with it. The token
`dexaflow auth login` saved still points at `http://127.0.0.1:8080`; log in
again when you point the CLI at another control plane.
