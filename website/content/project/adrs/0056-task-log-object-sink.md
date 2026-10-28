---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /adr/0056-task-log-object-sink.html
# --- end AUTO redirect aliases ---
title: "ADR 0056: Task-log object sink — native dual-SDK (S3 + GCS), keyless-first"
linkTitle: "0056 · Task-log object sink — native dual-SDK (S3 + GCS), keyless-first"
weight: 560
description: "ADR 0056: Task-log object sink — native dual-SDK (S3 + GCS), keyless-first"
---

**Status:** Accepted
**Date:** 2026-08-18
**Relates:** ADR 0035 (cloud-connector auth — keyless-first; §5 "no cloud SDK in core" is scoped here), ADR 0014 (supply-chain stack — the scans these dependencies pass), ADR 0022 (per-run staging volume), ADR 0049 (api/scheduler split — the control-plane role that writes logs), #227 (durable task logs / log PVC)

> **This ADR carves a narrow, explicit exception into ADR 0035 §5.** ADR 0035
> says "No cloud SDK in the Go control plane." That rule is about **connector
> credential resolution** — user-facing, per-DAG, connector-agnostic auth whose
> token exchange belongs *in the task*, never in core. The durable **task-log
> sink** is a different plane: operator infrastructure, chosen once at deploy
> time, writing to one bucket with the pod's own keyless identity. Shipping a
> cloud SDK there does not make core a key manager and does not couple core to a
> DAG connector. This ADR names that boundary so the two rules do not blur.

## Context

Task logs are written by the control plane (it receives them from task pods over
gRPC and persists them under `logs.dir`, #227). The default sink is a
`PersistentVolumeClaim`. On a shared, multi-team cluster that default has two
costs: at `replicaCount > 1` it forces an RWX log volume (NFS / CephFS / EFS /
Filestore), and retention lives in an in-cluster janitor rather than the object
store's own lifecycle policy. Every mature K8s-native orchestrator offloads logs
to object storage instead — **Argo Workflows** and **Kubeflow Pipelines** both do,
each via *per-cloud native clients* (historically `minio-go` for S3-compatible,
`cloud.google.com/go/storage` for GCS), not a generic blob abstraction.

An earlier iteration of this sink used a single S3 client and reached GCS through
its **S3 interop endpoint**. That path is broken for our stance: GCS interop
requires **HMAC keys** and cannot use **Workload Identity**, so it forfeits
keyless — the exact property ADR 0035 makes non-negotiable.

## Decision

1. **The durable log sink MAY use a cloud SDK in the Go control plane.** This is
   the narrow exception to ADR 0035 §5. It is justified because the log sink is
   operator infrastructure, not connector auth: one bucket, chosen at deploy
   time, addressed with the control-plane pod's ambient identity. There is no
   per-DAG credential, no token minting for user code, no connector coupling.

2. **Native dual-SDK behind one seam, not a generic abstraction.** The existing
   `logs.ObjectStore` interface (`Put`/`Get`) is the seam. Two implementations
   satisfy it, each with its provider's own SDK and its own keyless path:
   - **`s3`** — `github.com/aws/aws-sdk-go-v2` (AWS S3, MinIO, Ceph RGW). Keyless
     via IRSA / instance profile.
   - **`gcs`** — `cloud.google.com/go/storage` (Google Cloud Storage). Keyless
     via GKE Workload Identity (Application Default Credentials).
   This mirrors what Argo/Kubeflow do (per-cloud native clients); we use
   `aws-sdk-go-v2` for S3 rather than the now-discontinued `minio-go`.

3. **Keyless-first, consistent with ADR 0035.** The default and recommended auth
   for both providers is the ambient identity — no key material in Leoflow. Static
   S3 keys (`existingSecret`) and a GCS service-account file
   (`logs.sink.credentials_file`) are a **discouraged escape hatch** for dev and
   clusters without an identity broker, exactly the posture ADR 0035 takes for
   connector keys.

4. **Opt-in; `disk` stays the default.** `logs.backend` is `disk` (default) |
   `s3` | `gcs`. Lite and every deployment that does not opt in keep the exact
   on-disk path — byte-identical. A misconfigured object backend (no bucket) fails
   closed at boot (`config.validateLogs`), never silently drops logs.

5. **Deployment surface is one optional values block.** The Helm chart adds
   `logs.sink.{provider,bucket,prefix,…}`; keyless reuses the existing
   `serviceAccount.annotations` passthrough (IRSA for `s3`, Workload Identity for
   `gcs`). No new template, no sidecar, no CRD. Default renders no sink env at all.

6. **Retention is the bucket's, not ours.** `ObjectSink` deliberately does not
   implement `Pruner`; the in-cluster janitor skips it. Bucket lifecycle policy
   owns retention — the operator-native model.

## Consequences

- **Two cloud SDKs enter core's dependency graph** (`aws-sdk-go-v2/service/s3`,
  `cloud.google.com/go/storage`). They are dormant unless an object backend is
  selected, but they are compiled in and widen the supply-chain surface. They pass
  the ADR 0014 scans (govulncheck / Trivy / CodeQL) like any other dependency, and
  are justified per `contributing.md` ("no new dependency without justification").
- **The ADR 0035 boundary is now explicit**, not implicit: connector auth resolves
  in the task with no core SDK; infrastructure sinks may use an SDK in core with
  the pod's keyless identity. A future infrastructure concern (e.g. object-store
  artifact staging) inherits this precedent; a future *connector* does not.
- **RWX log PVC becomes optional** at `replicaCount > 1`: point the sink at a
  bucket and set `logs.persistence.enabled: false`.
- **Symmetric keyless story across clouds** — EKS/IRSA and GKE/Workload Identity
  both work with no key on disk, which the S3-interop path could not deliver.

## Alternatives considered

- **Generic blob abstraction (`gocloud.dev/blob`).** Rejected: stalled releases,
  an alpha-shaped API, and the heaviest transitive surface of the options — for a
  two-provider need that the tiny `ObjectStore` seam already abstracts. Neither
  Argo nor Kubeflow use it.
- **`minio-go` as a single S3-compatible client (incl. GCS interop).** Rejected:
  `minio-go` is discontinued (archived 2026), and GCS interop needs HMAC keys —
  no Workload Identity, so no keyless. This is the path we removed.
- **Keep S3-only and tell GCS users to run MinIO/HMAC.** Rejected: forces key
  material on GKE shops that have Workload Identity, directly against ADR 0035.
- **Hold the ADR 0035 §5 line and never add a cloud SDK to core.** Rejected: it
  conflates connector auth with operator infrastructure. The cost of the rule
  (core is not a key manager, not connector-coupled) is fully preserved by the
  keyless, single-bucket, deploy-time sink; the rule's *intent* is not violated.
