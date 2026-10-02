---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/smtp.html
# --- end AUTO redirect aliases ---
title: SMTP connection
linkTitle: SMTP
weight: 460
description: SMTP connection
---

Connect a task to an SMTP mail relay over a managed Leoflow Connection. The
host, port, credentials, and an Extra blob (`from_email`, `timeout`) are
encrypted at rest and delivered to the task as `AIRFLOW_CONN_<CONN_ID>`.

## Declare the provider

```yaml
# dexaflow.yaml
dag_id: smtp_notify
connectors:
  - smtp
```

## URI shape

```
smtp://<login>:<password>@<host>:<port>?__extra__=<json>
```

The control plane percent-escapes the password; `SmtpHook` (which parses
`AIRFLOW_CONN_<ID>`) un-escapes it back. The Extra blob rides in the
`__extra__` query param.

## Fields the UI asks for

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `smtp_default`. Exported as `AIRFLOW_CONN_SMTP_DEFAULT`. |
| Conn Type | yes | `smtp`. |
| Host | yes | The relay host, e.g. `smtp.example.com`. |
| Port | optional | Defaults to `587` (STARTTLS) or `465` (implicit TLS). |
| Login | yes | The SMTP username. |
| Password | yes | Stored encrypted at rest (ADR 0019). Percent-escaped in the URI. |
| Extra | optional | JSON, e.g. `{"from_email":"bot@example.com","timeout":30}`. |

## Example DAG

The hook is imported inside the task body so DAG parsing stays import-light.

```python
# dag.py
from airflow.sdk import DAG, task


@task
def send():
    from airflow.providers.smtp.hooks.smtp import SmtpHook

    with SmtpHook(smtp_conn_id="smtp_default") as hook:
        hook.send_email_smtp(
            to="ops@example.com",
            subject="Leoflow run complete",
            html_content="<p>The pipeline finished.</p>",
        )


with DAG("smtp_notify", schedule=None, catchup=False, tags=["example"]):
    send()
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: smtp_notify
python_version: "3.11"
connectors:
  - smtp
```

## Security notes

- **TLS in transit**: prefer port `587` (STARTTLS) or `465` (implicit TLS).
- **Secrets in logs**: never `print()` the URI — it carries the password.
  Log the host + login only.
- **gRPC channel (agent ↔ control plane)**: secrets are served only over an
  authenticated channel (ADR 0021); Pro must run with TLS.

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery.
- #67 — connectors umbrella.
- #138 — the chain-of-custody contract test this page documents.
