---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/discord.html
# --- end AUTO redirect aliases ---
title: Discord connection
linkTitle: Discord
weight: 80
description: Discord connection
---

Post messages to a Discord channel from a task over a managed Leoflow
Connection. `DiscordWebhookHook` sends to a channel webhook.

## Declare the provider

```yaml
# dexaflow.yaml
dag_id: discord_notify
connectors:
  - discord
```

## URI shape

```
discord://:<webhook_token>@?__extra__=<json>
```

There is no host. The webhook token lives in Password (percent-escaped),
and the webhook endpoint path (`webhooks/<id>/<token>`) rides in `Extra`
under `__extra__`. The hook joins Discord's base webhook URL with the
endpoint to post.

## Fields the UI asks for

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `discord_default`. Exported as `AIRFLOW_CONN_DISCORD_DEFAULT`. |
| Conn Type | yes | `discord`. |
| Password | optional | The webhook token. Encrypted at rest (ADR 0019). |
| Extra | yes | JSON: `{"webhook_endpoint":"webhooks/123/abc"}`. |
| Host | no | Leave blank. |

## Example DAG

```python
# dag.py
from airflow.sdk import DAG, task


@task
def send():
    from airflow.providers.discord.hooks.discord_webhook import (
        DiscordWebhookHook,
    )

    hook = DiscordWebhookHook(
        http_conn_id="discord_default",
        message="DAG finished",
    )
    hook.execute()


with DAG("discord_notify", schedule=None, catchup=False, tags=["example"]):
    send()
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: discord_notify
python_version: "3.12"
connectors:
  - discord
connections:
  - discord_default
```

## Security notes

- **The webhook URL is the secret**: anyone with it can post to the
  channel. Keep the endpoint in `Extra` and the token in Password —
  both are encrypted at rest.
- **Rotate on leak**: delete and recreate the webhook in Discord channel
  settings, then update the Connection.
- Never log `AIRFLOW_CONN_DISCORD_DEFAULT`.

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery (`AIRFLOW_CONN_<CONN_ID>`).
- `TestDiscordConnectionURIShapeIntegration` — chain-of-custody delivery test.
