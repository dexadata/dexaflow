---
title: "ADR 0092: CI pipelines deploy with their provider's OIDC token, exchanged for a short session"
linkTitle: "0092 · CI deploys with OIDC tokens"
weight: 920
description: "ADR 0092: the control plane trusts a list of CI OIDC issuers (GitHub Actions, GitLab, Google Cloud Build), each with its audience and the one tenant it deploys to; ordered rules map claims such as the repository, the ref and the environment to a role; a verified token is exchanged for a short deploy session; `dexaflow ci init` writes this path when the cluster offers it and the deploy token path otherwise."
---

**Status:** Accepted
**Date:** 2026-10-17
**Relates:** ADR 0074 (scoped API tokens and the deployer role: the token path this one sits next to, and the scopes and deploy reads it reuses), ADR 0067 (scopes only narrow), ADR 0057 (OIDC sign-in: the tenant is never taken from a claim without a pin), ADR 0050 and #1773 (the trusted issuer, whose key cache this reuses, and `deploy_audience`), ADR 0008 (per-request reload)
**Issues:** #1882, #1708

## Context

A pipeline deploys with a deploy token (ADR 0074): a `dxf_` API token that
lives up to 365 days, 90 by default, stored as a CI secret. It works on every
CI system, but it is a long-lived secret: it is copied into the CI system by
hand, it must be rotated before it expires, and a leaked copy deploys until
someone notices and revokes it.

