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

## Turning it on

```bash
DEXAFLOW_EXECUTION_SOURCE_MODE_ENABLED=true
DEXAFLOW_EXECUTION_SOURCE_MODE_IMAGE=ghcr.io/dexadata/dexaflow-runtime:0.5.3@sha256:<digest>
```

The image must be pinned by digest; the server refuses to start with a tag
alone, so every source-mode task runs the exact image you vetted. Use the
runtime image your release ships, or your own image built on it with the
packages your teams need.

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
before, since `GET /api/v2/dagSources/{dag_id}` already returns it to readers,
but anyone who can read pods in the task namespace can now read it on the
pod too.
