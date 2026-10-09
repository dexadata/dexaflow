---
title: Source mode
weight: 112
description: Run a Pro DAG straight from its dag.py on a shared runtime image, with no image build.
---

Status: **Pro**, off by default ([ADR 0067](/project/adrs/0067-mcp-run-control-scopes-source-mode/),
issue #1475).

## What it is

In Pro, every DAG normally runs on an image that carries its `dag.py`, built
and pushed before the version is registered. Source mode lets a simple DAG
skip that build: the version names the operator's **runtime image** and
carries its `dag.py` as the version's `source`, and each task pod gets that
file at start.

A version runs in source mode when all three hold:

- `execution.source_mode.enabled` is `true`;
- the version's `image` is exactly `execution.source_mode.image`;
- the version carries a `source`.

Every other version runs exactly as before, and with the mode off Pro ignores
a version's source, as it always has.

Turning the mode on also applies to versions already registered on the runtime
image. Register checks the 128 KiB cap only while the mode is on, so dispatch
checks it again: an attempt whose source is over the cap fails once, with no
retries, and the same message register gives.

## Turning it on

```bash
DEXAFLOW_EXECUTION_SOURCE_MODE_ENABLED=true
DEXAFLOW_EXECUTION_SOURCE_MODE_IMAGE=ghcr.io/dexadata/dexaflow-runtime:0.5.3@sha256:<digest>
```

The legacy `LEOFLOW_EXECUTION_SOURCE_MODE_ENABLED` and
`LEOFLOW_EXECUTION_SOURCE_MODE_IMAGE` names still work; when both forms are
set, the `DEXAFLOW_` one wins. The same keys can go in the server config file
as `execution.source_mode.enabled` and `execution.source_mode.image`. The Helm
chart has no dedicated values for them yet: set the variables through
`extraEnv`.

The image must be pinned by a full digest, `@sha256:` followed by 64
lowercase hex characters; the server refuses to start with a tag alone or a
short digest, so every source-mode task runs the exact image you vetted. Use
the runtime image your release ships, or your own image built on it with the
packages your teams need.

## Registering a source-mode version

`dexaflow deploy` always builds or pins a DAG image, so a source-mode version
is registered on the API, by hand or by your own deploy flow (ADR 0067 §4):

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
3. `POST /api/v2/dags/{dag_id}/versions` with that JSON and a token that may
   write the DAG (with a scoped issuer token, `dexaflow:deploy`).

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
- **Cold pods only.** A [warm worker](/operate/warm-pools/) is started before
  its task is known, so it cannot carry the file. Source-mode versions get no
  warm pool and every task attempt gets its own pod.
- **Read-only working directory.** The task runs from the directory that
  holds `dag.py`, which is read only. Write to `$TMPDIR`, `/tmp` or
  [`/staging`](/operate/staging-volume/), as on any task pod.

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
