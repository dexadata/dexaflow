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
