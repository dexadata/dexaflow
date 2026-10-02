"""gcp_gcs_load — write + read a GCS object using a managed Dexaflow Connection.

Demonstrates the `google_cloud_platform` connection in **both** auth modes:

  * **keyless (recommended)** — no key in the Connection. Credentials come from
    Application Default Credentials (ADC): on GKE/Pro that's **Workload Identity**
    (the task pod's KSA bound to a GCP service account); locally/Lite it's
    `gcloud auth application-default login` or GOOGLE_APPLICATION_CREDENTIALS.
  * **key** — a service-account JSON stored in the Connection (`keyfile_dict`,
    encrypted at rest), or a `key_path` to a mounted file.

The Connection is injected as AIRFLOW_CONN_GOOGLE_CLOUD_DEFAULT. The target
bucket comes from the `GCS_BUCKET` Variable (AIRFLOW_VAR_GCS_BUCKET) or env.

Field names follow Airflow's (so an existing GCP connection drops in), but we
keep the clean short names and also accept a few quality-of-life improvements
(`scopes` as a list or a comma string; keyless is the default).
"""
from __future__ import annotations

import json
import os
import urllib.parse

from airflow.sdk import DAG, task

GCP_CONN = "GOOGLE_CLOUD_DEFAULT"


def _conn_extra(conn_id: str) -> dict:
    """Parse the Connection's Extra JSON out of AIRFLOW_CONN_<ID> (the __extra__ param)."""
    uri = os.environ.get(f"AIRFLOW_CONN_{conn_id.upper()}")
    if not uri:
        return {}
    raw = urllib.parse.parse_qs(urllib.parse.urlsplit(uri).query).get("__extra__", [None])[0]
    return json.loads(raw) if raw else {}


def _field(extra: dict, name: str):
    """Prefer the clean short name; fall back to Airflow's legacy extra__google_cloud_platform__<name>."""
    val = extra.get(name)
    if val in (None, ""):
        val = extra.get(f"extra__google_cloud_platform__{name}")
    return val


def gcp_credentials(conn_id: str = GCP_CONN):
    """Resolve GCP credentials from a Dexaflow Connection. Returns (creds, project, mode).

    Resolution (first match wins): keyfile_dict -> key_path (K8s Secret) ->
    key_secret_name (GCP Secret Manager) -> ADC (keyless / Workload Identity).
    """
    import google.auth
    from google.oauth2 import service_account

    extra = _conn_extra(conn_id)
    scopes = _field(extra, "scopes") or _field(extra, "scope")
    if isinstance(scopes, str):
        scopes = [s.strip() for s in scopes.split(",") if s.strip()]
    project = _field(extra, "project") or _field(extra, "project_id")

    keyfile_dict = _field(extra, "keyfile_dict")
    key_path = _field(extra, "key_path")

    if keyfile_dict:
        info = json.loads(keyfile_dict) if isinstance(keyfile_dict, str) else keyfile_dict
        creds = service_account.Credentials.from_service_account_info(info, scopes=scopes)
        return creds, project or info.get("project_id"), "key (keyfile_dict)"
    if key_path:
        creds = service_account.Credentials.from_service_account_file(key_path, scopes=scopes)
        return creds, project, "key (key_path / K8s Secret)"

    key_secret_name = _field(extra, "key_secret_name")
    if key_secret_name:
        # Fetch the key from GCP Secret Manager using ADC (so the task still needs
        # an ambient identity to *read* the secret — typically Workload Identity).
        from google.cloud import secretmanager

        sm = secretmanager.SecretManagerServiceClient()
        name = key_secret_name
        if not name.startswith("projects/"):
            name = f"projects/{project}/secrets/{name}/versions/latest"
        payload = sm.access_secret_version(name=name).payload.data.decode("utf-8")
        info = json.loads(payload)
        creds = service_account.Credentials.from_service_account_info(info, scopes=scopes)
        return creds, project or info.get("project_id"), "key (Secret Manager)"

    creds, adc_project = google.auth.default(scopes=scopes)
    return creds, project or adc_project, "keyless (ADC / Workload Identity)"


@task
def gcs_roundtrip() -> str:
    from google.cloud import storage

    bucket_name = os.environ.get("AIRFLOW_VAR_GCS_BUCKET") or os.environ.get("GCS_BUCKET")
    if not bucket_name:
        raise RuntimeError("set the GCS_BUCKET Variable (or env) to a bucket the credentials can write to")

    creds, project, mode = gcp_credentials()
    print(f"gcp auth mode: {mode}  project: {project}")

    client = storage.Client(project=project, credentials=creds)
    blob = client.bucket(bucket_name).blob("leoflow/gcp_gcs_load.txt")
    payload = "hello from leoflow gcp_gcs_load"
    blob.upload_from_string(payload)
    got = blob.download_as_text()
    assert got == payload, f"roundtrip mismatch: {got!r}"
    print(f"gcs roundtrip ok: gs://{bucket_name}/{blob.name} ({len(got)} B)")
    return blob.name


with DAG("gcp_gcs_load", schedule=None, catchup=False, tags=["example"]):
    gcs_roundtrip()
