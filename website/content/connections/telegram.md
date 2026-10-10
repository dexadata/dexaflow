---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/telegram.html
# --- end AUTO redirect aliases ---
title: Telegram connection
linkTitle: Telegram
weight: 510
description: Telegram connection
---

Send messages to a Telegram chat or channel from a task over a managed
Dexaflow Connection. `TelegramHook` posts to the Bot API using a bot token.

## Declare the provider

```yaml
# dexaflow.yaml
dag_id: telegram_notify
connectors:
  - telegram
```

## URI shape

```
telegram://:<bot_token>@
```

There is **no host** — the token (token-only shape, like Slack) lives in
Password and is percent-escaped. The chat/channel id is supplied per
message by the operator, or carried in `Extra`.

## Fields the UI asks for

| Field | Required | Notes |
|---|---|---|
| Conn Id | yes | e.g. `telegram_default`. Exported as `AIRFLOW_CONN_TELEGRAM_DEFAULT`. |
| Conn Type | yes | `telegram`. |
| Password | yes | The bot token from @BotFather, e.g. `1234567:AAH...`. Encrypted at rest (ADR 0019). |
| Host | no | Leave blank. |
| Extra | optional | JSON: `{"chat_id":"-1001234567890"}` to pin a default chat. |

## Example DAG

```python
# dag.py
from airflow.sdk import DAG, task


@task
def send():
    from airflow.providers.telegram.hooks.telegram import TelegramHook

    hook = TelegramHook(telegram_conn_id="telegram_default")
    hook.send_message({"chat_id": "-1001234567890", "text": "DAG finished"})


with DAG("telegram_notify", schedule=None, catchup=False, tags=["example"]):
    send()
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: telegram_notify
python_version: "3.12"
connectors:
  - telegram
connections:
  - telegram_default
```

## Security notes

- **Treat the bot token like a password**: anyone with it can post as the
  bot. Revoke and re-issue via @BotFather if leaked.
- **Restrict the bot**: add it only to the chats it must post to; disable
  group privacy only if the bot must read messages.
- Never log `AIRFLOW_CONN_TELEGRAM_DEFAULT`; it carries the token.

## Related

- ADR 0019 — secret encryption at rest.
- ADR 0021 — agent secret delivery (`AIRFLOW_CONN_<CONN_ID>`).
- `TestTelegramConnectionURIShapeIntegration` — chain-of-custody delivery test.
- [Slack connection](/connections/slack/) — the same token-in-password shape.
