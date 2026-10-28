---
title: "ADR 0074: CI deploys with named, scoped, revocable API tokens and a built-in deployer role"
linkTitle: "0074 · Scoped API tokens and the deployer role"
weight: 740
description: "ADR 0074: long-lived API tokens owned by a user, stored as a SHA-256 hash, narrowed by the ADR 0067 scopes, expiring and revocable; a deploy:dag permission and a built-in deployer role that registers versions and rolls back, and cannot delete."
---

**Status:** Accepted
**Date:** 2026-10-13
**Relates:** ADR 0008 (JWT authentication; this adds a second credential next to the session JWT, under the same per-request reload), ADR 0067 (the scopes these tokens carry, and the rule that a scope only narrows; this amends its scope table so `dexaflow:deploy` also opens the reads a deploy makes), ADR 0041 (pipeline-less deploy, which CI runs)
**Issues:** #1708 (CI/CD credentials and roles)

## Context

A CI pipeline that runs `dexaflow deploy` needs a control-plane credential, and
today it has none that fits:

- The only engine credential is the session JWT. It lives an hour
  (`auth.jwt.token_ttl_seconds`, 3600) and renewal is capped by
  `auth.jwt.max_lifetime_seconds`, so a token stored as a CI secret stops
  working the same afternoon. The documented workaround stores the service
  user's **password** in CI and signs in at the start of every job, which puts
  a password in every pipeline and every job log that leaks it.
- There is no way to name, list or revoke a credential given to a pipeline.
  Revoking one means deactivating its user, which revokes every pipeline that
  shares it.
