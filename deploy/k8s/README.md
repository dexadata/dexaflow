# Leoflow Pro on GKE — reproducible test cluster

> **Cloud-portable.** This setup is not GKE-specific — it runs unchanged on
> **EKS / GKE / AKS** (and vanilla clusters): it needs only cert-manager, the
> local Leoflow chart, and the official Postgres/Redis charts. The `gke` scripts
> here are simply where it started; the directory is named `deploy/k8s` because
> the recipe is cloud-agnostic.

Scripted, parameterized provisioning of a **GKE cluster for testing & finalizing
the Leoflow Pro Helm chart**. Everything is driven by environment variables and
reads your active `gcloud` project, so **no environment-specific id, account, or
secret is committed here** — only placeholders.

> ⚠️ This is sized for an **initial functional test**, not production and not the
> load test. See [Scaling up later](#scaling-up-later).

## Why GKE Standard (not Autopilot)

The goal is to validate the Helm chart against a **generic, vanilla-ish
Kubernetes** so it works on any cluster (GKE/EKS/AKS/on-prem). Autopilot works
against that:

| | Autopilot | **Standard (this setup)** |
|---|---|---|
| Chart resource requests/limits | rewritten to Autopilot minimums | applied as-is |
| Pod-per-task executor | extra provisioning latency + own bin-packing | real, observable scheduling |
| "Any K8s" fidelity | Autopilot ≠ vanilla (own admission/security) | close to a standard cluster |
| NetworkPolicy / PSA / HPA / PDB | several restricted or mutated | exercised explicitly |
| Idle cost | per provisioned pod | Spot node pool, scale-to-1, delete on demand |

## Design

- **GKE Standard, zonal** (single zone) — first zonal cluster's management fee is
  covered by the GKE free tier.
- **`us-central1` (Iowa)** — among the cheapest US regions.
- **One Spot node pool**, `e2-standard-2` (2 vCPU / 8 GB), **autoscaling 1→3**,
  `pd-standard` boot disk — near-zero idle cost.
- **Dataplane V2** (Cilium) — so the chart's NetworkPolicy template is enforced.
- **Workload Identity** enabled — for future keyless cloud auth (#56).
- **Datastores: in-cluster plain official Postgres + Redis** (Pro requires both)
  — ephemeral emptyDir, cheapest way to iterate on the chart. (We use the
  upstream `postgres`/`redis` images rather than the Bitnami charts: Bitnami
  moved versioned images to `docker.io/bitnamilegacy` / behind a paywall in
  Aug 2025, breaking pinned chart installs.) Managed (Cloud SQL + Memorystore)
  is the realistic-prod path we switch to for the load test.

## Prerequisites

- `gcloud`, `kubectl`, `helm` installed and `gcloud auth login` done.
- A GCP project with **billing enabled**, set as your active project:
  ```bash
  gcloud config set project <YOUR_PROJECT_ID>
  ```

## Run it

```bash
cd deploy/k8s

# 1. Create the cluster (enables APIs, creates cluster + node pool, kubeconfig, namespace)
./00-create-cluster.sh

# 2. Install cluster add-ons (cert-manager — required by the Pro agent-TLS channel)
./01-install-addons.sh

# 3. Install Leoflow Pro (official Postgres + Redis, agent-TLS, generated secrets)
./02-install-leoflow.sh
```

Override any parameter via env vars, e.g.:

```bash
PROJECT=my-proj ZONE=us-central1-a MACHINE_TYPE=e2-standard-4 MAX_NODES=5 ./00-create-cluster.sh
```

### Parameters (`00-create-cluster.sh`)

| Var | Default | Notes |
|---|---|---|
| `PROJECT` | active `gcloud` project | no id is hardcoded |
| `ZONE` | `us-central1-a` | cheapest US, single zone |
| `CLUSTER` | `leoflow-test` | cluster name |
| `MACHINE_TYPE` | `e2-standard-2` | bump for the load test |
| `MIN_NODES` / `MAX_NODES` | `1` / `3` | autoscaler bounds |
| `USE_SPOT` | `true` | set `false` for load-test stability |
| `DISK_TYPE` / `DISK_SIZE` | `pd-standard` / `50` | cheapest disk |

## What `02-install-leoflow.sh` does

With the cluster + cert-manager up, the Pro install:

1. Generates `database.url`, `redis.url`, `auth.jwtSecret`, `secretKey`,
   `bootstrap.password` into a **gitignored** `values.local.yaml`
   (created once; re-runs reuse it so creds stay stable). The initial admin
   password is printed and stored there.
2. Deploys **official Postgres + Redis** (ephemeral emptyDir, no persistence)
   into the `leoflow` namespace.
3. Issues the agent gRPC server cert via **cert-manager** (self-signed root CA →
   server leaf, SANs for `leoflow.leoflow.svc.cluster.local`), and publishes the
   CA as a `ConfigMap` for `agentTLS.caConfigMap`.
4. `helm upgrade --install` from `helm/dexaflow`, pinning `image.tag` +
   `migrations.image.tag` (default `v0.0.1-prealpha.28`).

Open the UI after install:

```bash
kubectl -n leoflow port-forward svc/leoflow 8080:8080
# http://127.0.0.1:8080/
# login: admin@leoflow.local  (the bootstrap admin email — NOT "admin")
# password: bootstrap.password in values.local.yaml
```

## 03 — managed datastores (Cloud SQL + Memorystore)

The realistic-prod path — **scripted** as `03-managed.sh` (run after `02`):

```bash
./03-managed.sh    # enables APIs, sets up VPC peering, creates Cloud SQL +
                   # Memorystore (private IP), repoints the release, rolls the pod
```

What it does:

1. Enables `servicenetworking` / `sqladmin` / `redis` APIs and sets up **Private
   Services Access** (allocated range + VPC peering) on the cluster's VPC.
2. Creates **Cloud SQL** Postgres (`db-f1-micro`, `--edition=ENTERPRISE`, private
   IP) + database `leoflow`, and **Memorystore** Redis (Basic 1 GB, `--enable-auth`,
   private), both in `us-central1`.
3. Writes a gitignored `values.managed.local.yaml` (reusing `02`'s secrets +
   agent-TLS) and `helm upgrade`s the release at the managed endpoints, then
   `kubectl rollout restart`s the Deployment.

Validated end-to-end on GKE: python / bash + map-reduce + cron
scheduler all `success` against Cloud SQL + Memorystore, and the scheduler showed
no advisory-lock step-down on managed Postgres.

**Known limitations (tracked upstream):**
- Redis runs **AUTH + TLS-off** — the client can't verify Memorystore's CA
  (issue #312). Security = private-IP VPC + AUTH.
- Postgres uses `sslmode=require` (encrypted, not cert-verified); `verify-full`
  needs a mounted CA the chart doesn't expose yet (issue #315).
- The pod is rolled manually because the chart lacks a `checksum/secret`
  annotation (issue #316).
- Prefer **Workload Identity** over inline credentials once the connector
  supports it (the cluster already has the workload pool enabled — issue #56).

> ⚠️ **Hardening checklist for `03-managed.sh` (do NOT inherit the test posture).**
> The in-cluster test runs Redis with **no auth** and Postgres with
> `sslmode=disable` — fine for a namespace-isolated smoke test, **never** for
> managed/shared services. The managed path MUST:
> - **Redis AUTH required** — Memorystore AUTH enabled; URI `rediss://:<token>@…`.
> - **TLS in transit** — Memorystore in-transit encryption on (`rediss://`),
>   Postgres `sslmode=require` (or `verify-full`).
> - **Private IP only** — no public IP on either datastore.
> - **No secrets in git** — endpoints/tokens via generated local values or
>   Secret Manager, same as the test flow.

## Scaling up later (load test)

Re-run with bigger bounds — the script is idempotent for the cluster but you'll
resize the node pool / machine type:

```bash
# bigger, non-Spot nodes for stable load numbers
gcloud container clusters resize leoflow-test --node-pool=default-pool \
  --num-nodes=3 --zone=us-central1-a
# or create a dedicated load-test node pool, and consider managed Cloud SQL + Memorystore
```

## Tear down (stop all cost)

```bash
gcloud container clusters delete leoflow-test --zone=us-central1-a
```

## Security note

Nothing environment-specific lives in this directory: project id is read at
runtime, and all secrets (DB/Redis URLs, JWT/encryption keys, admin password)
are generated locally at install time and kept out of git. Do **not** add
kubeconfig files, `*.tfstate`, secret manifests, or real ids/hostnames here.
