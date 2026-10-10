# internal/api/

Rules for changing the HTTP API. The root [AGENTS.md](../../AGENTS.md) applies
too.

- **`/api/v2/` follows Airflow 3.2.x.** Paths, parameters and response shapes
  match what the unmodified Airflow UI expects; Dexaflow-only data goes in
  added fields or in separate routes, never in a changed Airflow field.
- **The OpenAPI spec is the source of truth.** Change `docs/api/openapi.yaml`
  first, then regenerate the client with `make pkg-client`
  (`make pkg-client-check` fails on drift). The schemas embedded in
  `internal/domain` must match `docs/api` (`TestEmbeddedSchemasMatchDocs`).
- **Never remove or change** a path, parameter or field in a patch
  ([ADR 0068](https://dexaflow.dexadata.ai/project/adrs/0068-patch-content-gated-by-safety/)).
- **Every new input is validated** and every handler is scoped to the caller's
  tenant: no read or write across tenants, roles included.
- **Errors do not leak internals:** no host paths, SQL text or stack traces in a
  response body.
