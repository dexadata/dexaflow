---
title: Trigger a run from an external system
linkTitle: External trigger (REST)
weight: 55
description: Obtain a token, trigger a DAG run with conf over the REST API, and poll it — the raw-HTTP path for an external integrator.
---

Any external system can start a DAG run over the same `/api/v2` REST surface the
CLI and UI use — no CLI install, no UI dependency. The flow is three calls:
**obtain a token → trigger with `conf` → poll the run**. The API is Airflow
3.2.x-shaped, so an existing Airflow REST integration ports with minimal change.

{{% alert title="Interactive reference" color="info" %}}
Every endpoint below, with request/response schemas and a live **Send** button,
is in the [HTTP API (Scalar) reference](/reference/api/). A running control plane
also serves it at `/docs`.
{{% /alert %}}

## 1. Obtain a JWT

Exchange credentials for a bearer token ([ADR 0008](/project/adrs/0008-jwt-auth/)).
Use a dedicated service account with only the `execute:dag` permission it needs.

```bash
TOKEN=$(curl -sS -X POST https://leoflow.example.com/auth/token \
  -H 'Content-Type: application/json' \
  -d '{"username":"ci-bot","password":"'"$DEXAFLOW_PASSWORD"'"}' \
  | jq -r .access_token)
```

The response is `{"access_token": "...", "token_type": "bearer", "expires_in": 3600}`.
Pass the token as `Authorization: Bearer $TOKEN` on every subsequent call. Tokens
expire (default 1h); re-fetch, or renew via `POST /api/v2/auth/token/renew`.

## 2. Trigger a run with conf

`POST /api/v2/dags/{dag_id}/dagRuns`. The `conf` object becomes the run's
parameters — reachable in tasks as `{{ params.x }}` / `context["params"]`. `conf`
must be a JSON **object**; an array or scalar is rejected with `400`.

```bash
curl -sS -X POST https://leoflow.example.com/api/v2/dags/sales/dagRuns \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"conf": {"date": "2026-01-01", "limit": 100}}'
```

Optional fields: `dag_run_id` (your own idempotency key — see below), and `note`.
The response carries the created run, including its `dag_run_id` and `state`:

```json
{"dag_run_id": "manual__2026-01-01T00:00:00Z", "state": "queued", "conf": {"date": "2026-01-01", "limit": 100}, "...": "..."}
```

{{% alert title="Declared params are validated and defaulted" color="info" %}}
If the DAG declares `params=` (Airflow's typed params), the trigger merges your
`conf` over the declared defaults (your value wins per key) and validates each
declared value against its schema — a type or constraint violation is a `400`.
Keys you send that the DAG does not declare are carried through unchanged. A DAG
that declares no params accepts any object `conf`.
{{% /alert %}}

## 3. Poll the run

Capture the `dag_run_id` from step 2 and poll until the run reaches a terminal
state (`success` / `failed`):

```bash
RUN_ID=$(... # the dag_run_id from step 2)
curl -sS https://leoflow.example.com/api/v2/dags/sales/dagRuns/"$RUN_ID" \
  -H "Authorization: Bearer $TOKEN" | jq -r .state
```

To inspect individual tasks, `GET
/api/v2/dags/{dag_id}/dagRuns/{dag_run_id}/taskInstances`.

## Idempotency

Supply your own `dag_run_id` to make a retry safe: a duplicate `(dag_id,
run_id)` is rejected (a run id is unique per DAG), so re-sending the same request
after a network blip does not create a second run. Omit it and the control plane
assigns an `manual__<timestamp>` id.

## See also

- [HTTP API (Scalar) reference](/reference/api/) — every endpoint, interactively.
- [CI/CD deploy](/operate/cicd-deploy/) — the `dexaflow push` + `DEXAFLOW_TOKEN`
  path when you control the DAG source (vs. only triggering an existing DAG).
- [ADR 0008 — JWT auth](/project/adrs/0008-jwt-auth/).
