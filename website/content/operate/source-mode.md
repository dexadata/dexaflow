---
title: Source mode
weight: 112
description: Run a Cluster DAG straight from its dag.py on a shared runtime image, with no image build.
---

Status: **Cluster**, off by default ([ADR 0067](/project/adrs/0067-mcp-run-control-scopes-source-mode/),
issue #1475).

## What it is

In Cluster, every DAG normally runs on an image that carries its `dag.py`, built
and pushed before the version is registered. Source mode lets a simple DAG
skip that build: the version names the operator's **runtime image** and
carries its `dag.py` as the version's `source`, and each task pod gets that
file at start.

A version runs in source mode when all three hold:

- `execution.source_mode.enabled` is `true`;
- the version's `image` is exactly `execution.source_mode.image`;
- the version carries a `source`.

Every other version runs exactly as before, and with the mode off Cluster ignores
a version's source, as it always has.

Turning the mode on also applies to versions already registered on the runtime
image. Register checks the 128 KiB cap only while the mode is on, so dispatch
checks it again: an attempt whose source is over the cap fails once, with no
retries, and the same message register gives.

## Turning it on

```bash
DEXAFLOW_EXECUTION_SOURCE_MODE_ENABLED=true
DEXAFLOW_EXECUTION_SOURCE_MODE_IMAGE=ghcr.io/dexadata/dexaflow-runtime:py3.11-{{< latest-version v >}}@sha256:<digest>
```

The legacy `LEOFLOW_EXECUTION_SOURCE_MODE_ENABLED` and
`LEOFLOW_EXECUTION_SOURCE_MODE_IMAGE` names still work; when both forms are
set, the `DEXAFLOW_` one wins. The same keys can go in the server config file
as `execution.source_mode.enabled` and `execution.source_mode.image`.

With the Helm chart, set the two values under `execution.sourceMode`
(and `deploy.defaults.sourceMode` to advertise the image to deploys, see
[Deploying in source mode](#deploying-in-source-mode)):

```yaml
execution:
  sourceMode:
    enabled: true
    image: ghcr.io/dexadata/dexaflow-runtime:py3.11-{{< latest-version v >}}@sha256:<digest>
```

The chart renders `LEOFLOW_EXECUTION_SOURCE_MODE_ENABLED` and
`LEOFLOW_EXECUTION_SOURCE_MODE_IMAGE` on every control-plane Deployment (the
`api` and the `scheduler` of a split install too) only while `enabled` is
true, and refuses to render an image that is not pinned by a full digest, the
same check the server makes at boot. An install that set the variables through
`extraEnv` keeps working: `extraEnv` renders after the chart's entries, so its
value wins. Move them to the values above and drop the `extraEnv` entries.

The image must be pinned by a full digest, `@sha256:` followed by 64
lowercase hex characters; the server refuses to start with a tag alone or a
short digest, so every source-mode task runs the exact image you vetted. Use
the runtime image your release ships, or your own image built on it with the
packages your teams need.

A source-mode DAG can import only what the image holds, and the base runtime
holds the Task SDK and little else. From v0.5.7, each release
also ships a **data variant**, `dexaflow-runtime:py3.11-data-v<X.Y.Z>`: the
base plus the Airflow providers for Postgres, MySQL, Amazon, Google,
Databricks, Snowflake, HTTP and common SQL, and dbt with its Postgres,
Snowflake, BigQuery and Databricks adapters. Its pinned list, with hashes, is
[`runtime/data/requirements.txt`](https://github.com/dexadata/dexaflow/blob/main/runtime/data/requirements.txt).
It is about 1.6 GB installed (about 500 MB compressed), against about 200 MB
for the base, so a task pod on a fresh node pulls more.

## Deploying in source mode

`dexaflow deploy` registers a source-mode version when the target it deploys
to names the runtime image as `source_mode_image`. Set it on the target in
`dexaflow.workspace.yaml`:

```yaml
targets:
  dev:
    server: https://cluster.example.com
    source_mode_image: ghcr.io/dexadata/dexaflow-runtime:py3.11-{{< latest-version v >}}@sha256:<digest>
```

or on a target of your config file (`dexaflow target add dev
https://cluster.example.com --source-mode-image <image>`, with `--global` inside
a workspace). The value must be `execution.source_mode.image` byte for byte;
`deploy` and `target add` refuse an image that is not pinned by a full
digest, since the server never runs one in source mode.

The platform team can instead advertise the runtime image for the whole
cluster, so no repository has to copy it:

```yaml
deploy:
  defaults:
    sourceMode: true    # requires execution.sourceMode.enabled
```

The server then serves its own `execution.source_mode.image` as
`source_mode_image` on `GET /api/v2/deployDefaults`
([cluster deploy defaults](/operate/workspaces-and-targets/#cluster-deploy-defaults)).
The precedence is that of the other deploy defaults: the target's
`source_mode_image`, then `defaults.source_mode_image` in the workspace file,
then the cluster, with two rules of its own:

* The cluster's image applies only when no local source chose a registry
  (`--registry`, `DEXAFLOW_REGISTRY`, `registry.url` in `dexaflow.yaml`, a
  target, the workspace defaults or the config file): a registry you set
  means you build, so a cluster that starts advertising source mode does not
  turn your deploy into another one.
* A local `source_mode_image` that differs from the cluster's is refused,
  naming both values and where each came from: the server runs a
  source-mode version only on its own runtime image, so that deploy could
  never run.

To build anyway, set `source_mode_image: none` on a target or in the
workspace defaults, which clears the image a less specific layer or the
cluster sets, or pass `--no-source-mode` to one `deploy` (and to `status`,
so it computes the same label). `target add --source-mode-image none`
writes that value.

The value must also be an image reference by the usual grammar (lowercase
repository, no whitespace or control characters); the deploy prints it with
control and format characters removed.

Before anything else the deploy prints the image and where it came from:

```text
server:     https://cluster.example.com (target dev, from default_target in /work/dags/dexaflow.workspace.yaml)
image:      ghcr.io/dexadata/dexaflow-runtime:py3.11-{{< latest-version v >}}@sha256:<digest> (source mode, from target "dev" in /work/dags/dexaflow.workspace.yaml): dag.py runs on this runtime image, nothing is built or pushed
```

`dexaflow deploy <dag_id>` then:

1. refuses a DAG that needs a build, before any network call when the
   setting is local (after reading the cluster's defaults when it comes from
   there), and names every reason: a `requirements.txt` or a `Dockerfile`
   beside `dag.py`, other Python modules beside it, a subdirectory with
   Python files (a package), or `dependencies`, `connectors`,
   `system_packages`, `include_paths`, `base_image`, `build.dockerfile`,
   a `build.context` other than `.`, `dbt` or `dbt_groups` in
   `dexaflow.yaml`. The project is walked as the content label walks it, so
   `exclude_paths` and the `.dockerignore` apply; Python test files
   (`test_*.py`, `*_test.py`, `conftest.py`) and an empty top-level
   `__init__.py` do not count. Other files a build would have copied (data,
   SQL, templates) do not refuse the deploy, but a warning names them, since
   the task will not see them. `--dockerfile` is refused, and `--registry`,
   `--platform`, `--rebuild` and `--builder` print a note that they change
   nothing;
2. runs the usual [preflight](/operate/cicd-deploy/#checks-before-the-build-preflight)
   of the control plane and the token (there is no registry to check);
3. compiles `dag.json` with exactly the runtime image, and checks that it
   carries `source` and that the source is at most 131072 bytes, the check
   the register makes;
4. sends it to the server's dry run (when the server offers one), then
   registers it. Nothing is built or pushed, and no registry is needed.

`--plan` works the same way: it says `build no (source mode)` and
`push no (source mode)`, and `--output json` marks the plan with
`"source_mode": true`. The content label hashes the runtime image too, so
a new runtime image is a new version, and the check that a label is already
the current version works as for any deploy; `dexaflow status` computes the
same label. `deploy --watch` registers each saved `dag.py` the same way.

To deploy one DAG that needs a build to a target that sets
`source_mode_image`, pass `--no-source-mode` or use another target.

### By hand

Any deploy flow of your own can register the same version without
`dexaflow deploy` (ADR 0067 §4):

1. Compile the DAG with the runtime image and no build:

   ```bash
   dexaflow compile --image "$RUNTIME_IMAGE" --output dag.json
   ```

   `$RUNTIME_IMAGE` must be byte for byte the value of
   `execution.source_mode.image`. Another spelling of the same image (a tag
   added before the digest, a `docker.io/library/` prefix) does not match:
   the version then runs the bare runtime image and its tasks fail with
   `ModuleNotFoundError: No module named 'dag'`.
2. Check that `dag.json` carries `source` (the `dag.py` text the compiler
   captures) and that it is at most 131072 bytes.
3. Register it:

   ```bash
   dexaflow push dag.json --server https://cluster.example.com
   ```

   `push` sends the file as it is to `POST /api/v2/dags/{dag_id}/versions`.
   The token must be allowed to write the DAG (with a scoped issuer token,
   `dexaflow:deploy`).

A `400` names the problem: no source, or a source over the cap.

## What a source-mode DAG can and cannot do

- **One file.** The task gets `dag.py` and nothing else: no sibling modules,
  no `requirements.txt`, no data files.
- **Only the runtime image's packages.** A DAG that imports anything else
  fails at import time.
- **No `dexaflow.yaml` build steps.** There is no build, so nothing from the
  build section runs.
- **128 KiB at most.** Registering a version on the runtime image answers
  `400` when its source is empty or over 131072 bytes. The cap is fixed,
  not a setting.
- **Cold pods, unless warm pools are on for source mode.** By default every
  task attempt gets its own pod. With
  [`warm_pools`](#warm-workers) on, attempts can also run on
  [warm workers](/operate/warm-pools/), which receive the `dag.py` with each
  assignment.
- **Read-only working directory.** On a dedicated pod the task runs from the
  directory that holds `dag.py`, which is read only. Write to `$TMPDIR`,
  `/tmp` or [`/staging`](/operate/staging-volume/), as on any task pod.

A DAG that needs more builds an image, as today.

## How the file reaches the pod

The Kubernetes executor puts the source in the task pod's
`leoflow.io/dag-source` annotation and projects that annotation into the task
container as a read-only `dag.py` through a downward API volume at
`/opt/leoflow/source`. The container's working directory is that volume, so
the Python runtime imports `dag` from it, as Lite does from its work
directory.

There is no new Kubernetes object, no new RBAC and nothing to clean up: the
file lives and dies with the pod. Pod-per-task, the task NetworkPolicy and
secret delivery are unchanged. The control plane never imports or runs
`dag.py`; it only carries the text. The source is no more exposed than
before through the API, since `GET /api/v2/dagSources/{dag_id}` already
returns it to readers, but the annotation reaches readers outside the
engine's access control:

- anyone who can get or watch pods in the task namespace, which all tenants
  share;
- the apiserver audit log, at the `Request` level and above;
- log pipelines that copy pod annotations onto every record. The Fluent Bit
  `kubernetes` filter and Vector's `kubernetes_logs` source both do by
  default, so every log line of a source-mode task carries up to 128 KiB of
  source, which multiplies log volume and puts the code in the log store.

## Warm workers

A [warm worker](/operate/warm-pools/) is started before its task is known, so
its pod cannot carry the `dag.py` annotation. By default a source-mode version
therefore gets no warm pool, and the register's execution report says
`no warm pool for this version: source mode`.

`execution.source_mode.warm_pools` (Helm: `execution.sourceMode.warmPools`,
environment `DEXAFLOW_EXECUTION_SOURCE_MODE_WARM_POOLS`, default `false`)
lets source-mode versions use warm pools
([ADR 0095](/project/adrs/0095-source-mode-on-warm-workers/)). It needs
`execution.warm_pools_enabled` too. The control plane then sends the
version's `dag.py` with each warm assignment, over the worker's
authenticated assignment stream, together with its SHA-256:

- the worker checks the text against the SHA-256 and refuses the assignment
  when they differ, so the attempt is placed again. This catches a copy
  damaged on the way; it is the same stored source a dedicated pod gets, so
  it says nothing about who wrote it;
- it writes the file as `dag.py` into a directory of its per-attempt scratch
  and runs the attempt from there, as a dedicated pod runs from its projected
  file. A worker that cannot write it ends, and the pool replaces it;
- it removes the file with the rest of the attempt's scratch once the attempt
  is done, before it takes the next one.

On a warm worker the source never goes in a pod annotation, so it does not
reach pod readers, the audit log or a log shipper the way the annotation
does. The working directory differs from a dedicated pod's: it is a writable
directory under the worker's scratch, not `/opt/leoflow/source`, so `__file__`
names that path, and it is wiped after every attempt. A DAG that reads files
next to `dag.py` by an absolute `/opt/leoflow/source` path behaves
differently there; build paths from `__file__` instead.

The worker has to run an agent from 0.5.7 or later, which says so when it
registers. The agent comes with the runtime image, so every warm worker of a
version runs the agent of that version's image, and recycling a worker does
not change it. To turn the setting on:

1. Rebuild your runtime image on a runtime from 0.5.7 or later and set
   `execution.source_mode.image` to its new digest.
2. Deploy every source-mode DAG again, so its versions name the new image. A
   version on the old digest no longer matches the runtime image, so it no
   longer runs in source mode and its tasks would run without their `dag.py`.
3. Turn `execution.source_mode.warm_pools` on.

A warm worker that does not confirm source delivery is never given a
source-mode attempt: the attempt takes a dedicated pod, and the server logs a
warning with `reason=warm_worker_no_source_delivery` and counts it once per
worker in
`dexaflow_scheduler_decisions_total{decision_type="warm_worker_no_source_delivery"}`.
Such a pool never serves its version, since its workers all run the same
image: when the counter moves, redeploy the DAG on the new image or turn the
setting off.

Exclude the annotation in your log shipper before turning source mode on, for
example:

- Fluent Bit: `Annotations Off` in the `kubernetes` filter, or a
  `record_modifier` / Lua step that removes
  `kubernetes.annotations['leoflow.io/dag-source']`;
- Vector: `del(.kubernetes.pod_annotations."leoflow.io/dag-source")` in a
  `remap` transform, or turn off pod annotation enrichment.

The control plane's own pod cache drops the annotation, so it does not hold
the source of every task pod in memory.

The annotation can be changed after the pod is created by anyone with `patch
pods` in the task namespace, and the kubelet refreshes `dag.py` when it is.
Keep `patch pods` there to the control plane.
