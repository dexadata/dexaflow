---
title: Published images
weight: 65
description: "Every container image and chart Dexaflow publishes, how each is tagged, and which tags are immutable."
---

Every release publishes five images and one chart to GitHub Container Registry.
This page says what each one is, who pulls it, and how it is tagged, because the
tag is the part people get wrong.

## What is published

| artifact | what it is | who pulls it |
| --- | --- | --- |
| `ghcr.io/dexadata/dexaflow-server` | the control plane: API, scheduler, UI | the Helm chart, and `docker compose` for the demo |
| `ghcr.io/dexadata/dexaflow-migrate` | schema migrations, run as a Helm pre-install and pre-upgrade hook | the chart's migration Job |
| `ghcr.io/dexadata/dexaflow-runtime` | the task base image, one per supported Python line; plus a `data` variant on Python 3.11 with connector providers and dbt | your DAG image's `FROM`, at `dexaflow compile --build`; the data variant, a [source mode](/operate/source-mode/) runtime |
| `ghcr.io/dexadata/dexaflow-mcp` | the [MCP server](/mcp/), for running it as a service with `--transport http` | your own Deployment or `docker run` |
| `ghcr.io/dexadata/dexaflow-cli` | the `dexaflow` CLI with Python 3.13, git and uv, no Docker (from `v0.5.4`) | [pipelines that run a job in a container](/operate/cicd-deploy/#the-dexaflow-cli-image): GitLab CI, an Argo CD hook |
| `oci://ghcr.io/dexadata/charts/dexaflow` | the Helm chart | `helm install` / `helm upgrade` |

### Names from before the rename

Every image except `dexaflow-mcp` and `dexaflow-cli`, which never shipped under the old name, is also
published under its pre-rename name, from the same build:
`leoflow-server`, `leoflow-migrate` and `leoflow-runtime` carry the same tags and
the same digests as their `dexaflow-*` names, so values files, Dockerfiles and
`FROM` lines that name `leoflow-*` keep receiving new releases.

The chart is published twice as well, from the same templates:
`charts/dexaflow` for new installs and `charts/leoflow` for releases installed
before the rename. The chart name feeds the selector labels and resource names,
and a Deployment's selector cannot change in place, so **upgrade an existing
release with `oci://ghcr.io/dexadata/charts/leoflow`**. (Upgrading it with the
`dexaflow` chart needs `--set nameOverride=leoflow`, which renders the same
thing.)

## How each is tagged

| artifact | tags | mutable? |
| --- | --- | --- |
| `dexaflow-server` | `{{< latest-version >}}` **and** `{{< latest-version v >}}` | no, both point at the same digests |
| `dexaflow-migrate` | `{{< latest-version >}}` **and** `{{< latest-version v >}}` | no |
| `dexaflow-mcp` | `{{< latest-version >}}` **and** `{{< latest-version v >}}` | no |
| `dexaflow-cli` | `{{< latest-version >}}` **and** `{{< latest-version v >}}` | no |
| `dexaflow-runtime` | `py3.11-{{< latest-version v >}}` | no |
| `dexaflow-runtime` | `py3.11` | **yes**, moved by every GA of the newest minor |
| `dexaflow-runtime` (data variant) | `py3.11-data-v<X.Y.Z>`, from the first release after v0.5.6 | no; it has no moving tag |
| the chart | `{{< latest-version >}}` | no |

The server, migrate, MCP and CLI images carry both the bare and the `v`-prefixed tag, and
both resolve to identical digests, so either spelling works.

The versioned tags appear while a release is still being checked. The chart,
the moving `py3.N` tags and the GitHub release marked Latest move only after
every install, upgrade and signature check passed, so a chart version never
references an image that does not exist yet.

{{% alert title="py3.11 moves, py3.11-vX.Y.Z does not" color="warning" %}}
Every GA of the newest minor re-points `dexaflow-runtime:py<version>` at its
own build, once the release has passed its install, upgrade and signature
checks. A release candidate never moves it, and neither does a patch of an
older minor. A `FROM ghcr.io/dexadata/dexaflow-runtime:py3.11` rebuilt next month is
a different base than the same line built today. Pin the versioned tag when you
want a build to reproduce.
{{% /alert %}}

## Which base your DAG image gets

`dexaflow compile --build` writes the `FROM` for you, and it picks between those
two tag shapes **based on the CLI you are running**:

| your `dexaflow` binary | the `FROM` it writes |
| --- | --- |
| a released build (`dexaflow version` shows a clean `X.Y.Z`) | `ghcr.io/dexadata/dexaflow-runtime:py<ver>-v<X.Y.Z>`, immutable |
| a development build (built from source, a dirty tree, or a `git describe` version) | `ghcr.io/dexadata/dexaflow-runtime:py<ver>`, the moving line |

A release pins its own base so a compile from that release reproduces byte for
byte (ADR 0003). A development build has no published versioned base to point
at, so it falls back to the moving line.

This has a consequence worth knowing: **two people compiling the same project
can get different base images**, if one runs a released CLI and the other runs
one built from source. If that matters to you, set `base_image` in
`dexaflow.yaml` explicitly, which overrides both rules and is used verbatim.

`python_version` selects the `py<ver>` part; see
[Python version support](/reference/configuration/#python-version-support) for
the supported lines and the deprecation schedule.

## Verifying what you pulled

Artifacts on the GitHub release are checksummed (SHA-256) and the checksums
file is signed with cosign, keyless. The `dexaflow-server`, `dexaflow-mcp` and
`dexaflow-cli` manifests are signed by digest, so both tag shapes are covered
(the same command verifies `dexaflow-mcp` and `dexaflow-cli`):

```bash
cosign verify ghcr.io/dexadata/dexaflow-server:{{< latest-version >}} \
  --certificate-identity-regexp 'https://github.com/(dexadata|neochaotic)/(dexaflow|leoflow)/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

## Older releases

Every image above is published per release and nothing is deleted, so an older
version stays pullable by its versioned tag. Releases up to v0.4.8 were first published under
`ghcr.io/neochaotic/...` (the repository's previous owner) and stay pullable
there; v0.4.8 is also available under `ghcr.io/dexadata/...`. The exception is the moving
`dexaflow-runtime:py<ver>` line, which only ever names the newest release.
