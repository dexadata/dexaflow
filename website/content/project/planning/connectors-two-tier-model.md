---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /planning/connectors-two-tier-model.html
# --- end AUTO redirect aliases ---
title: Connectors — two-tier model (ADR 0035 + shim)
linkTitle: Two-tier connectors
weight: 20
description: "Planning note: the two-tier connector model."
---

> **Companion to** [`airflow-connector-compatibility.md`](/project/planning/airflow-connector-compatibility/).
> Reviews the `google_cloud_platform` connector already shipped (ADR 0035 + `examples/gcp_gcs_load/`) against Strategy A of the compatibility study, and answers: **do we need two classes of connectors going forward (a "Dexaflow-native" tier and an "Airflow-compat" tier), or can one model serve both?**
>
> **Verdict: a two-tier model is real, but it's a policy split, not a code-split.** One shim is enough; the security policy from ADR 0035 lives in a pre-processor that runs *before* the connection is handed to any Airflow-style hook.

## 1. What we ship today on GCP (recap)

### 1.1 ADR 0035 — the position

`docs/adr/0035-cloud-connector-auth-keyless-first.md` declares, accepted, on 2026-06-02:

> **Dexaflow is not a secrets/key manager.** It orchestrates; it does not aspire to own credential material.

Concretely for cloud connectors:

- **Default:** keyless. Runtime identity (Workload Identity on GKE, ADC on Lite/subprocess).
- **Else:** a *reference* to a platform-managed secret — `key_path` (mounted K8s Secret) or `key_secret_name` (GCP Secret Manager). **The key never enters Dexaflow's DB.**
- **`keyfile_dict`** (the cloud key stored inline in the Connection) is **accepted for Airflow compatibility but explicitly discouraged**. It's the cloud-key analog of how `postgres_conn` stores a user/password encrypted at rest (ADR 0019): pragmatic, Airflow-compatible, but not the desirable posture.
- **Resolution order in the task:** `keyfile_dict` → `key_path` → `key_secret_name` → ADC.
- **Field names** are short (`keyfile_dict`, `key_path`, `key_secret_name`, `project`, `scopes`); legacy `extra__google_cloud_platform__<name>` accepted as migration fallback only.
- **No cloud SDK in the Go control plane** — validation is *structural* (`internal/api/connection_probe.go:64`); the token exchange happens in the task.
- **Generalizes to future AWS/Azure** — same stance: keyless first, secret-reference next, key-in-DB only as a discouraged fallback.

### 1.2 The implementation shape — `examples/gcp_gcs_load/dag.py`

Today the GCP connector resolves credentials inside the **DAG itself**, via an inline helper:

```python
# examples/gcp_gcs_load/dag.py — actual shape
def gcp_credentials(conn_id: str = GCP_CONN):
    extra = _conn_extra(conn_id)  # parses __extra__ from AIRFLOW_CONN_<ID>
    # short name preferred; extra__google_cloud_platform__<name> accepted as fallback.
    if keyfile_dict := _field(extra, "keyfile_dict"):
        return service_account.Credentials.from_service_account_info(...)
    if key_path := _field(extra, "key_path"):
        return service_account.Credentials.from_service_account_file(key_path, ...)
    if key_secret_name := _field(extra, "key_secret_name"):
        # Fetch from GCP Secret Manager using ADC — bootstrap still needs an ambient identity.
        ...
    return google.auth.default(...)   # keyless / Workload Identity

@task
def gcs_roundtrip():
    creds, project, mode = gcp_credentials()
    client = storage.Client(project=project, credentials=creds)
    # ... real GCS work ...
```

**Key observations:**

1. **The user's DAG owns the resolver.** There is no `LeoflowGCPHook` class today; the resolver is a helper function the user copies into their DAG (or imports from a snippet library we publish).
2. **`AIRFLOW_CONN_<ID>` is the on-the-wire format.** The Go agent already emits it (`internal/agent/runner.go:170`); the helper parses it via `urllib.parse`. This is *the same wire format* Strategy A's shim would feed to upstream provider hooks.
3. **The Go side carries a structural probe**, not a real auth probe. `connection_probe.go:90` validates the `keyfile_dict` JSON shape and lights up green for keyless paths; it never mints a token.

## 2. Does this coexist with Strategy A (the Airflow-compat shim)?

### 2.1 The points of friction — there are three

| # | Friction | Severity |
|---|---|---|
| 1 | Airflow's `apache-airflow-providers-google.GCSHook` accepts `keyfile_dict` in the Connection's `extra` and **does not warn** the user that the key is now sitting in our DB. ADR 0035 calls this pattern "explicitly discouraged." | Medium — same DB-at-rest stance ADR 0019 already lives with. Not a blocker; needs a UX nudge. |
| 2 | `key_secret_name` (GCP Secret Manager reference) is a **Dexaflow extension** — the upstream GCSHook doesn't recognize the field. If a user creates a connection with `key_secret_name` set and then writes `from airflow.providers.google.cloud.hooks.gcs import GCSHook`, the hook ignores the field and tries to fall through to ADC (which may not exist), and the task fails silently. | High — silent fallback to wrong path. |
| 3 | `key_path` (mounted K8s Secret) **is** recognized by upstream GCSHook (as `extra__google_cloud_platform__key_path`). So that path "just works" through the shim. | Low — already compatible. |

