---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /alerting.html
# --- end AUTO redirect aliases ---
title: On-failure alerting
linkTitle: Alerting
weight: 50
description: "Notify on run failure from dexaflow.yaml — Slack or a generic webhook, no extra task and no Python."
---

Dexaflow can **notify you when a run fails** — Slack or a generic webhook — with no
extra task and no Python. You declare the rules in `dexaflow.yaml`; the **scheduler
fires them in Go** the moment a DagRun reaches the terminal `failed` state.

```yaml
# dexaflow.yaml
alerts:
  on_failure:
    - type: slack
      conn: slack_prod
      message: "🚨 {{dag}} run {{run_id}} failed on {{task}}"
    - type: webhook
      conn: pagerduty
```

That's it. Compile and deploy as usual — when a run of this DAG fails, both the
Slack message and the PagerDuty webhook fire.

> **Native, not a task.** This runs in the control plane on the terminal failure —
> no task pod, no Python in the hot path, and it can't be skipped by an upstream
> failure the way a notify *task* can. For the task-based pattern (and its
> trade-offs) see [When to use a task instead](#when-to-use-a-task-instead).

## How it works

```mermaid
flowchart TB
  A["dexaflow.yaml<br/>alerts:"] -->|"compile<br/>(validate + overlay)"| B["dag.json<br/>(alerts baked in)"]
  B --> C["scheduler sees the run reach <code>failed</code>"]
  C -->|"off the tick, in a goroutine"| D["for each rule:<br/>resolve connection → endpoint URL,<br/>render the message, POST it"]
  D --> E["Slack / PagerDuty / Opsgenie / Teams …"]
```

Three guarantees hold by design:

- 🔒 **The secret stays in the connection.** The webhook URL (which *is* a secret)
  lives encrypted in a managed connection — never in `dexaflow.yaml` and never in
  the compiled `dag.json`.
- 🛟 **Best-effort.** A delivery that fails (a 500, a bad URL, a missing
  connection) is logged and dropped — it can **never** fail the run, and one bad
  rule never stops the others.
- ⚡ **Off the tick path.** Alerts dispatch in a detached goroutine, so a slow
  endpoint can't stall the scheduler.

## Setting up the connection

The endpoint URL is stored as the connection's **secret** (so it's encrypted at
rest). Create the connection once, then reference it by id from any DAG.

{{< tabpane text=true >}}
{{% tab header="Slack" %}}

