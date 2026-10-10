---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/imap.html
# --- end AUTO redirect aliases ---
title: IMAP connection
linkTitle: IMAP
weight: 240
description: IMAP connection
---

Connect a task to an IMAP mailbox over a managed Dexaflow Connection — to poll
for incoming files or messages. The host, port, and credentials are encrypted
at rest and delivered to the task as `AIRFLOW_CONN_<CONN_ID>`.

## Declare the provider

```yaml
# dexaflow.yaml
dag_id: imap_poll
connectors:
  - imap
```

## URI shape

```
imap://<login>:<password>@<host>:<port>
```

The control plane percent-escapes the password; `ImapHook` (which parses
`AIRFLOW_CONN_<ID>`) un-escapes it back.

## Fields the UI asks for

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `imap_default`. Exported as `AIRFLOW_CONN_IMAP_DEFAULT`. |
| Conn Type | yes | `imap`. |
| Host | yes | The IMAP host, e.g. `imap.example.com`. |
| Port | optional | Defaults to `993` (implicit TLS). |
| Login | yes | The mailbox username. |
| Password | yes | Stored encrypted at rest (ADR 0019). Percent-escaped in the URI. |
| Extra | optional | JSON for hook-specific options. |

## Example DAG

The hook is imported inside the task body so DAG parsing stays import-light.

```python
# dag.py
from airflow.sdk import DAG, task


@task
def check():
    from airflow.providers.imap.hooks.imap import ImapHook

    with ImapHook(imap_conn_id="imap_default") as hook:
        return hook.has_mail_attachment("report-*.csv")


with DAG("imap_poll", schedule=None, catchup=False, tags=["example"]):
    check()
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: imap_poll
python_version: "3.11"
connectors:
  - imap
```

## Security notes

- **TLS in transit**: prefer port `993` (implicit TLS) over plain `143`.
- **Secrets in logs**: never `print()` the URI — it carries the password.
  Log the host + login only.
- **gRPC channel (agent ↔ control plane)**: secrets are served only over an
  authenticated channel (ADR 0021); Pro must run with TLS.

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery.
- #67 — connectors umbrella.
- #138 — the chain-of-custody contract test this page documents.
