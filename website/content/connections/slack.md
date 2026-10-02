---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /connections/slack.html
# --- end AUTO redirect aliases ---
title: Slack connection
linkTitle: Slack
weight: 450
description: Slack connection
---

Send messages / alerts to Slack from a task via a managed Leoflow Connection.
Two conn types ship: `slack` (Slack **API**, a bot token) and `slackwebhook` (an
**Incoming Webhook** URL). Use `slack` for rich API calls, `slackwebhook` for a
fire-and-forget message to one channel.

## Declare the provider

```yaml
# dexaflow.yaml
dag_id: slack_alert
connectors:
  - slack
```

## Fields the UI asks for

**`slack` (API):**

| Field | Where it goes | Notes |
|---|---|---|
| Conn Id | — | e.g. `slack_default`. |
| Password | password | The **bot token** (`xoxb-…`). Encrypted at rest (ADR 0019). |
| Base URL / Timeout | extra | Optional API tuning. |

**`slackwebhook`:** put the webhook path in the connection (host
`hooks.slack.com`, password = the `T00/B00/XXXX` token segment), or the full URL
in Extra `{"webhook_token": "…"}`.

The token round-trip (reserved characters included) is pinned by
`TestSlackConnectionURIShapeIntegration`.

## Example DAG (copy-paste)

Docs-only recipe (needs a Slack workspace + bot token). The hook is imported
**inside** the task.

```python
# dag.py
from __future__ import annotations

from airflow.sdk import DAG, task


@task
def notify() -> None:
    from airflow.providers.slack.hooks.slack import SlackHook

    hook = SlackHook(slack_conn_id="slack_default")
    print("notify: posting message via SlackHook(slack_default)")
    hook.call(
        "chat.postMessage",
        json={"channel": "#data-alerts", "text": "Leoflow run finished ✅"},
    )


with DAG("slack_alert", schedule=None, catchup=False, tags=["example"]):
    notify()
```

```yaml
# dexaflow.yaml
schema_version: "1.0"
dag_id: slack_alert
description: Post a message to Slack via SlackHook.
owner: examples
tags:
  - example
python_version: "3.11"
connectors:
  - slack
```

### Run it

1. Create a Slack app with a bot token (`chat:write` scope), invite the bot to the
   channel.
2. **Admin → Connections → +**, type `slack`, paste the token in Password.
3. `leoflow lite path/to/this/dag` → trigger `slack_alert` → check the channel.

## Security notes

- The bot token is a secret — it is encrypted at rest and never echoed back by the
  form. Scope it to the minimum (`chat:write`).
- **Never `print()` the URI** — it carries the token.

## Related

- `docs/connections/index.md` — *Installing a connector's provider*.
- ADR 0019 / 0021 — secret encryption + agent delivery.
