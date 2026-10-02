# gcp_gcs_load — Google Cloud connection (key + keyless)

Writes and reads back a Cloud Storage object using a managed
`google_cloud_platform` Connection, in **either** auth mode. It's the manual
companion to the connection delivery test, and the reference for the GCP
connector.

> Credentials resolve in this order (first match wins), mirroring Airflow's
> `GoogleBaseHook` but with cleaner field names and **keyless as the default**:
> `keyfile_dict` → `key_path` → **ADC (keyless / Workload Identity)**.

## Connection fields (Extra)

Put these in the Connection's **Extra** (JSON). Short names are canonical; the
legacy `extra__google_cloud_platform__<name>` form is also accepted so an
existing Airflow connection drops in unchanged.

| Field | Meaning |
|---|---|
| `keyfile_dict` | Service-account JSON, inline (key mode). Encrypted at rest. Object or stringified JSON. |
| `key_path` | Path to a service-account JSON file mounted in the task (key mode). |
| `project` / `project_id` | GCP project (optional; falls back to the key's / ADC's project). |
| `scopes` / `scope` | OAuth scopes — a **list** or a comma-separated string. |
| `num_retries` | Pass-through to your GCP client (optional). |

Leave all key fields empty for **keyless** (ADC). Advanced Airflow fields
(`key_secret_name`, `credential_config_file`, `impersonation_chain`,
`quota_project_id`) are not handled in v1.

## Prerequisites

- A GCS bucket the credentials can write to.
- Set the bucket as a Leoflow **Variable** `GCS_BUCKET` (Admin → Variables), or
  export `GCS_BUCKET` for a subprocess run.

## Keyless (recommended)

No key in the Connection — credentials come from the ambient identity.

### Pro (GKE — Workload Identity)
1. Create a GCP service account (GSA) with the needed roles (e.g.
   `roles/storage.objectAdmin` on the bucket).
2. Let the chart create + annotate the task KSA (cloud-agnostic knob):
   ```bash
   helm upgrade leoflow ./helm/dexaflow -n leoflow --reuse-values \
     --set taskServiceAccount.create=true --set taskServiceAccount.name=leoflow-gcs \
     --set 'taskServiceAccount.annotations.iam\.gke\.io/gcp-service-account=GSA@PROJECT.iam.gserviceaccount.com'
   gcloud iam service-accounts add-iam-policy-binding GSA@PROJECT.iam.gserviceaccount.com \
     --role roles/iam.workloadIdentityUser \
     --member "serviceAccount:PROJECT.svc.id.goog[leoflow/leoflow-gcs]"
   ```
   Then set `execution.service_account: leoflow-gcs` in the DAG's `dexaflow.yaml`
   (already done in this example).
3. Create the Connection `google_cloud_default` with **empty key fields** (just
   `project`/`scopes` if you want). Run the DAG — no key touches the cluster.

### Lite (local — subprocess executor)
```bash
gcloud auth application-default login        # or export GOOGLE_APPLICATION_CREDENTIALS=/path/key.json
leoflow lite --executor=subprocess dags/gcp_gcs_load
```
The subprocess inherits your host ADC. (The **k3d** executor has no metadata
server, so keyless isn't available there — use key mode under k3d.)

## Key mode (service-account JSON)

> **Discouraged — Leoflow is not a key manager** ([ADR 0035](../../docs/adr/0035-cloud-connector-auth-keyless-first.md)).
> Prefer keyless, or `key_path` pointing at a mounted Kubernetes Secret (the key
> stays in the cluster's secret store, not in Leoflow). Use `keyfile_dict` only
> for dev / low-criticality.

1. Admin → Connections → **+**, Conn Id `google_cloud_default`, type
   **Google Cloud**.
2. In **Extra**, paste:
   ```json
   { "keyfile_dict": { ...service account JSON... }, "project": "my-project" }
   ```
   (The JSON is encrypted at rest — ADR 0019.)
3. Run the DAG. Works on Lite (subprocess or k3d) and Pro alike.

## Key from a Kubernetes Secret (`key_path`) — preferred when not keyless

The key stays in the cluster's secret store; Leoflow only mounts it.

```bash
kubectl -n leoflow create secret generic gcp-sa-key --from-file=key.json=/path/to/key.json
helm upgrade leoflow ./helm/dexaflow -n leoflow --reuse-values \
  --set taskSecret.name=gcp-sa-key --set taskSecret.mountPath=/etc/leoflow/secrets
```
Then the Connection's Extra: `{ "key_path": "/etc/leoflow/secrets/key.json", "project": "my-project" }`.

## Key from GCP Secret Manager (`key_secret_name`)

Store the JSON key in Secret Manager; the task fetches it via ADC (so the task's
identity — typically Workload Identity — needs `roles/secretmanager.secretAccessor`).

```bash
gcloud secrets create leoflow-gcp-key --data-file=/path/to/key.json
```
Then the Connection's Extra: `{ "key_secret_name": "leoflow-gcp-key", "project": "my-project" }`.

## Run + verify

```bash
# Pro: leoflow compile dags/gcp_gcs_load --image <REG>/gcp_gcs_load:v1 --build --push -o dag.json
#      leoflow push dag.json && leoflow runs trigger gcp_gcs_load
# Lite: leoflow lite --executor=subprocess dags/gcp_gcs_load
```
The task log prints the resolved auth mode and `gcs roundtrip ok: gs://<bucket>/leoflow/gcp_gcs_load.txt`.
