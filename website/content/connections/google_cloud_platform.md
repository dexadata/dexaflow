---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/google_cloud_platform.html
# --- end AUTO redirect aliases ---
title: "`google_cloud_platform` — Google Cloud connection"
linkTitle: "`google_cloud_platform` — Google Cloud"
weight: 200
description: "`google_cloud_platform` — Google Cloud connection"
---

Connect tasks to Google Cloud (GCS, BigQuery, Pub/Sub, …) with a managed
Connection, in **two auth modes**: **keyless** (Workload Identity / ADC —
recommended) and **service-account key** (encrypted at rest).

The connection follows Airflow's `google_cloud_platform` shape — an existing
Airflow GCP connection drops in unchanged — with cleaner short field names and
keyless as the default.

## URI shape

GCP carries no host/login/password — everything lives in **Extra**. The control
plane delivers it as `AIRFLOW_CONN_GOOGLE_CLOUD_DEFAULT`:

```
google-cloud-platform://?__extra__=<url-encoded JSON>
```

## Extra fields

Short names are canonical; the legacy `extra__google_cloud_platform__<name>`
form is also accepted.

| Field | Meaning |
|---|---|
| `keyfile_dict` | Service-account JSON, inline (key in the connection). **Encrypted at rest** (ADR 0019). Discouraged. |
| `key_path` | Path to a service-account JSON **file mounted from a Kubernetes Secret** (the chart's `taskSecret`). |
| `key_secret_name` | Name (or full resource path) of a **GCP Secret Manager** secret holding the JSON key; fetched in the task via ADC. |
| `project` / `project_id` | GCP project (optional). |
| `scopes` / `scope` | OAuth scopes — a list **or** a comma-separated string. |
| `num_retries` | Pass-through to your GCP client (optional). |

**Resolution order (first match wins):** `keyfile_dict` → `key_path` →
`key_secret_name` → **ADC (keyless)**. Leave all key fields empty for keyless.

Not handled in v1: `key_secret_project_id`, `credential_config_file`,
`impersonation_chain`, `quota_project_id`.

## Auth modes

### Keyless (recommended)
No key in the Connection. Credentials come from Application Default Credentials:

- **Pro (GKE):** **Workload Identity** — the task pod runs as a Kubernetes SA
  bound to a GCP service account; no key ever touches the cluster. See the
  chart's task-ServiceAccount knob and issue #56.
- **Lite (subprocess):** your host ADC (`gcloud auth application-default login`
  or `GOOGLE_APPLICATION_CREDENTIALS`).
- **Lite (k3d):** no metadata server → keyless unavailable; use key mode.

### Key from a Kubernetes Secret (`key_path`) — preferred when not keyless
The key lives in a **Kubernetes Secret**, mounted read-only into every task pod;
the connection only references the file by `key_path`. The key never enters
Dexaflow's DB/API/UI. Wire it via the chart:

```bash
kubectl -n leoflow create secret generic gcp-sa-key --from-file=key.json=/path/to/key.json
helm upgrade <release> <chart> -n leoflow --reset-then-reuse-values \
  --set taskSecret.name=gcp-sa-key --set taskSecret.mountPath=/etc/leoflow/secrets
```
Then set the connection's `key_path` to `/etc/leoflow/secrets/key.json`.

### Key from GCP Secret Manager (`key_secret_name`)
The connection references a Secret Manager secret name; the task fetches it at
runtime via ADC (so the task still needs an ambient identity — typically
Workload Identity — to *read* the secret). Grant the task's GSA
`roles/secretmanager.secretAccessor`.

### Key inline (`keyfile_dict`) — discouraged
The SA JSON in the connection's Extra (encrypted at rest). Convenient for
dev/low-criticality; **not** recommended (see Security below).

### dbt (BigQuery) profile mapping
When a **dbt** task uses this connection, Dexaflow generates the BigQuery
`profiles.yml` from it:

| Extra | dbt profile |
|---|---|
| `method: oauth` | `method: oauth` — **keyless**, via Application Default Credentials (Workload Identity on Pro). No key shipped. `dataset` required; `project` optional (defers to the ADC project). |
| `keyfile_dict` (inline SA JSON) | `method: service-account-json` + `keyfile_json` |

Set **`method: oauth`** in Extra to get the keyless profile; otherwise the mapper
expects `keyfile_dict`. Prefer keyless — it ships no downloadable key.

## Lite vs Pro

| | Lite (subprocess) | Lite (k3d) | Pro (GKE) |
|---|---|---|---|
| Keyless (ADC) | ✅ host ADC | ❌ (no metadata server) | ✅ Workload Identity |
| `key_path` (K8s Secret) | ✅ (mount a file) | ✅ (k3d Secret) | ✅ (chart `taskSecret`) |
| `key_secret_name` (Secret Manager) | ✅ (needs ADC) | ⚠️ needs ADC | ✅ (WI + secretAccessor) |
| `keyfile_dict` (inline) | ✅ | ✅ | ✅ |

## Security — Dexaflow is not a key manager

Credentials should come from the **runtime identity** (keyless) or from a
**secret the platform manages**, which the connection only *references*. Dexaflow
does not aspire to store cloud keys (see [ADR 0035](/project/adrs/0035-cloud-connector-auth-keyless-first/)).
In order of preference:

1. **Keyless (Workload Identity / ADC)** — recommended. No key anywhere.
2. **Reference a platform-managed secret** — the key lives in the cluster's or
   cloud's secret store; the connection holds only a reference. The key never
   enters Dexaflow's DB/API/UI:
   - **`key_path` + a mounted Kubernetes Secret** (chart `taskSecret`);
   - **`key_secret_name` + GCP Secret Manager** (task fetches via ADC).
3. **`keyfile_dict`** — **discouraged.** It stores the key inside the connection
   (encrypted at rest with `DEXAFLOW_SECRET_KEY`, ADR 0019; delivered only over
   the TLS agent channel, ADR 0021). It is the cloud-key analog of how
   connections store a database user/password — pragmatic and Airflow-compatible,
   but **not** the desired posture. Use only for dev / low-criticality; many orgs
   forbid creating such keys anyway.

## Example DAG + test

- Example: [examples/gcp_gcs_load](https://github.com/dexadata/dexaflow/tree/main/examples/gcp_gcs_load)
  — writes + reads a GCS object in both modes, with a clean `gcp_credentials()`
  helper.
- Delivery (chain-of-custody) is covered by an automated test that round-trips a
  synthetic key through encryption + `__extra__` (no real cloud needed); a real
  end-to-end run against GCS is documented as manual in the example README.

See also: [variables-connections.md](/author-dags/variables-connections/), ADR 0019, ADR 0021.
