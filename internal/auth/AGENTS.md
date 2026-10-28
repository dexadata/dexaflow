# internal/auth/

Rules for changing authentication, roles and tenant isolation. The root
[AGENTS.md](../../AGENTS.md) applies too. There is no `internal/tenant`
package: the tenant rules below also cover the code that carries a tenant in
`internal/api`, `internal/storage`, `internal/oidc`, `internal/issuer` and
`migrations/`.

## Who may change it

CONTRIBUTING.md ("Security-Sensitive Changes") does not accept changes to
`internal/auth/`, `internal/executor/`, `internal/storage/`, `migrations/`,
`proto/`, or anything that handles credentials or secrets, from first-time
contributors: open a discussion issue first. The PR template asks every PR
that touches these paths to say so. A vulnerability is reported privately,
never in an issue or a PR ([`SECURITY.md`](../../SECURITY.md)).

## Rules

- **No tenant means no data.** A token that names no tenant is refused
  (`ErrTenantlessToken`, `tenantless_token_test.go`), and a request that
  reaches a handler without a tenant gets the empty name, which no tenant has,
  so every lookup misses instead of falling back to `default`
  (`tenantOf` in `internal/api/resources.go`,
  `internal/api/tenant_fail_closed_test.go`).
- **Isolation holds for roles too.** No read or write crosses tenants, role
  assignments included (review 6 in the root AGENTS.md). Storage queries carry
  the tenant predicate ([`internal/storage/AGENTS.md`](../storage/AGENTS.md)),
  and a migration that changes a built-in role changes it in every tenant
  ([`migrations/AGENTS.md`](../../migrations/AGENTS.md)).
- **Every protected route is gated.** It is guarded with `RequirePermission`,
  `RequireScopedPermission`, or `RequireScope` when any signed-in user may call
  it (`internal/api/middleware.go`). `TestScopeGateClassifiesEveryRoute`
  walks every registered route: a `GET` without a permission check, or a write
  that a scoped token can reach without being listed in `scopedWrites`, fails
  it
  ([ADR 0067](https://dexaflow.dexadata.ai/project/adrs/0067-mcp-run-control-scopes-source-mode/)).
- **A scope only narrows.** A scoped token never gets more than its user's
  roles allow, and no scope implies another (`HasScope`, `scope_test.go`).
- **API tokens are stored as a hash and never manage tokens.** Only the
  SHA-256 of a `dxf_` token is kept (`api_tokens.token_hash`); the token is
  shown once and never logged or audited. A token runs as its owner, reloaded
  on every request and narrowed to its scopes, and the `/api/v2/apiTokens`
  routes refuse every scoped principal, so a token cannot mint or revoke
  tokens
  ([ADR 0074](https://dexaflow.dexadata.ai/project/adrs/0074-scoped-api-tokens/)).
- **Sign-in fails closed.** OIDC pins the tenant from a configured claim and
  never falls back to `default`
  ([ADR 0057](https://dexaflow.dexadata.ai/project/adrs/0057-oidc-sso/)); a
  trusted issuer only opens a session for an existing user in an allowed
  tenant, and never creates users or grants roles (`internal/issuer`).
- **The agent's token is scoped to one attempt.** It names the task instance,
  try and attempt epoch, and storage refuses a call whose token names another
  tenant (`internal/storage/tenant_predicates_agent_integration_test.go`).
- **The browser flows are tested in a browser.** A change to sign-in, login or
  SSO runs the Playwright flows for its release candidate (review 5 in the root
  AGENTS.md: `make e2e-sso`, `test/e2e/ui-smoke.js`).
