---
title: Add or change an /api/v2/ endpoint
linkTitle: Add an API endpoint
weight: 50
description: Change the public HTTP API without breaking the Airflow UI, starting from the OpenAPI document and ending with a guarded, tenant-scoped handler.
---

The rules are in [`internal/api/AGENTS.md`](https://github.com/dexadata/dexaflow/blob/main/internal/api/AGENTS.md).
`/api/v2/` follows Airflow 3.2.x, so read the
[Airflow compatibility checklist](/contribute/review-checklists/airflow-compatibility/)
before you design the change.

1. **Start from the OpenAPI document.** Edit `docs/api/openapi.yaml`, then copy
   it to `internal/api/openapi.yaml`, which the server embeds
   (`TestEmbeddedOpenAPIMatchesDocs` fails while they differ). An Airflow path
   keeps Airflow's parameters and response fields; Dexaflow-only data goes in an
   added field or a separate route.
2. **Regenerate the client:** `make pkg-client` (`make pkg-client-check` fails
   on drift, and so does CI).
3. **Write the handler test first** in `internal/api/`, including the
   responses for a caller without the permission and for a caller of another
   tenant.
4. **Register the route behind a gate:** `RequirePermission`,
   `RequireScopedPermission` for a write a scoped token may call, or
   `RequireScope`. `TestScopeGateClassifiesEveryRoute` walks every route and
   fails a `GET` with no permission check and a scoped write missing from
   `scopedWrites`.
5. **Scope every read and write to the caller's tenant** with `tenantOf(c)` and
   a storage query that checks `tenant_id`
   ([`internal/storage/AGENTS.md`](https://github.com/dexadata/dexaflow/blob/main/internal/storage/AGENTS.md)).
6. **Validate every input** and answer errors with `AbortProblem`, keeping SQL
   text, host paths and stack traces out of the body (the leak scans in
   `repo_error_leak_test.go` and `leakscan_test.go`).
7. **Check the UI.** When the embedded Airflow UI reads the route, run
   `test/ui-contract/run.sh` against a local control plane, and add a
   parameterless path the UI fetches on screen load to
   `TestSPAReferencedRoutesAreRegistered`.
8. **Document it.** The OpenAPI document is user-facing surface for the docs
   guard: update the [API reference](/reference/api/) or the page of the
   feature, and record a changelog fragment
   ([record a change](/contribute/recipes/record-a-change/)). In a patch, a
   path, parameter or field is never removed or changed
   ([ADR 0068](/project/adrs/0068-patch-content-gated-by-safety/)).
9. **Check:** `go test ./internal/api/ ./internal/domain/` and `make lint test`.