Create a [Slack incoming webhook](https://api.slack.com/messaging/webhooks)
and store its full URL:

```console
$ curl -X POST .../api/v2/connections -d '{
    "connection_id":"slack_prod","conn_type":"slack",
    "password":"https://hooks.slack.com/services/T00000000/B00000000/XXXXXXXX"}'
```
{{% /tab %}}
{{% tab header="Generic webhook" %}}

Any endpoint that accepts a JSON `POST` (PagerDuty Events v2, Opsgenie, a
Microsoft Teams incoming webhook, your own service):

```console
$ curl -X POST .../api/v2/connections -d '{
    "connection_id":"pagerduty","conn_type":"http",
    "password":"https://events.pagerduty.com/v2/enqueue"}'
```
{{% /tab %}}
{{< /tabpane >}}

> The webhook URL goes in `password` on purpose: Dexaflow encrypts it at rest and
> hands it straight to the sender at failure time. `host`/`login`/`schema` are
> ignored for alert connections.

## The message

`message` is optional. When set, these placeholders are substituted at failure
time; when omitted, Dexaflow sends a default one-line summary.

| Placeholder        | Becomes                                             |
| ------------------ | --------------------------------------------------- |
| `{{dag}}`          | the DAG id                                              |
| `{{run_id}}`       | the run id you see in the UI (`manual__2026-07-30T…`)   |
| `{{logical_date}}` | the run's logical date, RFC3339                         |
| `{{task}}`         | the first task that failed                              |
| `{{tasks}}`        | every failed task, comma-separated                      |

A placeholder with no value for this run renders `(none)` rather than an empty
string — `{{logical_date}}` on a manually triggered run, for example. An alert
reading `failed for logical date ` is indistinguishable from a truncated one, so
the marker is deliberate.

Anything else in `{{…}}` is a **compile error**, naming the offending placeholder
and listing what is available. A typo used to survive compile and reach the
alert verbatim, which meant discovering it in the message that was supposed to
explain an outage.

**What each channel receives:**

- **`slack`** → Slack's incoming-webhook shape: `{"text": "<your message>"}`.
- **`webhook`** → a structured JSON body, so your service can route on it:

  ```json
  {
    "dag_id": "sales",
    "run_id": "manual__2026-07-17T09:00:00Z",
    "logical_date": "2026-07-17T09:00:00Z",
    "failed_tasks": ["transform"],
    "message": "🚨 sales run … failed on transform"
  }
  ```

## When to use a task instead

The `alerts:` block is the right default. But if you need the notification to run
your **own code** (enrich the payload, hit an internal API, page via a provider
operator), add a notify **task** guarded by a failure trigger rule — it runs in a
pod with full operator fidelity:

```python
alert = SlackWebhookOperator(
    task_id="alert", slack_webhook_conn_id="slack",
    message="pipeline failed 🚨", trigger_rule="one_failed",
)
[extract, transform] >> alert
```

Trade-off: a task runs a pod and is itself a task in the graph (it can be skipped
if the whole run is torn down), whereas the native `alerts:` block always fires on
the terminal failure, in the control plane, for free.

## The Airflow `on_failure_callback`

If you already write Airflow, you can also use its native **per-task**
`on_failure_callback` — a Python callable set on the operator or `@task`. Dexaflow
runs it **in-process, inside the task's own pod**, on the task's **terminal**
failure:

```python
def alert(context):
    # your code: enrich, page, post — runs in the task pod on final failure
    print("failed:", context["task_instance"].task_id)

@task(on_failure_callback=alert, retries=2)
def transform(): ...
```

How it differs from the `alerts:` block:

| | `alerts:` block | `on_failure_callback` |
| --- | --- | --- |
| Scope | the whole **DagRun** | one **task** |
| Runs where | control plane (Go) | the task's **pod** (your Python) |
| Fires when | the run reaches `failed` | that task's **final** attempt fails |
| Needs a pod | no | yes (it's your task's pod) |

**Only the terminal attempt fires it.** The callback runs only once retries are
exhausted (`try_number >= max_tries`), matching Airflow. With `retries: 2` a
`fail → fail → success` run fires it **zero** times; a `fail × 3` run fires it
**once**, on the last attempt. It's best-effort and anti-loop: a callback that
raises is logged and swallowed — it never changes the task's own `failed` outcome
and can't re-trigger itself.

**Where it works — and where the compiler stops you.** It runs on any
Python-executed task: a provider **operator** or a **`@task`**. It **cannot** run
on the two task types that leave no Python to run it, and the compiler rejects
those **loudly** rather than dropping the callback silently:

- **`bash`** — the runtime `exec`s bash in place, so no Python is left.
- **`http_api`** — *removed (ADR 0047/0048).* `HttpOperator` now runs in a pod like
  any provider operator (declare `connectors: [http]`); the old inline path ran in
  the control-plane process and is being removed.

`on_success_callback` and `on_retry_callback` aren't wired yet — they're a loud
compile error everywhere, never a silent drop. Reach for the `alerts:` block or a
downstream `@task` with a `trigger_rule` instead.

## Reference

| Field                      | Required | Notes                                              |
| -------------------------- | -------- | -------------------------------------------------- |
| `alerts.on_failure[].type` | yes      | `slack` or `webhook`                               |
| `alerts.on_failure[].conn` | yes      | managed connection id; its secret = the endpoint URL |
| `alerts.on_failure[].message` | no    | templated (see above); empty → default summary     |

{{% alert title="Scope today" color="info" %}}
Two on-failure surfaces ship: the native `alerts:` block (this page's focus,
firing on the terminal **DagRun** failure) and Airflow's per-task
`on_failure_callback` (above), which runs in the task's pod on its terminal
failure. `on_success` / `on_retry` callbacks and SLA-miss alerts are not wired
yet (a loud compile error, never a silent drop) — tracked in
[#424](https://github.com/dexadata/leoflow/issues/424).
{{% /alert %}}