- Registering a version needs `write:dag`, which also deletes DAGs, edits the
  IDE workspace and pauses DAGs (#1708). No built-in role grants deploying and
  nothing else.

ADR 0067 already defines scopes that narrow a token (`dexaflow:read`,
`dexaflow:run`, `dexaflow:deploy`) and a route gate that enforces them, but
only for tokens from a trusted issuer. CI OIDC with trusted issuers is the
longer-term answer and a separate change; this ADR covers what every
installation can use without an external issuer.

## Decision

### 1. API tokens

An **API token** is a long-lived bearer credential owned by one user of one
tenant.

- **Form.** `dxf_` followed by 32 random bytes in unpadded base64url
  (43 characters, 256 bits from `crypto/rand`). The prefix tells it apart
  from a JWT without parsing, and lets secret scanners match it.
- **Storage.** The server stores the hex SHA-256 of the whole token in
  `api_tokens.token_hash` (unique) and never the token. A fast hash is
  enough because the secret is random and long: there is no dictionary to
  try and no salt is needed. The hash is the lookup key, so authentication
  is one indexed equality lookup.
- **Shown once.** The create response is the only place the token appears.
  Nothing logs it, and the audit records name the token by id and name.
- **Owner and tenant.** A token belongs to the user who created it, in that
  user's tenant. A user creates tokens only for itself.
- **Authentication.** A bearer starting with `dxf_` is resolved by its hash.
  It is refused (`401`) when it is unknown, revoked or expired, when its
  owner no longer exists or is inactive, or when the owner's tenant is not
  the token's. Otherwise the request runs as the owner, with the owner's
  roles and permissions reloaded from the database on every request (as for
  a JWT, ADR 0008), marked `Scoped` with the token's scopes. A database
  failure during the lookup answers `503`, never `401`, like the JWT path.
- **Scopes.** A token is created for a named scope set: `deploy` carries
  `dexaflow:deploy` alone, `read` carries `dexaflow:read`. The ADR 0067 gate
  then applies: the role check runs first, the scope check second, both must
  pass, and a write no scope maps is refused. A deploy token does not carry
  `dexaflow:read`, which would open every read its owner has (variables,
  connection metadata, logs, XComs, users, the event log). Instead the reads
  a deploy makes accept `dexaflow:deploy` as well as `dexaflow:read`: the DAG
  list, a DAG, its details and its versions
  (`GET /api/v2/dags`, `/api/v2/dags/{id}`, `/api/v2/dags/{id}/details`,
  `/api/v2/dags/{id}/dagVersions` and `/dagVersions/{n}`). Every other read
  is refused to it with `403` naming `dexaflow:read`. This amends ADR 0067's
  scope table for trusted-issuer tokens too: a token with `dexaflow:deploy`
  now also reads those DAG routes. The
  creator must hold what the scope does at creation (`deploy` needs
  `deploy:dag` or `write:dag`, `read` needs `read:dag`), so a token never
  names a scope its owner could not use; at use time the owner's current
  roles decide.
- **Tokens do not manage tokens.** The token routes check no role permission
  and no scope grants them, so every scoped principal, an API token included,
  is refused there. A leaked token cannot mint a longer-lived one or revoke
  the owner's other tokens.
- **Lifetime.** Every token expires: 90 days by default, from one hour to 365
  days. There is no non-expiring token.
- **Last use.** The time a token last authenticated a request is recorded, at
  most once every five minutes per token, so a busy pipeline does not write
  on every request. The throttle is the `UPDATE`'s own predicate
  (`last_used_at < $at - interval '5 minutes'`), so concurrent requests
  write once and an older time never moves it back. A failed write is logged
  and does not fail the request.
- **Revocation.** Revoking sets `revoked_at` and keeps the row, so the audit
  trail can still name the token. Revoking a revoked token is not found
  (`404`), so it is never revoked or audited twice. A revoked token is refused on its next
  request. Deactivating or deleting the owner stops all of its tokens.
- **Limits.** Names are 1 to 64 ASCII letters, digits, dots, dashes or
  underscores, starting with a letter or digit, unique among one owner's
  unrevoked tokens; creating a token revokes the owner's expired tokens of
  the same name first, in the same transaction, so an expired token does not
  hold its name. A user holds at most 50 live tokens. The create transaction
  locks the owner's `users` row (`FOR NO KEY UPDATE`) before it counts, so
  concurrent creates cannot pass the cap together. The create body is
  bounded to 4 KiB and unknown fields are refused.
- **Who sees what.** A user lists and revokes its own tokens. A user who
  manages the tenant's accounts (`write:user`, which the admin role has) lists
  and revokes every token of the tenant. No list or revoke crosses tenants.
- **Audit.** `api_token.create` (with the scopes and the expiry) and
  `api_token.revoke` are recorded with the acting user, the token's id, its
  name and its owner's id.
- **Header only.** An API token authenticates only from the `Authorization`
  header. A `dxf_` value in the session cookie is ignored, so a token never
  becomes a browser session.

The API is `POST /api/v2/apiTokens`, `GET /api/v2/apiTokens` and
`DELETE /api/v2/apiTokens/{token_id}`, Dexaflow-only routes outside the Airflow
surface. The CLI is `dexaflow auth create-token --scope <deploy|read> --name
<name> [--ttl 90d]` (without `--scope` the command keeps printing a session
JWT), `dexaflow auth tokens list` and `dexaflow auth tokens revoke <id>`.

### 2. The deploy:dag permission and the deployer role

- A new permission, `deploy:dag`, allows registering a DAG version
  (`POST /api/v2/dags/{id}/versions`) and setting the current version
  (`PUT /api/v2/dags/{id}/currentVersion`).
- Both routes accept `deploy:dag` **or** `write:dag`, so every role that could
  deploy before, custom roles included, still can. No grant of an existing
  role changes.
- A built-in role, `deployer`, grants `read:dag` and `deploy:dag`, nothing
  else. It is created in every tenant (migration `044`), joined on
  `roles.is_system`, unassigned. A tenant that already has a custom role named
  `deployer` keeps it untouched. Tenants created later copy it from `default`
  like every built-in role.

## Alternatives considered

- **Long-lived JWTs.** Signing a JWT with a long `exp` needs no table, but it
  cannot be listed or revoked one at a time (only by deactivating the user or
  rotating the secret for everyone) and its last use is unknown.
- **bcrypt or argon2 for the hash.** A slow hash protects low-entropy
  passwords. A 256-bit random secret does not need it, and a slow hash would
  run on every request and cannot be the lookup key.
- **Tokens as their own principals (service accounts) with their own roles.**
  More flexible, but it adds a second kind of principal to every permission
  check, audit record and UI. A service user with the deployer role plus a
  scoped token gives the same least privilege with the principal model that
  exists.
- **Narrowing `write:dag`** so it no longer deletes. It would silently change
  what existing custom roles grant. A new, narrower permission that the two
  deploy routes also accept changes no existing grant.
- **No expiry.** Rejected: a forgotten CI secret would stay valid forever.

## Consequences

- CI stores one revocable secret per pipeline instead of a password, and the
  CI/CD guide (`operate/cicd-deploy`) shows that pattern.
- A leaked deploy token can register versions, roll back, and read the DAGs
  and versions of its owner's tenant until it expires or is revoked. It
  cannot delete, pause or trigger, cannot read variables, connections, logs,
  XComs, runs, users or the event log, and cannot create tokens. A `read`
  token reads everything its owner can read; give CI a `deploy` token.
- Every request with an API token costs one indexed lookup and the user
  reload a JWT already costs, plus at most one write every five minutes per
  token.
- The UI does not list or revoke tokens yet; that is a follow-up over the same
  API.

  > **Note (2026-10, #1886).** The UI now lists and revokes tokens at
  > **User > API tokens**, over the same API and checks; see
  > [CI/CD deploy](/operate/cicd-deploy/) and
  > [UI compatibility](/concepts/ui-compatibility/#dexaflow-screens-inside-the-ui).

- Migration `044` adds `api_tokens`, the permission and the role. Its down
  drops the table, which revokes every token, and removes the built-in role.
- CI OIDC with trusted issuers (#1708) may later replace stored tokens for
  systems that support it; it would map to the same deployer role and scopes.