### 2.2 Strategy A's promise was "drop-in Airflow hook imports"

The promise was:

```python
# user writes this — works because our shim defines BaseHook + Connection
from airflow.providers.google.cloud.hooks.gcs import GCSHook
hook = GCSHook(gcp_conn_id="google_cloud_default")
hook.upload(bucket_name=..., object_name=..., data=...)
```

If we ship Strategy A naively for `google_cloud_platform`, friction #2 (silent `key_secret_name` drop) breaks ADR 0035's invariant: a connection that the operator set up *expecting* a Secret-Manager-fetched key actually runs with a different identity. That is a **security regression**, not a compatibility regression — exactly the class the user worried about.

## 3. The two-tier model — what it is and isn't

### 3.1 What it is — a *policy split*, not a code-split

The right model is **one shim** (the Strategy-A `BaseHook` + `Connection`) **with a connection-time pre-processor** that enforces ADR 0035's resolution order *before* the Airflow hook ever sees the `extra` JSON. We do not maintain two parallel hook hierarchies.

```text
            ┌─────────────────────────────────────────────────┐
            │  User DAG:                                       │
            │    from airflow.providers.google...gcs import   │
            │           GCSHook                                │
            │    hook = GCSHook(gcp_conn_id="x")               │
            └────────────────────────┬─────────────────────────┘
                                     ▼
            ┌─────────────────────────────────────────────────┐
            │  Dexaflow shim — airflow.sdk.* BaseHook           │
            │    Connection.get(conn_id)                       │
            └────────────────────────┬─────────────────────────┘
                                     ▼
            ┌─────────────────────────────────────────────────┐
            │  resolveCredentials(extra) — Dexaflow native      │
            │    1. keyfile_dict → emit as `extra__...keyfile_dict`
            │       AND log "key in DB — see ADR 0035; consider  │
            │       key_path or Workload Identity"               │
            │    2. key_path → emit as `extra__...key_path`      │
            │       (upstream hook reads the file natively)      │
            │    3. key_secret_name → FETCH from Secret Manager  │
            │       NOW; emit the fetched key as a              │
            │       transient `extra__...keyfile_dict` for       │
            │       the upstream hook to consume.                │
            │    4. Otherwise: leave `extra` minimal → upstream  │
            │       falls through to ADC (Workload Identity).    │
            └────────────────────────┬─────────────────────────┘
                                     ▼
            ┌─────────────────────────────────────────────────┐
            │  Upstream GCSHook (pip-installed provider)       │
            │  Reads the standard extra__google_cloud_platform │
            │  __* field names that it already supports —       │
            │  never sees Dexaflow-only fields.                  │
            └─────────────────────────────────────────────────┘
```

### 3.2 What it isn't — *not* two parallel hook hierarchies

We are **not** doing:

- A `leoflow_runtime.hooks.gcp.LeoflowGCPHook` for the "native" tier and a `from airflow.providers.google... GCSHook` for the "compat" tier.
- A `LeoflowConnection` class and an `AirflowConnection` class.
- A connection-type registry with two parallel entries (`google_cloud_platform_native` vs `google_cloud_platform_compat`).

All of those would force the user to pick a tier up-front, breaking the "drop-in Airflow DAG" promise.

### 3.3 The split is in policy, applied at the `BaseHook.get_connection` seam

The pre-processor (~80 LOC of Python in the shim) is the single seam where ADR 0035 lives. By the time the Connection object hits the upstream hook, its `extra` JSON is canonicalized to what the upstream provider already understands. **There is no second hierarchy to maintain.**

The cost in the planning doc was ~1,200 LOC for the shim. Add ~80 LOC for the pre-processor + ~30 LOC per cloud provider for its specific resolver (`gcp_resolver.py`, future `aws_resolver.py`, future `azure_resolver.py`). For three clouds: ~1,400 LOC total. Negligible delta from the original estimate.

## 4. Does the existing GCP code need to change?

Mostly **no**, but a few small adjustments make the seam cleaner once Strategy A lands.

### 4.1 Today's GCP example DAG — should it stop using its inline helper?

The inline helper in `examples/gcp_gcs_load/dag.py` is **the right shape for today**, before the shim exists. It works on Lite and Pro identically and is the only way for a user to consume our `key_secret_name` extension without the shim.

**After Strategy A.0 lands** (the shim with the GCP resolver), the example should be **rewritten in two halves**:

```python
# Half 1 — the "compat" path: a real Airflow DAG works unchanged on Dexaflow
from airflow.providers.google.cloud.hooks.gcs import GCSHook
hook = GCSHook(gcp_conn_id="google_cloud_default")
hook.upload(bucket_name="x", object_name="y", data="...")
```

```python
# Half 2 — the "native" path: ergonomic, Dexaflow-flavored, same wire identity
from leoflow_runtime.cloud.gcp import gcs_client
client = gcs_client(conn_id="google_cloud_default")   # resolves per ADR 0035
client.bucket("x").blob("y").upload_from_string("...")
```

