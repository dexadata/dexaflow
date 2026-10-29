---
title: Compatibility
weight: 64
description: "The platforms Dexaflow runs on and talks to, per release line, and whether each is tested in CI, supported or best effort."
---

What each release line runs on and talks to. Read it before installing and
before upgrading your cluster, database or Python line.

Every cell carries one of three marks:

- **Tested in CI**: a CI job runs Dexaflow against it; the job is linked.
- **Supported**: expected to work and fixed when it does not, but no CI job
  runs it.
- **Best effort**: not tested and not promised; reports are welcome.

## 0.5.x

| Platform | Version | Status | Evidence |
|---|---|---|---|
| Kubernetes | the node image of the pinned kind release | Tested in CI | [`helm-ci.yaml`](https://github.com/dexadata/dexaflow/blob/main/.github/workflows/helm-ci.yaml) (chart install on kind), [`e2e-daemonless-deploy.yaml`](https://github.com/dexadata/dexaflow/blob/main/.github/workflows/e2e-daemonless-deploy.yaml) (k3d) |
| Kubernetes | EKS and GKE Standard, current versions | Supported | validated by hand per release candidate ([cluster validation runbook](https://github.com/dexadata/dexaflow/blob/main/test/release/rc-cluster-validation.md)) |
| Kubernetes | version skew across minors | Best effort | not tested ([#526](https://github.com/dexadata/dexaflow/issues/526)) |
| Helm | v4.2 | Tested in CI | [`helm-ci.yaml`](https://github.com/dexadata/dexaflow/blob/main/.github/workflows/helm-ci.yaml) |
| Helm | v3 | Supported | the chart is `apiVersion: v2`, which Helm 3 installs |
| PostgreSQL | 16 | Tested in CI | [`ci.yaml`](https://github.com/dexadata/dexaflow/blob/main/.github/workflows/ci.yaml) (integration tests, E2E Lite), [`helm-ci.yaml`](https://github.com/dexadata/dexaflow/blob/main/.github/workflows/helm-ci.yaml) |
| PostgreSQL | 13 and later | Supported | 13 is the minimum ([datastore compatibility](https://github.com/dexadata/dexaflow/blob/main/helm/dexaflow/README.md#datastore-compatibility)) |
| PostgreSQL | managed by `dexaflow lite` on Ubuntu 22.04 and 24.04 | Tested in CI | [`lite-managed-pg-distros.yaml`](https://github.com/dexadata/dexaflow/blob/main/.github/workflows/lite-managed-pg-distros.yaml) |
| Redis | 7 | Tested in CI | [`ci.yaml`](https://github.com/dexadata/dexaflow/blob/main/.github/workflows/ci.yaml), [`helm-ci.yaml`](https://github.com/dexadata/dexaflow/blob/main/.github/workflows/helm-ci.yaml) |
| Redis | 6.0 and later | Supported | ACLs are required, so Redis 5 and earlier do not work |
| Python (DAG runtime) | 3.11 | Tested in CI | [`ci.yaml`](https://github.com/dexadata/dexaflow/blob/main/.github/workflows/ci.yaml) (`python-runtime`, `python-parser`) |
| Python (DAG runtime) | 3.12, 3.13 | Supported | a `dexaflow-runtime` base is built, scanned and published for each on every release ([`release.yaml`](https://github.com/dexadata/dexaflow/blob/main/.github/workflows/release.yaml), [published images](/reference/published-images/)) |
| Python (DAG runtime) | 3.10 | Retired | last published by 0.5.7; see [`python_version`](/reference/configuration/) |
| Airflow REST API | `/api/v2` of Airflow 3.2.x | Supported | the surface the CLI, `pkg/client` and the UI use; [API compatibility](https://github.com/dexadata/dexaflow/blob/main/.github/workflows/api-compat.yaml) fails a pull request that breaks Dexaflow's own published spec |
| Airflow UI | 3.3.2, embedded | Tested in CI | `ui-smoke` in [`ci.yaml`](https://github.com/dexadata/dexaflow/blob/main/.github/workflows/ci.yaml); see [UI compatibility](/concepts/ui-compatibility/) |
| Binaries (`dexaflow`, server, agent, MCP) | Linux and macOS, amd64 and arm64 | Tested in CI on Linux amd64; built for all four | [`release.yaml`](https://github.com/dexadata/dexaflow/blob/main/.github/workflows/release.yaml) (install smoke) |
| Container images | linux/amd64, linux/arm64 | Supported | [published images](/reference/published-images/) |
| Windows | any | Best effort | no Windows binary is published |

## Keeping this page true

A row changes in the same pull request that changes what it describes: a new
Python line, a datastore image bump in CI, a Helm or kind pin. Before each
minor release the release owner reads it against the CI jobs it links
([RELEASING.md](https://github.com/dexadata/dexaflow/blob/main/RELEASING.md)).
