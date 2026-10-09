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
stable pages. With a `state` filter the two modes can report different
`total_entries` for a DAG with more than 10000 runs: offset paging filters and
counts the newest 10000 runs, cursor paging every run of the DAG. Browsers on
an allowed CORS origin can read the `Dexaflow-Next-Cursor` and `Link` headers.
