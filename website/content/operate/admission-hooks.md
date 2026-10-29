---
title: Admission hooks
description: Let an endpoint of yours admit, change, hold or refuse every task attempt before it gets a credential or a pod.
weight: 79
---

Status: available in both editions, off by default
([ADR 0100](/project/adrs/0100-dispatch-admission-hook/), issue #2177).

An admission hook is asked about each task attempt after Dexaflow's own checks
(the [placement allowlist](/operate/placement-allowlist/), the image
allowlist, the resource unit) and before the attempt's agent token is minted
or its pod is created. It answers one of:

| Answer | What happens |
|---|---|
| `admit` | The attempt runs as it is. |
| `admit` with `changes` | The attempt runs with its node selector, tolerations, priority class, labels, annotations or service account changed. |
| `hold` | The attempt waits and is offered again. The task fails once it has waited 15 minutes, with the hook's reason. |
| `refuse` | The task fails at once, without retries. Its failure reads `dispatch refused: admission: <reason>`. |

Use it to place each tenant's tasks on its own nodes, to label pods for cost
reporting, to keep work off the cluster during a freeze window, or to refuse
work a budget does not cover. Because the decision comes before the token and
the pod, a refused attempt never runs and never holds a credential, on Lite as
on Cluster.

## Turn it on

Point `execution.admission.webhook.url` at an endpoint. With the Helm chart:

```yaml
execution:
  admission:
    webhook:
      url: https://admission.policy.svc:8443/admit
      # Optional: a ConfigMap with key ca.crt that signs the endpoint's
      # certificate, and a Secret with key token sent as a bearer.
      caConfigMap: policy-ca
      tokenSecret: policy-token
    timeout: 500ms
    failOpen: false
```

| Setting | Helm | Default | Meaning |
|---|---|---|---|
| `execution.admission.webhook.url` | `execution.admission.webhook.url` | empty | The endpoint. `https`, unless the host is loopback. Empty installs no hook. |
| `execution.admission.webhook.ca_file` | `execution.admission.webhook.caConfigMap` | system roots | PEM bundle the endpoint's certificate must chain to. Read at boot, so a changed bundle takes a restart. |
| `execution.admission.webhook.token_file` | `execution.admission.webhook.tokenSecret` | none | Bearer token sent as `Authorization: Bearer`, read on every call, so a rotated Secret is picked up without a restart. |
| `execution.admission.timeout` | `execution.admission.timeout` | `500ms` | How long to wait for an answer, at most `5s`. |
| `execution.admission.fail_open` | `execution.admission.failOpen` | `false` | What to do when the endpoint is broken; see below. |

The chart mounts the CA bundle and the token only on the pods that dispatch
tasks (the scheduler in a split install), never on the API pods. The control
plane connects to the endpoint directly: `HTTPS_PROXY` and the other proxy
variables are not used.

## The request

The control plane POSTs one JSON document per attempt:

```json
{
  "api_version": "dexaflow.io/admission/v1",
  "kind": "AdmissionReview",
  "request": {
    "uid": "6f1c2a8e-0d4b-4f5e-9a3c-2b7d8e1f0a42",
    "tenant_id": "acme",
    "dag_id": "nightly_etl",
    "run_id": "0193f2c4-...",
    "task_id": "extract",
    "try_number": 1,
    "operator": "python",
    "image": "ghcr.io/acme/etl@sha256:...",
    "source_mode": false,
    "resources": {
      "requests": {"cpu": "500m", "memory": "1Gi"},
      "limits": {"memory": "1Gi"}
    },
    "execution": {
      "service_account": "dexaflow-task",
      "node_selector": {"zone": "a"},
      "labels": {"team": "data"}
    },
    "service_account_chosen": false
  }
}
```

`resources` are the effective ones, after the resource unit or the platform
defaults. `execution.service_account` is the account the pod would run as;
`service_account_chosen` is false when that is the operator's default rather
than one the task named. The request never carries the task's environment,
its source, its secrets or its token, but it does carry tenant and DAG names,
so the endpoint must be `https` unless it is on the same host.

## The answer

Answer with HTTP 200 and the same envelope, echoing the request's `uid`:

```json
{
  "api_version": "dexaflow.io/admission/v1",
  "kind": "AdmissionReview",
  "response": {
    "uid": "6f1c2a8e-0d4b-4f5e-9a3c-2b7d8e1f0a42",
    "decision": "admit",
    "changes": {
      "node_selector": {"tenant": "acme"},
      "tolerations": [{"key": "tenant", "operator": "Equal", "value": "acme", "effect": "NoSchedule"}],
      "labels": {"cost-center": "4711"}
    }
  }
}
```

- `decision` is `admit`, `hold` or `refuse`. `hold` and `refuse` take a
  `reason`, which the task instance shows (control characters removed, cut to
  512 bytes), and no `changes`.
- `changes` may set `node_selector` and `labels`/`annotations` (merged over the
  task's own, your keys win), `tolerations` (appended), `priority_class_name` and
  `service_account`. Nothing else: the image, command, environment, namespace
  and resources stay as Dexaflow chose them, so the attempt is charged the pool
  slots it was admitted with.

A change is refused, naming the hook, when it would set a label or annotation
key under `leoflow.io/` or `dexaflow.io/`, a key, value or toleration
Kubernetes would reject, a `priority_class_name` outside
`execution_allowlist.priority_classes`, or a `service_account` outside
`execution_allowlist.service_accounts`. A webhook may change the service
account only when that list is set, so whoever runs the endpoint cannot move a
task onto any account of the namespace. Changes are not checked against
`execution_allowlist.fields`: that list governs what DAG authors set, and the
task's own fields are still checked against it before the hook is asked.

An attempt whose placement the hook changed runs on a dedicated pod, like a
task that sets its own placement, never on a warm worker.

The answer must be at most 64 KiB and contain no field besides the ones above
(field names are matched as Go's JSON decoder matches them, ignoring case). An
`admit` that carries a `reason`, or a `hold` or `refuse` that carries
`changes`, is a malformed answer and is treated like a broken endpoint, below.
Redirects are not followed.

## When the endpoint is broken

A connection error, a status other than 200, an answer that does not decode
or does not echo the `uid`, or no answer within the timeout all mean the hook
is broken. By default the attempt is held, as with `hold`, so a task never
runs unchecked while the endpoint is down; it fails as `dispatch_failed` once
it has waited 15 minutes, naming the admission hook. That failure says only
that the request failed or timed out, never the endpoint's address; the
server log has the details.

After 5 failures in a row the control plane stops asking for 30 seconds, then
tries one attempt; while it is not asking, attempts are held without waiting
out the timeout. A `hold` or `refuse` is the endpoint working and never counts
as a failure.

With `fail_open: true` a broken endpoint admits attempts unchanged instead.
The server logs that at boot and on every such attempt. A `hold` or `refuse`
still applies.

## Monitor it

| Metric | Meaning |
|---|---|
| `dexaflow_dispatch_admission_total{outcome}` | Attempts by outcome: `admitted`, `changed`, `held`, `refused`, `error` (held because the endpoint failed), `breaker_open` (held without asking), `fail_open` (admitted unchecked). |
| `dexaflow_dispatch_admission_duration_seconds` | How long the hook took per attempt. |

Alert on `error`, `breaker_open` and `fail_open`: each means attempts are
waiting on, or bypassing, a broken endpoint.
