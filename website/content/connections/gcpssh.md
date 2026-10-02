---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/gcpssh.html
# --- end AUTO redirect aliases ---
title: GCP SSH connection
linkTitle: GCP SSH
weight: 170
description: GCP SSH connection
---

Run commands on a Google Compute Engine VM over SSH from a task, via a managed
Dexaflow Connection. The host, port, and credentials are encrypted at rest and
delivered to the task as `AIRFLOW_CONN_<CONN_ID>`.

## Declare the provider

```yaml
# dexaflow.yaml
dag_id: gcpssh_run
connectors:
  - gcpssh
```

## URI shape

```
gcpssh://<login>:<password>@<host>:<port>
```

The control plane percent-escapes the password; `ComputeEngineSSHHook` (which
parses `AIRFLOW_CONN_<ID>`) un-escapes it back.

## Fields the UI asks for

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `gcpssh_default`. Exported as `AIRFLOW_CONN_GCPSSH_DEFAULT`. |
| Conn Type | yes | `gcpssh`. |
| Host | yes | The VM's IP or DNS name, e.g. `10.0.0.5`. |
| Port | optional | Defaults to `22`. |
| Login | yes | The OS login user on the VM. |
| Password | yes | Stored encrypted at rest (ADR 0019). Percent-escaped in the URI. |
| Extra | optional | JSON for hook-specific options (project, zone, IAP). |

## Example DAG

The hook is imported inside the task body so DAG parsing stays import-light.

```python
# dag.py
from airflow.sdk import DAG, task


@task
def remote():
    from airflow.providers.google.cloud.hooks.compute_ssh import (
        ComputeEngineSSHHook,
    )

    hook = ComputeEngineSSHHook(gcp_conn_id="gcpssh_default")
    client = hook.get_conn()
    _, stdout, _ = client.exec_command("uname -a")
    return stdout.read().decode()


with DAG("gcpssh_run", schedule=None, catchup=False, tags=["example"]):
    remote()
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: gcpssh_run
python_version: "3.11"
connectors:
  - gcpssh
```

## Security notes

- **Prefer keys / IAP over passwords**: production Compute Engine SSH normally
  uses OS Login or an SSH key plus Identity-Aware Proxy; pass those in Extra.
  The password field is for environments that still rely on it.
- **Secrets in logs**: never `print()` the URI — it carries the password.
  Log the host + login only.
- **gRPC channel (agent ↔ control plane)**: secrets are served only over an
  authenticated channel (ADR 0021); Pro must run with TLS.

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery.
- #67 — connectors umbrella.
- #138 — the chain-of-custody contract test this page documents.
