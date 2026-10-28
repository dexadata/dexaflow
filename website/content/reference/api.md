---
title: HTTP API (Scalar)
linkTitle: HTTP API
weight: 10
description: The /api/v2/ control-plane API, Airflow 3.2.x-compatible, as an interactive Scalar reference generated from the OpenAPI spec.
---

Dexaflow's control-plane API is the `/api/v2/` surface, pinned **Airflow
3.2.x-compatible**. It is documented from the OpenAPI spec (`openapi.yaml`) and
rendered with [Scalar](https://github.com/scalar/scalar).

{{% alert title="Open the interactive reference" color="primary" %}}
The full interactive API reference (search, per-endpoint schemas, request/response
examples) is served as a standalone page:

**[→ Open the HTTP API reference](/api-reference.html)**
{{% /alert %}}

The static reference above hides the "Send" button (there is no live server behind
the docs). A running control plane serves its own Scalar at `/docs` with the test
button enabled, so you can exercise the API against your own instance.

## Where the spec lives

The OpenAPI document is generated from the Go handler annotations on every push and
published alongside the site as [`openapi.yaml`](/openapi.yaml). Point any
OpenAPI-aware client (curl-with-schemas, Postman, an SDK generator) at it.

See [ADR 0013](/project/adrs/0013-scalar-api-docs/) for why the API reference is
Scalar embedded in the server binary.

## Setting a run's state

`PATCH /api/v2/dags/{dag_id}/dagRuns/{dag_run_id}` with a `state` sets a run
to `queued`, `success` or `failed`, as Airflow 3.2 does. Only the scheduler
starts a run, under the DAG's pause and `max_active_runs`: a PATCH to
`running`, which Dexaflow 0.5.3 accepted, still answers 200 but stores
`queued`, and the response shows `queued`; the audit entry
(`dagrun.mark.queued`) records `requested_state: running`. On a run that is
already `running`, a PATCH to `running` changes nothing and answers with the
run as it is. Setting `queued` also clears the run's start date, so the
scheduler stamps a new one when it starts the run.

## Clearing on the latest version

`POST /api/v2/dags/{dag_id}/clearTaskInstances` and
`POST /api/v2/dags/{dag_id}/dagRuns/{dag_run_id}/clear` take
`run_on_latest_version`, as Airflow 3.2 does. With `true`, a finished run
(`success` or `failed`, no task instance `queued` or `running`) moves to the
DAG's current version: the clear creates the task instances of the tasks that
version adds and marks `removed` the ones of the tasks it dropped. A run that
is still running keeps its DAG version, and the cleared tasks re-run on that
version. Both routes, dry run included, then add the Dexaflow field
`run_on_latest_version_not_applied`, one entry per such run with its
`dag_run_id` and the `reason`; the field is left out when every run moved or
the option was not asked:

```json
"run_on_latest_version_not_applied": [
  {"dag_run_id": "manual__2026-10-16T09:00:00+00:00",
   "reason": "the run is running: only a finished run moves to the current version"}
]
```

To move such a run, wait for it to finish (or mark it failed or success and
let its running tasks end), then clear again. [Clearing a run that is still
running](/author-dags/dag-authoring/#clearing-a-run-that-is-still-running) has
the steps and an example. An unknown `dag_run_id` answers `404` before anything
is cleared.

## Paging

List endpoints page with `limit` and `offset`, as Airflow 3.2 does, and keep
the Airflow response body. An `offset` page costs more the deeper it is,
because the database reads and discards every row before it. Two endpoints
also take a cursor, a Dexaflow extension that makes every page cost the same
as the first:

- `GET /api/v2/dags/{dag_id}/dagRuns`
- `GET /api/v2/eventLogs`

Every page that has a successor names the next page's cursor in the
`Dexaflow-Next-Cursor` response header, offset pages included, so a client can
switch to cursors after its first request. Pass it back as the `cursor` query
parameter, with the same `limit` and filters; `offset` is ignored when a
cursor is set. The cursor is opaque: pass it back as you received it. A
malformed one is answered with `400`.

```sh
curl -sS -D headers.txt -H "Authorization: Bearer $TOKEN" \
  "$DEXAFLOW_URL/api/v2/dags/my_pipeline/dagRuns?limit=100" > page1.json
next=$(awk -F': ' 'tolower($1)=="dexaflow-next-cursor"{print $2}' headers.txt | tr -d '\r')
curl -sS -H "Authorization: Bearer $TOKEN" \
  "$DEXAFLOW_URL/api/v2/dags/my_pipeline/dagRuns?limit=100&cursor=$next" > page2.json
```

Runs that share a logical date are ordered by run id, so both modes return
stable pages, and both count every run that matches the filters. A cursor
page does not count again: it reports the `total_entries` of the page that
issued its cursor, so the total holds still while you page, and rows added
since show on the next walk from the first page. Browsers on
an allowed CORS origin can read the `Dexaflow-Next-Cursor` and `Link` headers.
