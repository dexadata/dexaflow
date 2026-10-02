---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /installation.html
# --- end AUTO redirect aliases ---
title: Installation
weight: 30
description: Install the leoflow CLI and provision the managed Python runtime.
---

Leoflow ships in **two editions** — pick the install path that matches the one
you want:

| Edition | Where it runs | Who it's for | Install path |
|---|---|---|---|
| **Lite** | Your laptop or a single VM (no Kubernetes, no Docker required) | Local development, small teams, evaluation | [Install Lite](#install-lite) |
| **Pro** | A Kubernetes cluster (pod-per-task executor) | Team-scale and production workloads | [Install Pro](#install-pro) |

See [Editions](/concepts/editions/) for the full feature-by-feature breakdown. The two
editions share the same Go control plane and the same Airflow-compatible
HTTP API — Pro adds the K8s executor, HA scheduler, and external-datastore
expectations; Lite bundles everything in one host process.

---

## Install Lite

One command installs Leoflow Lite and bootstraps everything it needs — **no
sudo, no system Python, no package manager**:

```bash
curl -fsSL https://raw.githubusercontent.com/dexadata/leoflow/main/install.sh | sh
```

That script downloads the release archive for your OS/architecture, verifies
its SHA-256 against the signed checksums, installs the binaries to
`~/.dexaflow/bin`, and then runs [`leoflow setup`](#what-leoflow-setup-does).

### What you need

Almost nothing. The control plane, CLI, and agent are **static Go binaries**,
and `leoflow setup` provisions a Python 3.11 itself if you don't have one.

There are **two execution paths** — and **no Docker executor**, on purpose
([ADR 0015](/project/adrs/0015-kubernetes-only-execution/)):
the Docker Go SDK carries an unfixable advisory (Moby AuthZ bypass,
GO-2026-4887) that would reach the control-plane binary and fail the security
gate. So:

| Executor | Needs | Isolation | For |
|---|---|---|---|
| **subprocess** | just the install (binaries + a managed Python) | none (dev-only) | fast local iteration, small projects |
| **kubernetes** | + Docker (to host a local **k3d** cluster; k3d/kubectl fetched on demand) | real pods | production parity, the staging volume, resource limits |

Docker, when present, is only the engine that **hosts the local k3d cluster** —
it is never an executor itself. `leoflow setup` **detects what's present and
picks the highest path available**; without Docker it uses subprocess. Run
[`leoflow doctor`](#leoflow-doctor) anytime to see where you stand, and see
[Choosing an executor](/contribute/local-dev-loop/#choosing-an-executor) for the trade-offs.

### What `leoflow setup` does

`setup` is idempotent — re-running is safe. It:

1. **Ensures Python 3.11.** Uses a system `python3.11` if one is on `PATH`;
   otherwise downloads a pinned, checksum-verified [relocatable
   CPython](https://github.com/astral-sh/python-build-standalone) into
   `~/.dexaflow/python`. No sudo, no system install.
2. **Extracts the DAG parser and task runtime** (embedded in the binary) to
   `~/.dexaflow/pysrc`.
3. **Points `parser_cmd` at the parser** in `~/.dexaflow/config.yaml`. The parser is
   pure Python with its dependencies vendored (the Airflow shim and PyYAML — ADR
   0024), so there is **no parser venv, no pip, and no Apache Airflow install** — it
   runs on the interpreter from step 1 directly.
4. **Creates your workspace** (default `~/leoflow`, override with `--workspace`)
   for your DAG projects, and asks (on a terminal) for the workspace, executor
   (`subprocess` for local use, `k8s` for a dev mini-cluster — changeable later),
   and UI port. Run non-interactively (e.g. `curl | sh`) it uses sensible defaults.
5. **Creates the Lite admin** (`admin@leoflow.local`) with a generated,
   human-friendly password, **shown once** at the end (only its hash is stored).
   Recover it with `leoflow lite reset-password`.

{{% alert title="Lite is for trusted networks" color="warning" %}}
The admin password is short by design and there is no SSO/RBAC — run Lite
on **localhost, an internal network, or a VPN**, never exposed publicly.
Production-grade deploys are Pro's job. See [Editions](/concepts/editions/).
{{% /alert %}}

Everything Leoflow manages lives under `~/.dexaflow`; your DAG source lives in
the workspace — the two are kept separate.

```bash
leoflow setup                      # interactive on a terminal; defaults otherwise (safe to re-run)
leoflow setup --dry-run            # show the plan, change nothing
leoflow setup --workspace ~/work   # choose where your DAG projects live
```

{{% alert title="There is no scanned `dags/` folder" color="info" %}}
Unlike Airflow, Leoflow has no monolithic DAGs directory. Each DAG is its
own project (`dag.py` + `dexaflow.yaml`); you point `leoflow lite <path>` at
it. The workspace is just a convenient home for those projects.
{{% /alert %}}

### Platforms

Leoflow ships **Linux and macOS** binaries for **amd64 and arm64**. Because
the install never touches your system package manager, the Linux
**distribution does not matter** — only the C library and CPU architecture do:

- **glibc distros** (Ubuntu, Debian, Fedora, RHEL/Rocky/Alma, Arch, openSUSE)
  and **musl** (Alpine) are both supported; `setup` detects musl and fetches
  the matching CPython build.
- **Windows:** use **WSL2** (it's a glibc Linux). Keep your project in the WSL
  **native filesystem** (`~/...`), not under `/mnt/c` — `leoflow lite`'s
  hot-reload uses inotify, which is unreliable on the Windows 9p mount.
  `leoflow doctor` warns when your project is under `/mnt`.

### Verifying the download

The release publishes `checksums.txt` (SHA-256), and the checksums file is
**cosign-signed** (keyless). `install.sh` verifies the archive checksum
automatically. To verify the signature yourself:

```bash
cosign verify-blob \
  --certificate checksums.txt.pem \
  --signature checksums.txt.sig \
  --certificate-identity-regexp 'https://github.com/(dexadata|neochaotic)/leoflow' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
```

### `leoflow doctor`

A read-only diagnostic — it changes nothing:

```console
$ leoflow doctor
leoflow doctor

  platform      linux/amd64 (glibc)
  python 3.11   found (/usr/bin/python3.11)
  docker        found
  k3d           not found (fetched on demand for the k8s tier)
  kubectl       not found (fetched on demand for the k8s tier)

  recommended executor: k8s
    subprocess  always available (dev-only, no isolation)
    kubernetes  available (Docker present; k3d/kubectl fetched on demand)

  next: run `leoflow setup` to bootstrap the managed runtime.
```

### Confirming the installed version

Each binary reports its own build, so you can confirm what landed on `PATH`:

```console
$ leoflow --version          # root CLI (leoflow version also prints commit + build date)
$ leoflow-server --version   # control plane
$ leoflow-agent --version    # in-pod agent
$ leoflow-mcp --version      # MCP server (see the MCP guide)
```

### Installer options

| Variable | Effect |
|---|---|
| `DEXAFLOW_VERSION=v0.4.0-rc.2` | install a specific release (default: newest, including pre-releases). See [Releases](https://github.com/dexadata/leoflow/releases) for the current tag. |
| `DEXAFLOW_NO_SETUP=1` | install binaries only; run `leoflow setup` yourself later |
| `DEXAFLOW_INSTALL_DIR=~/.dexaflow/bin` | where to put the binaries |

### Building Lite from source

If you have a Go toolchain and prefer to build it yourself:

```bash
go install github.com/dexadata/dexaflow/cmd/dexaflow@latest
go install github.com/dexadata/dexaflow/cmd/dexaflow-server@latest
go install github.com/dexadata/dexaflow/cmd/dexaflow-agent@latest
# ensure $(go env GOPATH)/bin is on your PATH, then:
leoflow setup
```

The subsequent `leoflow setup` provisions the same managed runtime the
install-script path uses (managed CPython under `~/.dexaflow/`).

### Uninstalling Lite

Use the built-in command — it removes the install directory and (with
`--purge`) your workspace too:

```bash
leoflow uninstall              # removes ~/.dexaflow (binaries, managed Python, parser, config)
leoflow uninstall --purge      # also removes ~/leoflow (your DAGs!)
```

If the `leoflow` binary is gone or broken, fall back to the same paths by
hand:

```bash
rm -rf ~/.dexaflow              # what `leoflow uninstall` would have removed
rm -rf ~/leoflow               # what `--purge` adds (your workspace)
```

---

## Install Pro

Pro installs the **control plane into Kubernetes** via the Leoflow Helm
chart. Task pods are scheduled into the cluster by the same control plane —
no host-side process supervisor, no managed Python sidecar. DAGs ship as
**container images** built in CI ([CI/CD & deploy examples](/operate/cicd-deploy/)).

The chart is **cloud-portable** — the same commands install unchanged on
**EKS, GKE, AKS**, or any conformant Kubernetes cluster.

### Quickstart (one command, any cloud)

The chart is published as an **OCI artifact** next to the images, and it
**auto-generates its own agent-TLS certificate** by default. So there is **no
cert-manager to install and no TLS Secret to pre-create** — TLS on the agent
gRPC channel stays **mandatory**, the chart just mints a stable self-signed CA
+ server cert for you and reuses it across upgrades. Point the chart at your
external Postgres and Redis and go:

```bash
helm install leoflow oci://ghcr.io/dexadata/charts/leoflow --version <VERSION> \
  -n leoflow --create-namespace \
  --set database.url='postgres://USER:PASS@HOST:5432/leoflow?sslmode=verify-full' \
  --set redis.url='rediss://HOST:6380/0' \
  --set auth.jwtSecret="$(openssl rand -base64 64)" \
  --set secretKey="$(openssl rand -hex 32)" \
  --set bootstrap.password='change-me'
```

That's the whole install — **no cert-manager, no pre-created Secret**. The only
values you must supply are your two datastore URLs and the three credentials.
Using RDS or Cloud SQL? Their certificates are signed by a CA the system trust
store does not carry, so read [private CA](#if-your-postgres-uses-a-private-ca-rds-cloud-sql)
below before running this.
`--version` takes the chart version — the
[latest release](https://github.com/dexadata/leoflow/releases) tag with the
leading `v` stripped (per SemVer2).

#### If your Postgres uses a private CA (RDS, Cloud SQL)

`sslmode=verify-full` above verifies the server certificate against the
**system** trust store. That is right whenever the Postgres certificate chains
to a public CA — Azure Database for PostgreSQL Flexible Server does, for
instance, so the quickstart works there unchanged. It is wrong for offerings
that sign with a provider or per-instance CA that no system root store carries,
where you get a certificate-verification failure at connect time.

Publish the provider's CA bundle as a ConfigMap with the key `ca.crt`, point
`database.caConfigMap` at it, and add `sslrootcert` to the DSN:

```bash
kubectl create namespace leoflow
kubectl -n leoflow create configmap rds-ca --from-file=ca.crt=./global-bundle.pem

helm install leoflow oci://ghcr.io/dexadata/charts/leoflow --version <VERSION> \
  -n leoflow --create-namespace \
  --set database.caConfigMap=rds-ca \
  --set database.url='postgres://USER:PASS@HOST:5432/leoflow?sslmode=verify-full&sslrootcert=/etc/leoflow/db-ca/ca.crt' \
  ...
```

The chart mounts the bundle at that path in **both** the control plane and the
pre-install migration Job. Both read the same DSN, so both need the file —
before this was fixed the server started and the migration Job failed, which
surfaced as `Job Failed` from `helm install` with the real error only in the
Job pod's log.

{{% alert title="Cloud SQL needs verify-ca, not verify-full" color="warning" %}}
Cloud SQL's per-instance server certificate carries the CN
`<project>:<instance>`, not the address you connect to, so `verify-full`
fails hostname verification even with the CA mounted. Use `sslmode=verify-ca`,
or the Cloud SQL Auth Proxy, or connect via the instance DNS name
(`<instance>.<region>.<project>.cloudsql.goog`) with a customer-managed CA.
{{% /alert %}}

The two consumers do not use the same Postgres driver — the control plane uses
pgx, the migration Job links lib/pq. Both read `sslrootcert=<path>` identically,
which is why the recipe above is safe on both. `sslrootcert=system` is **pgx
only**: put it in this shared DSN and the control plane starts while the
migration Job fails.

{{% alert title="OCI chart is the primary path" color="info" %}}
The OCI chart above is **published** and is the recommended way to install
Pro — it carries the auto-generated agent TLS by default. [Installing from
source](#install-from-source) (below) is the alternative, for the bleeding
edge on `main` ahead of the next release.
{{% /alert %}}

### Install from source

Installing the chart straight from a checkout of **`main`** is the
**bleeding-edge** alternative to the published OCI chart — use it to pick up
changes that have merged to `main` but not yet been cut into a release. The
OCI-chart and auto-generated-TLS features are live in released charts too, so
this is only needed when you want `main`. Same required values, from the
`helm/leoflow` directory in the repo:

```bash
git clone --depth 1 https://github.com/dexadata/leoflow   # current main
cd leoflow

helm install lf ./helm/leoflow -n leoflow --create-namespace \
  --set image.tag=v0.4.0-rc.2 \
  --set migrations.image.tag=v0.4.0-rc.2 \
  --set database.url='postgres://USER:PASS@HOST:5432/leoflow?sslmode=verify-full' \
  --set redis.url='rediss://HOST:6380/0' \
  --set auth.jwtSecret="$(openssl rand -base64 64)" \
  --set secretKey="$(openssl rand -hex 32)" \
  --set bootstrap.password='change-me'
```

The chart auto-generates the agent TLS cert regardless of image version, so
this works on `main` today. Pin `--set image.tag` / `--set migrations.image.tag`
to a published release tag (`v0.4.0-rc.2` shown — see the
[releases](https://github.com/dexadata/leoflow/releases)); from a source
checkout the image tags are not baked in, so set them explicitly. Add
`--branch <TAG>` to the clone to install the chart at a specific tag instead of
`main`.

What this installs (one Deployment, one Service, RBAC for the pod-per-task
executor, a pre-install/upgrade migrations Job; optional Ingress, PDB, HPA,
ServiceMonitor, NetworkPolicy):

- **`leoflow-server`** Deployment listening on HTTP `8080`, metrics `9090`,
  and agent gRPC `9091`.
- A pre-install/pre-upgrade **Job** running `golang-migrate` against
  `database.url` before the server starts.
- A **ServiceAccount + Role/RoleBinding** letting the control plane create,
  watch, and delete **task pods** (and read their logs) in `taskNamespace`.
- A chart-managed **Secret** holding the inline DB / Redis / JWT / bootstrap
  credentials. Skipped when you bring your own via `*.existingSecret`.

Open the UI by port-forwarding the Service, or enable `ingress.enabled=true`
with a controller of your choice — see the chart's
[ingress values](/operate/helm-chart/) for the field shape. Log in as the bootstrap
admin (`admin@leoflow.local` / the password you set above) and rotate it.

### Prerequisites

Two external datastores and one cluster capability — that's all a default
install needs. **cert-manager is NOT required** (the chart auto-generates
agent TLS, above).

| Requirement | Why |
|---|---|
| A **Kubernetes cluster** (1.27+ recommended) | runs the control plane and task pods |
| `kubectl` + **Helm 3.8+** | apply the chart; Helm 3.8+ is required to `helm install` an OCI chart |
| An **external Postgres** (PostgreSQL **13+**) | Pro datastore — the chart refuses to install without `database.url` (the embedded datastore is Lite-only) |
| An **external Redis** (Redis **6.0+**) | XCom + advisory locks — the chart refuses to install without `redis.url` |
| A **default StorageClass** | the control-plane logs PVC (`logs.persistence.enabled: true`, on by default) binds to it |

Managed services are first-class — RDS / Cloud SQL / Azure Database for
Postgres on the SQL side; ElastiCache / Memorystore / Azure Cache for Redis.
See the chart's [Datastore compatibility](https://github.com/dexadata/leoflow/blob/main/helm/leoflow/README.md#datastore-compatibility)
table for tested versions; managed providers that present a per-instance or
provider-specific CA expose a `caConfigMap` knob (Postgres and Redis sides
respectively) for verified TLS.

{{% alert title="A fresh cluster with no default StorageClass (e.g. EKS Auto Mode)" color="warning" %}}
The logs PVC binds to the cluster's **default StorageClass**. On a truly
fresh cluster that has **none** — notably **EKS Auto Mode**, which ships
without a default — the PVC hangs `Pending` and the control-plane pod never
starts. Two fixes:

- **Name a StorageClass:** `--set logs.persistence.storageClass=gp2` on EKS
  (use e.g. `standard-rwo` on GKE, `managed-csi` on AKS), or mark one
  StorageClass as the cluster default.
- **Ephemeral trial:** `--set logs.persistence.enabled=false` uses an
  `emptyDir` instead — **logs are lost on every pod restart**; dev only.
{{% /alert %}}

{{% alert title="No managed datastore yet? There's a PoC path." color="info" %}}
For a one-cluster evaluation (kind, minikube, k3d, scratch namespace), the
chart deliberately won't fall back to embedded datastores — that's Lite's
job. The supported PoC path is to install plain Postgres + Redis
manifests alongside the chart, then point Leoflow at the in-cluster
Services. Recipe: [`helm/leoflow/examples/README.md`](https://github.com/dexadata/leoflow/tree/main/helm/leoflow/examples/README.md).
**Not for production.**
{{% /alert %}}

### Bring-your-own Secrets

Inline `--set` values bake credentials into the chart-managed Secret.
Production deploys typically pre-create Secrets (sealed-secrets,
External Secrets, etc.) and point the chart at them:

```bash
--set database.existingSecret=my-db     # key: databaseUrl
--set redis.existingSecret=my-redis     # key: redisUrl
--set auth.existingSecret=my-jwt        # key: jwtSecret
--set secretKeyExistingSecret=my-key    # key: secretKey
--set bootstrap.existingSecret=my-boot  # key: bootstrapPassword
```

When **every** credential comes from an existing Secret, the chart creates
no Secret of its own. The `checksum/secret` annotation on the pod template
only sees the chart-managed Secret, so rotation of an
`existingSecret` requires a manual `kubectl rollout restart deploy/lf-leoflow`.

### Production hardening

The quickstart is production-*shaped* but not production-*hardened*. For a real
deploy, layer on:

- **cert-manager / BYO TLS.** The default auto-generated cert is a stable
  self-signed CA — fine for the in-cluster agent channel, but many orgs want
  cert-manager-issued or externally-rooted certs with automatic rotation. Set
  `agentTLS.serverCertSecret` + `agentTLS.caConfigMap` and the chart uses them
  verbatim (skipping auto-gen). Full recipe: [Pro TLS with cert-manager](/operate/pro-tls/).
- **External Postgres / Redis** on managed services (see [Prerequisites](#prerequisites)),
  with **verified** TLS via the `database.caConfigMap` / `redis.caConfigMap` knobs.
- **StorageClass sizing.** The logs PVC defaults to `50Gi` (~1 GB/day per
  ~1000 active task runs). For multi-replica HA use a **ReadWriteMany**
  StorageClass (`--set logs.persistence.accessMode=ReadWriteMany` with NFS /
  Longhorn-rwx / CephFS / EFS / Azure Files / GCP Filestore) or ship logs to an
  object store (`logs.sink`); a `ReadWriteOnce` PVC pins you to a single
  replica.
- **NetworkPolicy — two independent values, and the task-pod one is the
  containment.** `networkPolicy.enabled=true` restricts the **control plane**,
  and only its **ingress**: `networkPolicy.egress` is **empty by default, and the
  chart then renders a single empty egress *rule* (`- {}`), which matches every
  destination** — deliberately, so that enabling the policy does not silently
  break Postgres / Redis / kube-apiserver access. (The distinction matters if you
  hand-write policies: an empty egress *list* under `policyTypes: [Egress]` is
  deny-all; an empty *rule* is allow-all.) Egress stays wide open until you
  populate `networkPolicy.egress` with your own data-store and apiserver rules
  (DNS is always allowed regardless). Its ingress, meanwhile, is port-scoped but
  not source-scoped until you set `networkPolicy.ingressFrom`: with the default
  empty list the policy renders ports 8080/9091 with no `from`, so any pod in any
  namespace may reach them. It renders nothing for task pods.
- **`taskNetworkPolicy.enabled` is the task-pod policy, and it defaults to
  `false`.** This is the one that governs the pods running untrusted,
  author-supplied DAG code: it denies all ingress and allows egress only to DNS,
  the control-plane gRPC, and every other destination *except* the cloud-metadata
  range `169.254.0.0/16`. It is the network-layer containment
  [ADR 0048](/project/adrs/0048-no-user-code-in-control-plane/) leans on when it
  argues that untrusted code is contained outside the control plane, so a
  production deploy should set it explicitly:
  `--set taskNetworkPolicy.enabled=true`. Two knobs go with it:
  `taskNetworkPolicy.blockPrivateNetworks=true` *additionally* denies RFC1918 and
  the apiserver (opt-in, because a DAG calling an internal service is a
  legitimate orchestration pattern the policy cannot tell apart from the
  apiserver by IP), and `taskNetworkPolicy.allowMetadataEgress` re-permits single
  hosts inside the blocked metadata range — one `/32` each, never the whole
  range, and the chart **fails the render** on anything wider (IPv6: `/128`),
  because an additive allow rule naming the range would override the `except`
  and delete the block rather than widen the exception. That last one is why the
  policy is opt-in rather than on by default:
  both clouds serve **keyless workload identity from the link-local range**, so
  turning the policy on breaks keyless external-secrets auth on GKE Workload
  Identity (`169.254.169.254/32`) and EKS Pod Identity (`169.254.170.23/32`)
  until the exception is added. AWS IRSA needs no exception — it uses the public
  STS endpoint. The install NOTES warn while the value is off.
- **Both policies need a CNI that *enforces* NetworkPolicy.** A rendered policy
  object is not proof of enforcement: kindnet (the default on `kind`) enforces
  nothing; the AWS VPC CNI enforces policy only when its network-policy agent is
  enabled — off by default on many EKS clusters; and on GKE, Dataplane V2
  (Cilium) enforces natively, while a non-DPv2 Standard cluster needs the
  network-policy addon or nothing enforces. Verify on your own CNI before
  treating either policy as a control.
- **Secret-delivery posture.** Three `auth.*` flips decide how much credential a
  task pod can reach, and all three ship on the value that is byte-for-byte
  today's behavior — so a default install is the *permissive* end of each:
  `auth.secretScoping` (`permissive` — every task receives the **whole tenant
  vault**), `auth.secretLivenessMode` (`observe` — a task instance that is no
  longer live still resolves secrets) and `auth.agentTokenTransport` (`envvar` —
  the bearer token sits in plaintext on the `Pod` object). What each one narrows,
  and which combinations are required together, is tabled in
  [Agent credential transport → Relationship to secret scoping](/operate/agent-credential-transport/#relationship-to-secret-scoping).

{{% alert title="`secretScoping: enforce` denies secrets to a DAG that declares none" color="warning" %}}
Under `enforce` a task receives **only** the Variables and Connections its
`dexaflow.yaml` declares — and a DAG that declares **nothing** therefore receives
**nothing**, not everything. The declaration schema is new, so most existing DAGs
declare nothing and would break on the flip. Land it the way any breaking
security change lands: leave `permissive` on while your DAGs adopt
[declarations](/author-dags/variables-connections/#declare-what-a-task-consumes),
then flip. Note what the observation period can and cannot tell you — the
scope-warning trail covers only DAGs whose declarations still resolve, so it
sees neither a DAG that declares nothing nor one whose declared names have since
been deleted from the vault
([#800](https://github.com/dexadata/leoflow/issues/800)).
{{% /alert %}}

{{% alert title="The agent channel is server TLS, not mutual mTLS" color="info" %}}
The agent↔control-plane gRPC channel is **one-way (server) TLS**: the agent
verifies the control-plane's **server** certificate, but the agent does
**not** present a client certificate. The agent's *identity* is a **bearer
JWT**, not an mTLS client cert. So "provision the cert" only ever concerns
the server side — there are no per-agent client certs to mint or rotate.
{{% /alert %}}

{{% alert title="GitOps (ArgoCD / Flux): don't rely on auto-gen" color="warning" %}}
Auto-generation uses Helm's `lookup` to find and reuse the existing cert
Secret across upgrades. Cluster-less rendering — ArgoCD/Flux diffing or
templating **without** live cluster access — can't `lookup`, so it either
regenerates the CA on every sync (breaking every already-running agent's
trust) or renders an empty cert. Under GitOps, use **BYO / cert-manager**
(`agentTLS.serverCertSecret` + `agentTLS.caConfigMap`) rather than auto-gen.
See [Pro TLS](/operate/pro-tls/).
{{% /alert %}}

### Upgrades

`helm upgrade` runs the migrations Job, rolls the Deployment, and respects
PDB/replica settings. The full upgrade contract — version skew, downtime
expectations, rollback — lives in [Upgrades](/operate/upgrades/).

### Verifying the chart and images

Both the **chart** and the **images** (`leoflow-server`, `leoflow-migrate`,
plus `leoflow` and `leoflow-agent` binaries) are published by
`.github/workflows/release.yaml` and **cosign-signed** (keyless):

```bash
# Verify the server image at a release tag.
cosign verify ghcr.io/dexadata/leoflow-server:v0.4.8 \
  --certificate-identity-regexp 'https://github.com/(dexadata|neochaotic)/leoflow' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

### Full values reference

The chart [README on this site](/operate/helm-chart/) is auto-generated from
`values.yaml` by `helm-docs` and documents every knob — TLS, observability,
networking, autoscaling, secret wiring. Treat it as the source of truth.

### Uninstalling Pro

```bash
helm uninstall lf -n leoflow
kubectl delete namespace leoflow
```

This removes the chart-managed resources. PVCs (e.g. for control-plane logs
when `logs.persistence.enabled=true`) and any **external** Postgres / Redis
data outlive the chart — drop them out of band when you're done.

---

## Next

- [Quickstart](/get-started/quickstart/) — run your first DAG.
- [The `leoflow lite` workflow](/contribute/local-dev-loop/) — the hot-reload inner loop
  (Lite).
- [Helm chart](/operate/helm-chart/) — full Pro values reference.
- [CI/CD & deploy examples](/operate/cicd-deploy/) — packaging DAGs as images for Pro.
- [Editions](/concepts/editions/) — Lite vs Pro, feature by feature.
