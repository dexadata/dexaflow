---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /adr/0003-dag-as-image.html
# --- end AUTO redirect aliases ---
title: "ADR 0003: DAG-as-Image with `leoflow.yaml` Abstraction Layer"
linkTitle: "0003 · DAG-as-Image with `leoflow.yaml` Abstraction Layer"
weight: 30
description: "ADR 0003: DAG-as-Image with `leoflow.yaml` Abstraction Layer"
---

**Status:** Accepted
**Date:** 2026-05-21

## Context

Workflow orchestrators must decide where the user's code lives at execution time. The candidates are:

1. **Shared filesystem (`/dags` mounted on all workers).** This is the classic Airflow model. Requires NFS or PVC, suffers dependency hell, and ties every worker to the same Python environment.
2. **Code serialized into the DAG payload.** The control plane sends the source code directly to the worker. Works for trivial cases, breaks for anything with imports or native dependencies.
3. **Per-DAG container image.** Each DAG declares its own image. The worker pod uses that image. Total isolation.

## Decision

Leoflow uses the **per-DAG container image** model exclusively. Each DAG is identified by:

- A `dag.json` file (the serialized graph and metadata)
- A container image reference (e.g., `myrepo/etl-vendas:v1.2.3`)

These two artifacts move together and are versioned together.

To avoid forcing users to learn Docker, Leoflow provides an abstraction layer: the `leoflow.yaml` file. The developer declares dependencies in YAML; the `leoflow compile` CLI builds the image automatically using an official base image.

## How the Abstraction Works

The developer writes only this:

```yaml
# leoflow.yaml
dag_id: etl_vendas
python_version: "3.11"
dependencies:
  - pandas==2.1.0
  - requests==2.31.0
system_packages:
  - libpq-dev
```

Plus their `dag.py` and `tasks.py` files. Then:

```bash
leoflow compile .
```

The CLI does all of the following:

1. Reads `leoflow.yaml`.
2. Generates a Dockerfile based on the official base image `leoflow/python-runtime:3.11`.
3. Adds the user's system packages via `apt-get install`.
4. Adds Python dependencies via `pip install`.
5. Copies the user's code.
6. Runs `docker build`.
7. Pushes to the configured registry.
8. Emits a `dag.json` with the resulting image reference.

The user never touches Docker. The result is a clean image they would have struggled to write by hand.

## Three Levels of UX

To serve different audiences, Leoflow supports three progressive levels:

| Level | Audience | What the user writes | What Leoflow does |
|---|---|---|---|
| Beginner | Small teams, junior devs | `leoflow.yaml` + Python | Generate Dockerfile, build, push |
| Intermediate | Mid-sized teams | Custom Dockerfile | Build and push with `--dockerfile` flag |
| Advanced | Enterprise with own pipelines | Pre-built image reference | Reference image in `dag.json` directly |

## Official Base Images

> **Note (2026-09, #1031).** The list below is the decision as taken in 2026-05
> and is no longer current — neither the repository name nor the set of Python
> lines. The live list is the `python_version` enum in
> `internal/domain/schemas/leoflow-yaml-schema.json`, published as
> `ghcr.io/dexadata/leoflow-runtime:py<version>` and rendered in the
> [configuration reference](/reference/configuration/#python-version-support).
> `scripts/check-python-runtime-matrix.sh` keeps every copy of it honest; this
> ADR is deliberately not one of them, because a decision record states what was
> decided, not what is true today.

The project maintains three base images from day one:

- `leoflow/python-runtime:3.10`
- `leoflow/python-runtime:3.11`
- `leoflow/python-runtime:3.12`

Each contains:

- The matching Python version
- The `leoflow-agent` binary as entrypoint
- Configured logging, signal handling, healthcheck endpoints
- gRPC client configuration for the control plane

## Rationale

- **Eliminates dependency hell.** Pandas 1.0 and Pandas 2.0 live in different DAGs without conflict.
- **GitOps-friendly.** Images are versioned, immutable, registry-stored. Rollback is a registry tag change.
- **CI-friendly.** Image build + `dag.json` generation are pure CI steps. No magic.
- **Accessible to juniors.** Through `leoflow.yaml`, devs do not need Docker knowledge.
- **Open for advanced users.** Custom Dockerfiles and pre-built images are both first-class.

## Consequences

- **Three Docker images must be maintained.** The project commits to publishing and patching base images for Python 3.10/3.11/3.12.
- **Registry is now part of the deployment.** Users must configure a registry (Docker Hub, GHCR, ECR, GCR, internal). This is a new operational burden compared to Airflow's shared filesystem.
- **Image pull policies matter.** For fast iteration, Leoflow recommends `imagePullPolicy: IfNotPresent` with semantic version tags. Never `latest`.
- **The `leoflow compile` CLI is a critical product surface.** It must be polished, fast, and well-documented.

## Alternatives Rejected

- **Shared `/dags` filesystem:** rejected as the root cause of dependency hell.
- **Code embedded in `dag.json`:** rejected because it breaks on anything beyond trivial scripts.