Both halves resolve credentials through the **same** ADR 0035 chain (the pre-processor). Half 1 proves Airflow compat; Half 2 demonstrates the Dexaflow ergonomic surface. The user picks one — the security guarantee is identical.

### 4.2 Go side — is `connection_probe.go` aligned?

Yes, with one caveat. Today `testGCPConnection` (`connection_probe.go:90`) returns:

- "keyfile_dict structurally valid" → green
- "key_path set — validated at task runtime" → green (yellow would be more honest)
- "keyless (ADC / Workload Identity) — resolved at task runtime" → green

After the shim, **add a fourth case for `key_secret_name`**: green with the note "Secret Manager reference — resolved at task runtime". One conditional, ~5 LOC.

The probe stays structural — no cloud SDK in Go (ADR 0014 stance preserved).

### 4.3 ADR 0035 — does it need a revision?

**No.** ADR 0035 is correct as written. The shim does not change the security stance; it adds a compat surface that *honors* the stance via the pre-processor. The follow-up ADR (the one Strategy A asks for — ADR 0036 (Runtime hook compatibility shim)) will *reference* ADR 0035 and say: "Cloud-flavored connections resolve `extra` through the ADR 0035 chain before reaching any upstream hook."

## 5. Generalizing to AWS / Azure

The same pattern works:

| Cloud | Keyless equivalent | Reference-a-secret equivalent | Key-in-DB (discouraged) |
|---|---|---|---|
| GCP | Workload Identity (GKE) / ADC | `key_path` (K8s Secret), `key_secret_name` (Secret Manager) | `keyfile_dict` |
| AWS | IRSA (EKS) / instance role | `role_arn` (assume role) / Secrets Manager reference | `aws_access_key_id` + `aws_secret_access_key` |
| Azure | Workload Identity (AKS) | Key Vault reference | `client_secret` (service principal password) |

Each gets its own ~30-LOC resolver in the pre-processor. The shim itself is unchanged.

The ADR 0035 generalization clause already anticipates this:

> 7. Generalizes to future cloud connectors (AWS, Azure): same stance — platform-native keyless first … resolve in the task, never in core.

## 6. Bottom line — answer to the user's question

> "as vezes nao é seguro ficar armazenando chaves no nosso database — mas pensando em abrir uma exception para caso de integracao nao nativa — consegue avaliar a ADR e o connector GCP que foi implantado se esse connector casa com isso ou se teriamos que fazer 2 classes de connectores: os nossos nativos que seriam o futuro e os do Airflow que seria o presente"

**The GCP connector and ADR 0035 do casa with Strategy A — but only with a small policy seam in the shim.** Specifically:

1. **One connection model** (the one Dexaflow already has — 100% field-level parity with Airflow 3.X per §4 of the compatibility study).
2. **One shim** (Strategy A — ~1,200 LOC).
3. **One ADR 0035 pre-processor** at the `BaseHook.get_connection` seam (~80 LOC + ~30 LOC per cloud) that canonicalizes `extra` to upstream-hook field names *and* fetches secret-store references *before* the upstream hook sees the Connection.

This gives both:

- **Present (Airflow compat):** Existing Airflow DAGs that import `from airflow.providers.google.cloud.hooks.gcs import GCSHook` run unchanged. The user's drop-in promise holds.
- **Future (Leoflow native):** A `leoflow_runtime.cloud.gcp.gcs_client(conn_id)` surface that delegates to the same pre-processor + chooses an ergonomic API surface. The user gets Leoflow-flavored errors, logging, observability hooks. No second hook hierarchy to maintain.

The **exception** the user mentioned ("abrir uma exception para caso de integracao nao nativa") fits naturally: when an Airflow provider would otherwise force a `keyfile_dict`-style key into our DB (e.g. an obscure cloud whose only provider auth path is "store the key"), the pre-processor logs a one-time warning ("ADR 0035: this connector stores a cloud key in Dexaflow's DB; see `key_path` for the recommended pattern") and proceeds. Compat preserved, security stance honored, no parallel hierarchy.

## 7. Concrete next steps (before any code)

1. **Open an ADR** (ADR 0036 (Runtime hook compatibility shim)) that locks the model in §3 and references both ADR 0019 (encryption at rest) and ADR 0035 (cloud auth posture).
2. **Add to the ADR's "Decision":** the pre-processor pipeline; the cloud-resolver plug-in point; the warning emitted when `keyfile_dict` is in use.
3. **Phase A.1 of the compatibility study** stays as-is (postgres / http / sqlite / redis / mysql first — none of these has the cloud-key issue).
4. **Phase A.2** adds the GCP resolver as the *first* cloud — same scope as today's GCP connector but with the pre-processor seam in place.
5. **Phase A.2+** adds AWS and Azure resolvers (same ~30 LOC pattern each).

No urgent rework of today's GCP code is required; the migration of `examples/gcp_gcs_load/dag.py` to the dual-path example shape (§4.1) ships alongside the shim.
