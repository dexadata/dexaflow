---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/file-transfer.html
# --- end AUTO redirect aliases ---
title: File transfer connections (SSH / SFTP / FTP)
weight: 130
description: File transfer connections (SSH / SFTP / FTP)
---

Three closely-related connectors for moving files and running remote commands, all
the same host:port + login/password shape:

| conn_type | Use | Hook | pip package |
|---|---|---|---|
| `ssh` | Run remote commands / SSH tunnels | `SSHHook` | `apache-airflow-providers-ssh` |
| `sftp` | File transfer over SSH | `SFTPHook` | `apache-airflow-providers-sftp` |
| `ftp` | Plain / TLS FTP | `FTPHook` | `apache-airflow-providers-ftp` |

## Declare the provider

```yaml
# dexaflow.yaml
dag_id: file_pull
connectors:
  - sftp        # or ssh / ftp
```

## Fields the UI asks for

| Field | Notes |
|---|---|
| Host | Remote host. |
| Port | `22` for ssh/sftp, `21` for ftp. |
| Login | Remote user. |
| Password | Encrypted at rest (ADR 0019). |
| Extra (ssh/sftp) | `{"key_file": "/path/to/id_rsa"}` or `{"private_key": "..."}` for key auth; `{"no_host_key_check": true}` to skip host-key verification. |

The host:port + login/password round-trip (reserved characters) is covered by
`TestFileTransferConnectionURIShapeIntegration` across all three.

## Example DAG (copy-paste) — SFTP

Docs-only recipe (needs a reachable SFTP server). Hook imported **inside** the
task.

```python
# dag.py
from __future__ import annotations

from airflow.sdk import DAG, task


@task
def pull() -> None:
    from airflow.providers.sftp.hooks.sftp import SFTPHook

    hook = SFTPHook(ssh_conn_id="sftp_default")
    print("pull: listing /incoming via SFTPHook(sftp_default)")
    for name in hook.list_directory("/incoming"):
        print("  ", name)
    hook.retrieve_file("/incoming/data.csv", "/tmp/data.csv")
    print("pull: downloaded data.csv")


with DAG("file_pull", schedule=None, catchup=False, tags=["example"]):
    pull()
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: file_pull
description: Download a file over SFTP via SFTPHook.
owner: examples
tags: [example]
python_version: "3.11"
connectors:
  - sftp
```

`ssh` and `ftp` follow the same shape — swap the hook
(`SSHHook`/`FTPHook`) and the `connectors:` entry.

## Security notes

- **Prefer key auth** (`key_file` / `private_key` in Extra) over passwords for
  ssh/sftp; the key is encrypted at rest.
- **Host-key checking**: leave it on in production; `no_host_key_check` is for
  throwaway/dev only.
- **FTP is plaintext** unless you use FTPS — avoid it for credentials over
  untrusted networks.

## Related

- `docs/connections/index.md` — *Installing a connector's provider*.
- ADR 0019 / 0021 — secret encryption + agent delivery.
