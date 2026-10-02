---
title: External secrets (keyless, ESO, and mounted secrets)
linkTitle: External secrets
weight: 58
description: Reach credentials that live in your cloud secret store or Vault from task pods — keyless first, then External Secrets Operator or a mounted Kubernetes Secret — without duplicating them in Dexaflow.
---

Dexaflow is **not a key manager** ([ADR 0035](/project/adrs/0035-cloud-connector-auth-keyless-first/)). A
Connection or Variable does not have to live in Dexaflow's vault: if your secret
already exists in **AWS Secrets Manager, GCP Secret Manager, Azure Key Vault, or
HashiCorp Vault** — provisioned by Terraform, synced by the External Secrets
Operator, etc. — a task can reach it **without a copy in Dexaflow**. That keeps a
single source of truth and stays fully declarative/IaC.

There are three ways for a task to reach an external secret, **in order of
preference**. Prefer the earliest one your environment allows.

{{% alert title="Security: less secret material is safer" color="warning" %}}
Every copy of a credential is a place it can leak. Keyless (option 1) keeps
**zero** secret material anywhere — no value in Dexaflow, no Kubernetes Secret, no
env var. Reach for options 2–3 only when a credential (not a cloud identity) is
genuinely required.
{{% /alert %}}

## 1. Keyless — Workload Identity (preferred)

The task pod runs as a Kubernetes ServiceAccount bound to a cloud identity, and
the cloud SDK inside the task uses **that identity** — no key, no token, nothing
to rotate or leak. This is the recommended path for any cloud connection.

| Cloud | Mechanism |
|---|---|
| AWS | **IRSA** or **EKS Pod Identity** — the pod's SA assumes an IAM role |
| GCP | **Workload Identity** — the KSA impersonates a Google service account |
| Azure | **Azure Workload Identity** — the KSA federates to a managed identity |
| HashiCorp Vault | **Kubernetes auth** — Vault trusts the pod's SA token |

AWS, GCP, and Azure each have a native, keyless-first Dexaflow Connection type
(`aws`, `google_cloud_platform`, `wasb`/`adls`/…) that resolves this identity
automatically — see [ADR 0035](/project/adrs/0035-cloud-connector-auth-keyless-first/).
Vault has no native Dexaflow Connection type: a task reaches it by using Vault's
own client library with the pod's ambient ServiceAccount token, independent of
Dexaflow's Connection model.

The Connection then declares **no key at all** — e.g. a `google_cloud_platform`
connection with neither `key_path` nor `keyfile_dict` resolves via Application
Default Credentials (the pod identity). Set up the identity binding via the
chart's task ServiceAccount, then point a task at it:

```yaml
# values.yaml
taskServiceAccount:
  create: true
  name: leoflow-task
  annotations:
    # GKE Workload Identity:
    iam.gke.io/gcp-service-account: "<GSA>@<project>.iam.gserviceaccount.com"
    # EKS IRSA (use this OR the GKE annotation, not both):
    # eks.amazonaws.com/role-arn: "arn:aws:iam::<account>:role/<role>"
```

```yaml
# dexaflow.yaml — reference that ServiceAccount from a task
tasks:
  my_task_id:
    execution:
      service_account: leoflow-task
```

Dexaflow passes the pod identity through untouched; it never sees a token or key.

## 2. External Secrets Operator (ESO) → a mounted Kubernetes Secret

