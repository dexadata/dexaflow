---
title: Validate the native resolver on a real cluster (before enabling)
weight: 59
description: A turnkey runbook to prove the keyless external-secrets resolver end-to-end on EKS or GKE — the one thing an emulated store cannot validate — before turning the backend on in production.
---

The native external-secrets resolver ([external secrets, section 4]({{< relref "external-secrets" >}})) ships **off by default**. Its whole surface — declaration scoping, the pod-side resolve chain, fail-closed behaviour, and the NetworkPolicy metadata-egress guard — is exercised in CI against an emulated AWS Secrets Manager (LocalStack). The **one** thing an emulator cannot prove is the **keyless identity handshake**: IRSA, EKS Pod Identity, and GKE Workload Identity all depend on the cloud's own OIDC provider and metadata path, injected at pod admission.

This runbook is that missing gate. Run it on a real EKS or GKE cluster and complete the [sign-off checklist](#sign-off-checklist) before you set `secrets.backend` in a production values file. It changes nothing permanent — it deploys one canary DAG and one throwaway secret, then tears both down.

## Before you start

- A cluster with the keyless mechanism you intend to use already wired at the **cloud** side (the IAM role / service account, the trust policy, and the identity webhook or agent). This runbook validates that Leoflow drives it correctly; it does not set up the cloud IAM.
- `kubectl` context on that cluster and permission to install/upgrade the Leoflow Helm release in a throwaway namespace.
- Access to the provider secret store to create and delete one test secret.
- The provider's Airflow backend package available in your task image (e.g. `apache-airflow-providers-amazon` for AWS, `-google` for GCP). A `dexaflow.yaml` `dependencies:` entry bakes it in.

Do this in a **non-production namespace** first.

## 1. Seed one throwaway secret

Create a secret the canary will resolve, under the prefix you will configure. Keep the value recognisable so the assertion is unambiguous, and note it — you will delete it at the end.

- **AWS Secrets Manager:** `aws secretsmanager create-secret --name airflow/variables/canary_region --secret-string "eu-west-canary-42"`
- **GCP Secret Manager:** `gcloud secrets create airflow-variables-canary_region --data-file=- <<< "eu-west-canary-42"` (map the separator to your `backendKwargs`).

## 2. Configure the backend + keyless identity (+ egress for GKE / Pod Identity)

Install/upgrade the release in the test namespace with the backend pointed at your store, the task ServiceAccount bound to the reader identity, and — only for the mechanisms that reach the metadata range — the scoped egress exception.

```yaml
# values-canary.yaml
secrets:
  backend: "airflow.providers.amazon.aws.secrets.secrets_manager.SecretsManagerBackend"
  backendKwargs: '{"connections_prefix":"airflow/connections","variables_prefix":"airflow/variables","region_name":"eu-west-1"}'

taskServiceAccount:
  create: true
  annotations:
    # AWS IRSA / Pod Identity:
    eks.amazonaws.com/role-arn: arn:aws:iam::<acct>:role/<leoflow-secrets-reader>
    # GKE Workload Identity instead:
    # iam.gke.io/gcp-service-account: <leoflow-secrets-reader>@<project>.iam.gserviceaccount.com

taskNetworkPolicy:
  enabled: true
  # ONLY for EKS Pod Identity or GKE WI — IRSA needs no exception (public STS).
  allowMetadataEgress: []
    # - 169.254.170.23/32   # EKS Pod Identity agent
    # - 169.254.169.254/32  # GKE metadata server
```

Match the `allowMetadataEgress` line to your mechanism:

| Mechanism | Reaches | `allowMetadataEgress` needed |
|---|---|---|
| AWS **IRSA** | public STS | no |
| AWS **Pod Identity** | `169.254.170.23` | `169.254.170.23/32` |
| **GKE Workload Identity** | `169.254.169.254` | `169.254.169.254/32` |

The `/32` is not a suggestion: the chart **refuses to render** an entry wider
than one host (IPv4 `/32`, IPv6 `/128`). A `169.254.0.0/16` would not widen the
exception, it would delete the metadata block — egress rules are additive, so a
rule allowing the range overrides the `except` in the allow-all rule.

## 3. Deploy the canary DAG

A one-task DAG that **declares** the seeded name and asserts the value it receives came from the store, not the vault (the vault has no such name):

```python
# canary_secrets/dag.py
import os
from airflow.sdk import DAG, task


@task
def check() -> None:
    got = os.environ.get("AIRFLOW_VAR_CANARY_REGION", "<MISSING>")
    print(f"CANARY_RESOLVED={got}")
    assert got == "eu-west-canary-42", f"resolver did not deliver the store value: {got!r}"


with DAG("canary_secrets", schedule=None, catchup=False):
    check()
```

```yaml
# canary_secrets/dexaflow.yaml
dag_id: canary_secrets
dependencies: [apache-airflow-providers-amazon]   # or -google for GKE
variables:
  - canary_region
```

Compile, push, and trigger it (`leoflow compile … && leoflow push … && curl -X POST …/dagRuns`).

## 4. Assert — the four things only a real cluster proves

1. **Keyless resolve works.** The `check` task reaches `success`, and its logs contain `CANARY_RESOLVED=eu-west-canary-42`. This proves the pod authenticated as its ServiceAccount, the cloud injected the token, and the resolver read the store — the whole keyless path.
2. **Egress guard holds.** For Pod Identity / GKE WI, confirm the task pod's NetworkPolicy allows **only** the `/32` you listed: a probe to any other `169.254.0.0/16` address from the pod must be refused. For IRSA, confirm no metadata exception was added.
3. **Fail-closed on missing permission.** Remove the store read permission from the reader role (or point at a name the role cannot read) and re-run. The task must **fail** with a sanitized reason — never the secret value, never a fallback to a wrong value. A clean miss (a name absent from the store) must instead fall through to the vault and, if also absent there, simply not export the name.
4. **Declaration is the scope authority.** A task that does **not** declare `canary_region` must **not** receive it, even with the backend configured.

## 5. Tear down

- Delete the canary DAG (`leoflow dags delete canary_secrets --deregister`), and
  remove its source so the next deploy does not re-register it.
- Delete the throwaway secret from the store.
- Restore any permission you removed for assertion 3.
- Uninstall the canary release / namespace.

## Sign-off checklist

Enable `secrets.backend` in production only when all of these passed on the target cluster:

- [ ] Canary task succeeded and logged the exact store value (assertion 1).
- [ ] Metadata egress is scoped to the single `/32` required, or none for IRSA (assertion 2).
- [ ] A permission-denied read failed the task closed with no secret in the error (assertion 3).
- [ ] An undeclared name was not delivered (assertion 4).
- [ ] The provider backend package is baked into the production task image.
- [ ] Both EKS and GKE were validated **separately** if you run both — the keyless mechanisms differ and do not transfer.

## See also

- [External secrets (keyless, ESO, mounted)]({{< relref "external-secrets" >}}) — the full option matrix and the resolver's guarantees.
- [Agent credential transport]({{< relref "agent-credential-transport" >}}) — how the vault path delivers secrets, for contrast.