The CI systems teams use already prove who a job is. GitHub Actions, GitLab
CI and Google Cloud Build each mint a short-lived OIDC token for the running
job, signed by the provider and naming the repository, the ref, the
environment or the service account. Cloud providers accept these tokens in
place of stored keys (workload identity federation), and the pipelines that
`dexaflow ci init` writes already use them to push images to Google Cloud and
AWS. The control plane accepts none of them: `auth.trusted_issuer` verifies
tokens of one platform issuer for users linked to it (#1284, #1468, #1773),
and its tenant comes from a claim the platform controls, which a CI provider's
token cannot be trusted with. Anyone can create a repository on GitHub and
mint a token for any audience, so a claim can say anything its author wants
except who the provider says the job is.

## Decision

### 1. Configuration

The operator lists the CI issuers the control plane trusts under
`auth.ci_oidc`, in the server configuration file or, as JSON, in
`DEXAFLOW_AUTH_CI_OIDC_ISSUERS` (Helm: `auth.ciOidc.issuers`):

```yaml
auth:
  ci_oidc:
    session_ttl_seconds: 3600
    issuers:
      - name: github-acme
        provider: github
        issuer: https://token.actions.githubusercontent.com
        audience: https://dexaflow.acme.example
        tenant: acme
        rules:
          - claims: {repository: acme/dags, ref: refs/heads/main, environment: production}
            role: deployer
```

- **An entry** has a `name` (1 to 40 lowercase letters, digits or `-`,
  unique), a `provider` (`github`, `gitlab`, `cloudbuild` or `other`), the
  exact `issuer` URL, an optional `jwks_url`, the `audience` its tokens must
  carry, the one `tenant` it deploys to, an optional `max_lifetime_seconds`
  and at least one rule.
- **A rule** names one or more `claims`, each with a pattern, a `role` of the
  tenant, and optional `scopes` (default `dexaflow:deploy`).
- **Nothing is on by default.** With no entry the exchange route is not
  registered and nothing changes (ADR 0068, points 1 and 2).
- **Invalid values fail at boot**, naming the key: an issuer or JWKS URL that
  is not `https` (or `http` on a loopback host), an empty audience or tenant,
  two entries with the same name or the same issuer and audience, an entry
  without rules, a rule without claims, a pattern that does not parse or
  matches everything, a rule naming the `admin` role, an unknown scope, and a
  rule of a `github`, `gitlab` or `cloudbuild` entry that pins no identity
  claim (`repository`, `repository_id`, `repository_owner` or
  `repository_owner_id` on GitHub; `project_path`, `project_id`,
  `namespace_path` or `namespace_id` on GitLab; `email` or `sub` on Cloud
  Build). Without that pin, every repository on the provider could deploy.

### 2. Verifying a token

The token is checked against the one entry whose `issuer` equals its `iss`
and whose `audience` is in its `aud`. These two are read from the unverified
payload first, so a token that names no configured entry costs no key
download and no signature check. Then:

- **Algorithm.** Only RS256, ES256 and PS256 are accepted; `none` and the
  HMAC algorithms are refused before any key is fetched.
- **Keys.** The signature must verify with a key of the entry's JWKS: from
  `jwks_url`, or discovered from `<issuer>/.well-known/openid-configuration`,
  whose `issuer` must equal the entry's and whose `jwks_uri` must be `https`.
  Discovery and keys are fetched on first use, never at boot, so a provider
  outage cannot stop the server. Keys are cached and downloaded again when a
  token names an unknown key id, at most once every 30 seconds (the trusted
  issuer's cache, ADR 0050). A provider whose keys cannot be fetched answers
  `503`, not `401`.
- **Claims.** `iss` equals the entry's issuer and `aud` contains its audience.
  `exp`, `iat` and `sub` are required; the token must not be expired, `nbf`
  (when present) must have passed and `iat` must not be in the future, each
  with 60 seconds of clock skew. `exp - iat` must not exceed the entry's
  `max_lifetime_seconds` (3600 by default, at most 86400), so a long-lived
  token minted elsewhere for the same audience is refused.

### 3. The tenant comes from the entry, never from a claim

The principal's tenant is the entry's `tenant`. No claim is read to choose
it, so a token carrying `tenant_id`, `tenant` or any other claim cannot move
the principal to another tenant. A provider is shared by many tenants (every
GitHub repository has the same issuer), so the audience tells their entries
apart and the rules decide who is admitted: two entries may share an issuer
but not an issuer and an audience.

### 4. Claims map to a role: first matching rule, or deny

- Rules are tried in order. A rule matches when every claim it names is
  present in the token as a string and matches its pattern (Go's
  `path.Match`: `*` does not cross `/`, so `refs/heads/*` does not match
  `refs/heads/a/b`). A claim that is absent, or not a string, does not match.
- The first rule that matches decides. When none matches, the token is
  refused with `403`, whatever else it carries.
- The principal gets the rule's role and nothing else: no other role, no
  default role, and never `admin`. Its permissions are that role's grants in
  the entry's tenant, read from the database. A role that does not exist in
  the tenant refuses the token.
- The principal is scoped (ADR 0067) to the rule's scopes, `dexaflow:deploy`
  by default, so it reaches the deploy routes and the reads a deploy makes
  (ADR 0074) and nothing else, even when the role allows more. The role
  narrows the scopes too: the gate checks both.

### 5. The exchange and the session

A CI token lives minutes (five on GitHub Actions) and a deploy that builds
images can take longer, so the token is exchanged once:

- `POST /api/v2/auth/ciToken` with `{"token": "<the provider's token>"}`, a
  public route (it authenticates its caller itself) registered only when an
  entry is configured, answers `200` with a session token, its type
  (`Bearer`), its lifetime, the tenant, the role and the scopes. The body is
  bounded to 16 KiB and unknown fields are refused. A token that does not
  verify answers `401`, one that matches no rule or names a missing role
  `403`, and a key or database outage `503`. Failed exchanges count against
  a per-IP limit of 30 a minute.
- **The session token** starts with `dxci_`, followed by an HS256 JWT signed
  with the engine secret for the audience `dexaflow-ci`. It names the entry,
  a fingerprint of the entry's configuration, the tenant, the role, the
  scopes and the CI subject (`sub`), and lives `session_ttl_seconds` (3600 by
  default, 300 to 43200). It is not renewed: a later job exchanges again.
- **On every request** the session is checked like an API token: the
  signature, the expiry, the entry still configured with the same
  fingerprint (so changing or removing an entry ends its sessions at the next
  restart), and the role's grants read again from the tenant, so removing a
  permission from the role or deleting the role takes effect at once.
- **Header only.** A `dxci_` value in the session cookie is ignored, as an
  API token is, so a CI session never becomes a browser session. Being
  scoped, it cannot reach `/api/v2/apiTokens`, so it cannot mint a longer
  credential.
- With `auth.trusted_issuer.deploy_audience` set (#1773), deploys go through
  the platform issuer only, and a CI session is refused there like any other
  credential.
- The principal's id is `ci:<entry name>`, which no user has, so a version it
  registers carries no `created_by` user; the exchange is audited as
  `ci_oidc.exchange` in the tenant, with the entry name, the subject, the
  role and the outcome.

### 6. How `dexaflow ci init` chooses

`GET /api/v2/deployDefaults` gains a `ci_oidc` field: the entries of the
caller's tenant, each with its name, provider, issuer and audience, or null
when there is none. It is filtered by tenant so a user never learns another
tenant's entries; the rest of the answer stays the same for every tenant.

`dexaflow ci init <system> --auth auto|oidc|token` (default `auto`) reads it
from the target the pipeline deploys to, with the session the CLI holds for
that target:

- `auto` writes the OIDC path when the cluster lists an entry for the system
  (`github`, `gitlab` or `cloudbuild`) and the pinned CLI release can
  exchange, and the deploy token path otherwise: no server chosen, not signed
  in, a server that predates the field, an unreachable server, no entry for
  the system, or `generic`. It says which path it wrote and why.
- `oidc` fails instead of falling back. `token` never asks the server.
- The OIDC pipeline obtains the provider's token for the entry's audience
  (GitHub's `ACTIONS_ID_TOKEN_REQUEST_URL` with `id-token: write`, GitLab's
  `id_tokens`, `gcloud auth print-identity-token` on Cloud Build), passes it
  as `DEXAFLOW_OIDC_TOKEN`, and the deploy script exchanges it with
  `dexaflow auth ci-token`, which prints the session token for
  `DEXAFLOW_TOKEN`. No secret is stored in the CI system.

### 7. Observability

- `dexaflow_ci_oidc_exchange_total{issuer, outcome}` counts exchanges by
  entry name (`unknown` when no entry matched) and outcome: `issued`,
  `invalid_token`, `unknown_issuer`, `no_rule`, `role_missing`,
  `keys_unavailable`, `error`.
- Each refusal and each issued session is logged with `slog` with the entry,
  the tenant, the outcome, the reason, the role and the token's `sub`. No log
  line, audit record or error body carries the token or its signature. The
  reason of a refusal may quote the token's `iss` or `aud` (for example an
  unknown issuer), which name the provider and the control plane, not the
  job; no other claim is logged.

## Alternatives considered

- **The provider's token as the bearer of every request.** No exchange and
  no session format, but GitHub's token lives five minutes and a deploy with
  an image build outlives it, and every request would cost a signature check
  against a remote key set.
- **Taking the tenant from a claim**, as `auth.trusted_issuer` does. A CI
  provider's claims are written by whoever owns the repository, so the tenant
  would be chosen by the caller. Pinning it in the entry keeps the operator
  the only one who decides.
- **Linking the token to a service user** and acting as that user. It would
  reuse the per-user reload, but it creates or requires users for pipelines
  and grants whatever roles that user gains later. A role named by the rule,
  read from the tenant, gives the same least privilege without a user row.
- **Mapping claims to scopes only**, with no role. Scopes narrow a role and
  never grant (ADR 0067); without a role there is nothing to narrow.
- **Advertising the entries on a public route.** `ci init` could then decide
  without a session, but the entries name tenants and audiences, which other
  tenants have no reason to read.
- **Requiring `jwks_url`.** Simpler, but GitHub, GitLab and Google publish
  discovery documents, and following them keeps a key URL change on the
  provider's side from breaking deploys.

## Consequences

- A pipeline written by `ci init` for a cluster that offers OIDC stores no
  credential, and nothing expires or needs rotating. The deploy token path
  stays for every other CI system and every cluster that does not offer it.
- The operator must write rules that pin the repository (or project, or
  service account), the ref and, where the provider has one, the protected
  environment. The boot check refuses a rule without an identity claim, but
  only the operator knows which refs may deploy. Pinning `repository_id` and
  `repository_owner_id` survives a rename on GitHub; `repository` does not.
- A leaked CI token is useful for at most its own lifetime and only for the
  rule it matches; a leaked session for `session_ttl_seconds`. There is no
  replay check (`jti`): a token may be exchanged more than once while it is
  valid, which only gives its holder more sessions of the same role.
- New surface: a public route, a new credential form (`dxci_`), a new
  setting, a counter, a `deployDefaults` field, a CLI command
  (`dexaflow auth ci-token`) and a `ci init` flag. No migration: the role
  lookup is a read of existing tables.
- Release candidate review 6 (tenant isolation) covers the exchange, the
  session and the `deployDefaults` filter: no claim chooses a tenant, and the
  role is looked up in the entry's tenant only.