When a **credential file** is genuinely required (a service-account JSON, a
client certificate, a private CA) and keyless is not available, keep the secret
in your external store and let **[ESO](https://external-secrets.io/)** sync it
into a Kubernetes Secret. Dexaflow mounts that Secret read-only into task pods;
the Connection references the file by path. **The secret value never enters
Dexaflow** — Dexaflow only mounts a Secret you (or ESO) created.

This is available **today**, provider-neutral (ESO supports AWS/GCP/Azure/Vault
and more), and needs no Dexaflow code.

1. **ESO syncs the external secret into a Kubernetes Secret** (illustrative — AWS
   Secrets Manager; the same shape works for any ESO provider):

   ```yaml
   apiVersion: external-secrets.io/v1beta1
   kind: ExternalSecret
   metadata:
     name: gcp-sa-key
     namespace: leoflow           # the chart's taskNamespace (default: leoflow)
   spec:
     refreshInterval: 1h
     secretStoreRef:
       name: aws-secrets-manager  # your ClusterSecretStore/SecretStore
       kind: ClusterSecretStore
     target:
       name: gcp-sa-key           # the Kubernetes Secret ESO creates
     data:
       - secretKey: key.json      # the file name inside the Secret
         remoteRef:
           key: prod/gcp/etl-sa-key  # the secret's id in the external store
   ```

2. **Mount that Kubernetes Secret into task pods** via the chart's `taskSecret`
   (the chart only mounts it — it never reads or copies the value):

   ```yaml
   # values.yaml
   taskSecret:
     name: gcp-sa-key                   # the Secret from step 1
     mountPath: /etc/leoflow/secrets    # read-only mount in every task pod
   ```

3. **Reference the mounted file from the Connection** with `key_path`
   ([ADR 0035](/project/adrs/0035-cloud-connector-auth-keyless-first/)) — the key
   is read from disk at task time, never stored in Dexaflow. Create or edit the
   Connection via **Admin → Connections** or the API, setting `key_path` in
   Extra:

   ```bash
   curl -X POST "$DEXAFLOW_SERVER/api/v2/connections" -H "Authorization: Bearer $TOKEN" \
     -H 'Content-Type: application/json' \
     -d '{"connection_id":"warehouse","conn_type":"google_cloud_platform","extra":"{\"key_path\":\"/etc/leoflow/secrets/key.json\"}"}'
   ```

   Then declare it so a DAG's tasks can consume it:

   ```yaml
   # dexaflow.yaml
   connections:
     - warehouse
   ```

See the per-connection pages (e.g.
[Google Cloud Platform](/connections/google_cloud_platform/)) for the exact
`extra` fields each connection type accepts.

{{% alert title="CSI Secret Store driver" color="info" %}}
The [Secrets Store CSI Driver](https://secrets-store-csi-driver.sigs.k8s.io/)
(with the AWS/GCP/Azure/Vault providers) is an equivalent mount-based
alternative: it mounts the external secret straight into the pod as a file,
which a Connection then references by `key_path` exactly as above. Use whichever
your platform already runs.
{{% /alert %}}

## 3. A hand-created mounted Kubernetes Secret

Without ESO or CSI, create the Kubernetes Secret yourself
(`kubectl create secret generic gcp-sa-key --from-file=key.json=...`) and mount
it with the same `taskSecret` config as step 2. Identical from Dexaflow's side —
you just own the sync instead of ESO.

## 4. Native external secrets resolver (Dexaflow-managed, ADR 0060)

Options 1–3 let a task *reach* a secret its own code reads. The **native resolver**
goes further: a DAG **declares a Connection/Variable by name** exactly as it would
for a vault secret, and Dexaflow resolves it **pod-side** from your provider store —
no copy in Dexaflow's vault, no author-visible provider path. It covers operator,
`@task`/python, and **bash** tasks uniformly (the value is exported as
`AIRFLOW_CONN_*` / `AIRFLOW_VAR_*`), which a raw in-pod Airflow backend does not.

**How it resolves.** For each name the task declared, the chain is
**external backend → Dexaflow vault → env**: an external hit wins, a miss falls back
to the vault. Resolution runs in the task pod under the pod's **own keyless
identity** — the control plane never reaches your secret store (ADR 0048). It is
**off by default**; with no backend configured the vault is the only source.

**Configure (operator, Helm).** Set the provider backend class and its kwargs; a
kind is served iff its `*_prefix` kwarg is present:

```yaml
# values.yaml
secrets:
  backend: "airflow.providers.amazon.aws.secrets.secrets_manager.SecretsManagerBackend"
  backendKwargs: '{"connections_prefix":"airflow/connections","variables_prefix":"airflow/variables","region_name":"us-east-1"}'

# Keyless: the resolver authenticates as the task pod's ServiceAccount.
taskServiceAccount:
  create: true
  annotations:
    eks.amazonaws.com/role-arn: arn:aws:iam::<acct>:role/<leoflow-secrets-reader>
```

```python
# dag.py — declare the name; no provider path, no key in Dexaflow.
with DAG("etl", ...):
    ...  # a task that declares connections=["warehouse"] / variables=["region"]
```

Dexaflow resolves `warehouse` from `<connections_prefix>/warehouse` in the store,
renders it as an Airflow connection URI, and exports `AIRFLOW_CONN_WAREHOUSE`.

{{% alert title="Required: the provider package in the task image" color="warning" %}}
The pod-side resolver drives the provider's Airflow secrets backend, so that
provider package must be present in the **task image** — the base image does not
bundle it. Declare it as a DAG dependency (baked at build time), e.g. for AWS:

```yaml
# dexaflow.yaml
dependencies: [apache-airflow-providers-amazon]   # -google / -microsoft-azure / -hashicorp for other providers
```

Without it the resolver fails to import the backend class and the task fails
closed. See the [cluster-validation runbook]({{< relref "external-secrets-cluster-validation" >}}).
{{% /alert %}}

**Provider-neutral.** AWS is the reference; GCP Secret Manager, Azure Key Vault,
and HashiCorp Vault use the same `secrets.backend` + `backendKwargs` with that
provider's backend class and keyless mechanism.

**Guarantees.**
- **Operator-only.** The backend config is delivered as `DEXAFLOW_SECRETS_*` pod
  env, which an author's task `env:` can never set (reserved keys are stripped).
- **Declaration is the scope authority.** Only names the task **declared** are
  resolved — the same rule as the vault.
- **Fail-closed.** A hard resolver error (access denied, throttle, malformed) fails
  the task with a sanitized reason — never the secret. A clean miss falls through to
  the vault; if it is also absent, the name is simply not exported and the task runs
  (as it does today for an undeclared vault secret).
- **No copy at rest.** The value lives only in the task process env for that
  attempt, exactly like a vault secret — never on the pod object or in etcd.

> **Keyless end-to-end (IRSA / Workload Identity) is verified on a real cluster.**
> Dexaflow only sets the pod's ServiceAccount; the cloud identity webhook injects
> the token at admission. Confirm the KSA→role binding on your cluster.

**NetworkPolicy — metadata egress (important for GKE and EKS Pod Identity).** The
task-pod NetworkPolicy always **blocks the cloud metadata range `169.254.0.0/16`**
(an anti-SSRF measure — the classic credential-theft target). Keyless mechanisms
that reach an endpoint in that range therefore need an explicit egress exception:

| Keyless mechanism | Endpoint | Default task NetworkPolicy |
|---|---|---|
| AWS **IRSA** | STS (public) | works as-is |
| AWS **Pod Identity** | `169.254.170.23` | **blocked** — needs an exception |
| **GKE Workload Identity** | metadata server `169.254.169.254` | **blocked** — needs an exception |

To use GKE WI or EKS Pod Identity, re-allow **just that host** with
`taskNetworkPolicy.allowMetadataEgress` — a scoped `/32` exception (the rest of
`169.254.0.0/16` stays blocked), not a hole in the SSRF guard:

```yaml
# values.yaml
taskNetworkPolicy:
  enabled: true
  allowMetadataEgress:
    - 169.254.169.254/32   # GKE metadata server
    # - 169.254.170.23/32  # EKS Pod Identity
```

AWS IRSA needs no NetworkPolicy change (it authenticates against the public STS
endpoint, not the metadata range).

{{% alert title="One host per entry, enforced at render time" color="warning" %}}
`allowMetadataEgress` accepts an IPv4 `/32` or an IPv6 `/128` and nothing wider.
`169.254.0.0/16`, `169.254.169.0/24`, a bare `169.254.169.254` with no prefix, a
malformed CIDR, or the value sent as a scalar instead of a list all **fail the
render** with a message naming the entry and what to pass instead.

The reason is that a wider entry does not widen the exception, it deletes the
block. NetworkPolicy egress rules are additive — traffic is allowed if it
matches *at least one* rule — so a second rule permitting `169.254.0.0/16`
overrides the `except: [169.254.0.0/16]` in the allow-all rule, and the rendered
manifest still shows an except-list that no longer blocks anything. To reach a
wider range that is **not** the metadata range, use
`taskNetworkPolicy.extraEgress`.
{{% /alert %}}

## What this does and does not cover

- **File-based credentials** (SA keys, certs, CA bundles) are the natural fit for
  options 2–3 via `key_path`.
- **Cloud API access** (BigQuery, S3, a warehouse reached by role) is best served
  by **option 1 (keyless)** — no secret at all.
- Some connection types already have a narrower, provider-specific direct-fetch
  field — e.g. `google_cloud_platform`'s `key_secret_name` fetches a key straight
  from **GCP Secret Manager** via ADC at task time (still needs a keyless
  identity to *read* the secret). That's a per-connection escape hatch, not the
  general mechanism this page covers.
- **Resolving a declared Connection/Variable directly from the external store**
  — so a secret in AWS Secrets Manager becomes a Dexaflow Connection/Variable with
  no Kubernetes Secret in between — is **option 4 above** (the native resolver,
  ADR 0060, [#811](https://github.com/dexadata/dexaflow/issues/811)). It ships
  **off by default**; enable it with `secrets.backend` after validating keyless
  end-to-end on your cluster (see
  [Validate the native resolver on a real cluster]({{< relref "external-secrets-cluster-validation" >}})).

## Scoping — which pod sees which secret

How a credential is isolated to the right task depends on the path it takes:

- **Mounted Kubernetes Secret (options 2–3) is cluster-wide, not per-task.** The
  `taskSecret` mount is applied to **every** task pod, so any task can read the
  files under `mountPath`. Isolate it *outside* Dexaflow: put only broadly-shared
  material in that Secret, separate sensitive workloads by namespace/cluster, and
  restrict who can read the Secret with RBAC. Better still, use **keyless
  (option 1)** — there is no mounted material to over-share.
- **Keyless (option 1) is scoped by the pod's own identity.** A task reaches a
  cloud API as the ServiceAccount identity you bound to its pod; another task with
  a different ServiceAccount cannot assume it. No secret is delivered at all.
- **Dexaflow-vault Connections/Variables — always attempt-scoped; per-task only under
  `enforce`.** Delivery is always against a short-lived identity bound to that
  specific task attempt, over TLS. Whether a pod receives *only the names it
  declared* is an operator policy: under `secret_scoping: enforce` (with
  `secret_liveness_mode: enforce`) a task gets only its declared names and only
  while the attempt is live; under the **default** (`permissive` / `observe`) a
  task receives its whole tenant's vault and delivery is not gated on liveness. See
  [ADR 0055](/project/adrs/0055-secret-scoping-and-token-liveness/). The **native
  resolver (section 4, ADR 0060)** extends the *declaration* model to external
  secrets — a task resolves only the names it declared — so the cluster-wide mount
  (options 2–3) is no longer the only way to reach an external store.

## Security notes

- The `taskSecret` mount is **read-only** and applies to **every** task pod;
  scope the Kubernetes Secret's contents accordingly (see Scoping above).
- **Prefer keyless.** A mounted key is a credential at rest in the cluster;
  Workload Identity is not.
- **Rotation** is your external store's job — ESO re-syncs on its
  `refreshInterval`, and each task runs in a fresh pod that re-reads the mount, so
  there is no long-lived cached copy to invalidate.
- Dexaflow never logs or persists a mounted secret's value; it only sets the mount
  path on the pod.

## See also

- [Validate the native resolver on a real cluster]({{< relref "external-secrets-cluster-validation" >}}) — the EKS/GKE keyless gate before enabling option 4.
- [ADR 0035 — Cloud connector auth: keyless-first](/project/adrs/0035-cloud-connector-auth-keyless-first/)
- [Variables & Connections](/author-dags/variables-connections/) — how secrets reach a task.
- [Connections reference](/connections/) — per-type `extra` fields (`key_path`, …).
- [Helm chart](/operate/helm-chart/) — the `taskSecret` values.
