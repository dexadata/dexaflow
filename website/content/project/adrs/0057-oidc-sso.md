---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /adr/0057-oidc-sso.html
# --- end AUTO redirect aliases ---
title: "ADR 0057: OIDC/SSO authentication with fail-closed tenant pinning"
linkTitle: 0057 · OIDC/SSO authentication with fail-closed tenant pinning
weight: 570
description: "ADR 0057: OIDC/SSO authentication with fail-closed tenant pinning"
---

**Status:** Accepted
**Date:** 2026-08-19
**Relates:** ADR 0008 (JWT auth — the `Authenticator` seam OIDC plugs into; the `_token` this flow mints and the per-request reload that governs live permissions), ADR 0035 (keyless-first — the verify path uses the issuer's public JWKS, no secret), ADR 0055 (token liveness/revocation — bounds the OIDC session lifetime), ADR 0014 (supply chain — the new `go-oidc` dependency), ADR 0049 (edition gating precedent), ADR 0018 (embedded login UI the `_token` cookie drives)

## Context

Leoflow authenticates with HS256 JWTs (`internal/auth/jwt.go`): a
username/password `POST /auth/token` mints a token carrying
`{tenant_id, email, roles}`, and the middleware accepts it as a bearer or the
`_token` cookie. The `Authenticator` interface was documented from the start as
keeping OIDC/LDAP pluggable, and the `users` table already carries
`oidc_subject`/`oidc_provider` (unique) with a nullable `password_hash` — the
schema anticipated SSO, but no code used those columns and `auth.provider: oidc`
booted closed as unimplemented.

The enterprise pivot needs SSO against Entra ID / Google Cloud Identity / Okta,
with **fail-closed** mapping of external identities to Leoflow tenants and roles.
OIDC is a **login flow**, not a new request-path authenticator: the callback
verifies the IdP's ID token and mints Leoflow's own `_token`, so the middleware,
`/ui/auth/*`, and the API are unchanged. The per-request reload from ADR 0008's
prerequisite work (roles/permissions/`is_active` reloaded on every verify)
remains the authority for live permissions and gives revocation-within-TTL.

An identity/zero-trust specialist review of the design flagged five HIGH issues;
all five are folded into the decisions below (H1→D6, H2→D8, H3→relies on the
role-ladder prerequisite, H4→D10, H5→the audit decision).

## Decisions

**D1. Flow = Authorization Code + PKCE.** Endpoints `GET /api/v2/auth/oidc/login`
and `GET /api/v2/auth/oidc/callback`, registered under the existing public
`/api/v2/auth/` prefix. The `state`, `nonce`, and PKCE `code_verifier` are carried
in a short-lived, signed, `HttpOnly; Secure; SameSite=Lax` cookie (stateless — no
new session store). The cookie is signed with a key **derived** from the app's
HS256 secret, distinct from the session-token key, so a state cookie can never
cross-verify as a session token. Implicit flow is rejected.

**D2. Session = the `_token` JWT cookie.** On success the callback mints
Leoflow's own `_token` via `MintUserToken` and sets it **server-side**
`HttpOnly; Secure; SameSite=Lax` (hardened vs the credential login page's
client-JS cookie). The OIDC session length equals the JWT TTL; mid-session
revocation is the per-request `is_active` reload (revocation-within-TTL). The
post-login redirect reuses `sanitizeNext` (open-redirect safe).

**D3. Identity match = `(oidc_provider, oidc_subject)`, email only as a verified
link key.** `FindUserByOIDCSubject` resolves a returning user by the immutable
pair (loading roles + permissions + `is_active`, mirroring the credential path).
`oidc_provider` is the pinned **issuer URL** — stable and unambiguous per
deployment. `email` is trusted only when `email_verified == true` (absent ⇒
false).

**D4. JIT provisioning = OFF by default (`auth.oidc.jit_provisioning: false`).**
Default requires a pre-existing user row (no accidental over-privilege). When ON,
a first OIDC login creates a `users` row (email + oidc_subject/provider, NULL
password) with the roles from D5. When OFF and no row matches, the login is
rejected (403) — never auto-created.

**D5. Group→role mapping = explicit config map to existing DB role names,
default-DENY, IdP-authoritative.** `auth.oidc.role_mappings: {<idp_group_value>:
<leoflow_role>}`. An unmapped group grants no role. On every login the mapped set
is written to the DB `user_roles` as the user's EXACTLY-current roles (see the
reconciliation consequence below) and also carried in the minted token; the
per-request reload then turns those DB roles into permissions. The identity
provider therefore stays authoritative for an OIDC user's roles.

**D5a. `default_role` (opt-in UX softener).** `auth.oidc.default_role` softens
default-deny **without weakening the secure default**: when an authenticated
user resolves to zero mapped roles and `default_role` is set, they are granted
that single fallback role (operators are advised to use a read-only role such as
`viewer`). Empty (the default) keeps strict deny — an unmapped user gets no role.
The role must exist for the resolved tenant; an unknown `default_role` fails the
login closed (audited), on both the pre-provisioned and the JIT paths.

**D6. Tenant pin = issuer-locked + IdP claim (`tid`/`hd`), fail-closed. NOT
email-domain (H1).** (a) the config pins the org's single-tenant **issuer URL**
and every ID token whose `iss` differs is rejected; (b) the tenant is resolved
from an IdP-issued claim — Entra `tid`, Google Workspace `hd` — via
`auth.oidc.tenant_claims: {<value>: <tenant_name>}`; (c) `email_verified == true`
is required before any email is trusted; (d) an absent/unmapped claim or an
issuer mismatch is a **403 that never falls back to `default`**. Email domain is
never the pin (spoofable under Entra `/common` or "any Google account").

**D6a. `allowed_email_domains` (login-level allowlist, layered on the pin — NOT
the pin).** `auth.oidc.allowed_email_domains` gates **every** OIDC login
(pre-provisioned and JIT). It runs **only after** the issuer pin, the `tid`/`hd`
pin, and `email_verified == true` have passed, so the email domain is trustworthy
at that point. Empty imposes no restriction (the `tid`/`hd` pin is the sole
boundary). Non-empty admits a login only when the verified email's domain is in
the list; every other login is 403 (audited). It is configured at install time
(env `LEOFLOW_AUTH_OIDC_ALLOWED_EMAIL_DOMAINS` / Helm value). It does **not**
substitute for `tid`/`hd` — H1 stays closed.

**D7. `provider: jwt` stays default; `oidc` is opt-in AND Pro-gated.**
`validateProvider` allows `provider: oidc` only when `ui.edition == "pro"` AND
the required config is present (issuer, client_id, redirect_url, https issuer);
otherwise boot fails closed with an actionable message. The JWT secret stays
required because the callback mints the app's own `_token`.

**D8. Break-glass = designated local admin(s) only (H2).** When
`provider: oidc`, the credential path `POST /auth/token` accepts ONLY the emails
in `auth.oidc.break_glass_emails`; every other password login is rejected. Each
break-glass attempt (allowed, denied, bad-credentials) is audited. Default posture
is effectively SSO-only. `dev_no_auth` stays loopback-only. In JWT mode the
credential path is ungated and unchanged.

**D9. Verify path is keyless (ADR 0035-aligned).** ID-token verification uses the
IdP's public OIDC discovery + JWKS — no secret. The `client_secret` is used only
for the code exchange, injected via `LEOFLOW_AUTH_OIDC_CLIENT_SECRET` (env, never
persisted, never logged). All `auth.oidc.*` keys are registered in
`serverDefaults` so viper binds the scalar `LEOFLOW_AUTH_OIDC_*` env vars; the
map/slice leaves (role_mappings, tenant_claims, allowed_email_domains,
break_glass_emails) are config-file / Helm-values driven.

**D10. Dependency = `github.com/coreos/go-oidc/v3` (+ `golang.org/x/oauth2`,
promoted to direct).** go-oidc verifies the signature (against the discovered
JWKS), the audience, and the issuer, and its `NewProvider` pins the issuer at
discovery. It does **not** verify nonce, azp, the callback state/CSRF binding, or
clock skew — these are enforced explicitly in Leoflow code (H4): `state` bound to
and compared against the signed cookie; RFC 9207 `iss` response param checked
where present; `aud == client_id`; `azp == client_id` when present; `nonce`
against the state cookie (constant-time); and `exp`/`iat`/`nbf` with a small
configurable clock skew (default 60s), which is why go-oidc's leeway-less expiry
check is disabled and this package is the single time authority.

**Audit (H5).** Every auth event is recorded to the existing audit sink
(`RecordAuthEvent`, alongside `RecordUserCreatedAudit`): login success/failure,
tenant-pin rejection, JIT provisioning, break-glass (allowed/denied), each with
actor/email/tenant/outcome and a non-secret reason. Tokens and the client secret
are never recorded. Audit is best-effort — a sink error never changes a security
outcome (a 403 stays a 403, a success stays a success). Events that never
resolved a tenant (a tenant-pin rejection) fall back to the `default` tenant so
the security event still lands, with the attempted values in the metadata.

## Consequences

- New public redirect endpoints; one new direct dependency; `x/oauth2` promoted
  to direct.
- The self-asserted tenant is closed off for OIDC logins (the JWT/password path
  is unchanged). Email-domain is a secondary, opt-in allowlist, never the pin.
- Session length is bounded by the JWT TTL; full logout propagation is deferred
  with the rest of ADR 0055. The per-request `is_active` reload gives
  revocation-within-TTL.
- The identity provider is authoritative for an OIDC user's roles. On every
  successful login — for both JIT-created and returning users — the user's DB
  `user_roles` are reconciled to EXACTLY the group→role-mapped set (D5), falling
  back to `[default_role]` when the mapping is empty and one is set, or to no
  roles otherwise. The per-request reload then reads that freshly-written set, so
  an IdP demotion or deprovisioning takes effect on the next login. The
  consequence is that a manual DB role grant for an OIDC user is overwritten by
  the next login: OIDC users' roles are managed through IdP group membership, not
  through admin DB grants. A role name (mapped or `default_role`) that does not
  exist in the tenant fails the login closed and is audited, rather than silently
  granting nothing.

## Security review — folded in

H1 → D6 (issuer-lock + `tid`/`hd`, never email-domain as the pin; D6a adds the
domain allowlist strictly on top). H2 → D8 (break-glass scoped to an explicit
allowlist, audited). H3 → the role model relies on the seeded role ladder and the
per-request permission reload already in place. H4 → D10 (explicit
nonce/azp/state/skew/iss checks, not assumed from the library). H5 → the audit
decision. Verification order is fixed and fail-closed at every step:
signature/audience/issuer → nonce → azp → clock skew → subject → email_verified →
tenant pin → email-domain allowlist.

## Amendment (2026-09-17): the slice leaves do bind from the environment, and D9's claim about them is withdrawn

D9 says, of the configuration surface:

> the map/slice leaves (role_mappings, tenant_claims, allowed_email_domains,
> break_glass_emails) are config-file / Helm-values driven.

The decision D9 records is unaffected: the verify path is still keyless, and the
client secret still travels only as an environment variable. What is withdrawn is
the supporting statement about how those four settings are loaded. It grouped two
different things and got one of them wrong.

**The slices bind from a single environment variable.** viper's default decoder
installs `StringToSliceHookFunc(",")`, so a comma-separated value becomes a list.
Measured against `LoadServer` rather than reasoned about:

```
LEOFLOW_AUTH_OIDC_SCOPES=openid,email,profile,groups   -> 4 elements
LEOFLOW_AUTH_OIDC_ALLOWED_EMAIL_DOMAINS=a.com,b.com    -> 2 elements
LEOFLOW_AUTH_OIDC_BREAK_GLASS_EMAILS=x@a.com,y@b.com   -> 2 elements
LEOFLOW_AUTH_OIDC_TENANT_CLAIMS=t1:default             -> empty map
```

**The two maps do not, and for a different reason than D9 gives.** `role_mappings`
and `tenant_claims` are tagged `mapstructure:"-"` and are absent from
`serverDefaults`, so viper never sees them at all. Their keys may contain dots (an
IdP group name, a Google Workspace domain), and a dotted key is ambiguous in both
the environment and viper's own key space, so they are read from the YAML config
file by a dedicated decoder. That exclusion is deliberate and correct.

**"Helm-values driven" was never true of either.** The chart ships no server
config file and no volume to mount one, so the two maps cannot be set through Helm
by any route. That is tracked in #1143, along with the boot gate that now refuses
the resulting unsatisfiable configuration instead of letting it reject every login
in silence.

This is recorded as an amendment rather than an edit because the original text is
what a reader of this ADR believed at the time, and because it did mislead
someone: a field report quoted the same claim back to the project, from the
matching comment in `internal/config/server.go`, as the explanation for a problem
it does not explain. Both the comment and the configuration reference are
corrected; this note keeps the record honest about where the belief came from.


## Amendment (2026-09-17): D4's "pre-existing user row" has no way to exist

D4 says JIT provisioning is off by default and that the default

> requires a pre-existing user row (no accidental over-privilege).

The decision stands: creating users implicitly on a first login should be opt-in.
What is withdrawn is the sentence describing what the OFF state leaves an
operator with. It reads as a supported alternative and there is none.

**Nothing an operator can run creates a row that a login would match.** D3 makes
the identity match `(oidc_provider, oidc_subject)` and `FindUserByOIDCSubject` is
the only lookup on the login path, so only a row carrying both columns can ever
resolve. The only statement that writes them is `CreateOIDCUser`
(`internal/storage/queries/users.sql`), reached only from `jitProvision`, which
runs only when the flag is on. No API endpoint, CLI command, admin UI or
migration writes either column. The user-creation API writes a password user,
which leaves both NULL.

So with `jit_provisioning: false` every first login is denied with
`no_user_jit_off`, and the remedy the sentence implies does not exist. The
deployment boots green, the IdP redirect works, the token verifies, the tenant
pin passes, and the login is refused at the last step with a generic 403. That is
the shape a field report hit on Google Workspace.

**What ships now:** the Helm chart defaults `auth.oidc.jitProvisioning` to `true`,
so a chart install is not in this state, and the server logs a boot WARN naming
`auth.oidc.jit_provisioning` whenever it is off. It is a warning and not a boot
failure because an operator who wrote the two columns directly with SQL has a
working deployment, and a hard gate would break it.

**What is left open:** D3 already names email as a "verified link key", but no
code links an existing user by verified email today. Implementing that link is
the honest way to make the OFF state mean what D4 says it means: an administrator
pre-creates the user with the roles they intend, and the first SSO login binds
the subject to it. That is a behavior change with its own security argument to
make (it lets an IdP account claim an existing account by email), so it is
tracked separately rather than folded into a patch release.

## Amendment (2026-09-17): a refused login answers the browser, not an API client

D4, D5a, D6 and D6a each describe a rejection as a **403**, and the Audit note
says "a 403 stays a 403". The decisions are unaffected: every one of those paths
still fails closed, still audits, and still never falls back to a default
identity or a default tenant. What changes is the **answer on the wire**.

Both OIDC routes are reached only by a top-level browser navigation: the user
clicks the sign-in control, or the IdP redirects them back. `problem+json` is the
right answer to an API client and the wrong one to a browser, which renders it as
a page of raw JSON with no way back to the sign-in page, which is also where the
retry and the break-glass form live.

A refused login now answers **`302` to `/api/v2/auth/login?sso_error=1`**, and
the login page says that sign-on did not complete.

**The redirect carries no reason.** Withholding the cause from the browser is the
posture of this whole path, since it is the same information someone probing the
deployment is after. The marker is `sso_error=1` and nothing else, the response
body is the bare redirect, and no response header carries the cause. The marker
is ignored unless an OIDC flow was discovered at boot, so it cannot be used to
make a `provider: jwt` deployment claim a sign-on was rejected.

Where the reason goes is unchanged: the audit row (`oidc.login.failure` /
`oidc.tenant_pin_rejected`, with the non-secret `reason`) and, since #1162, a
`WARN` on the control plane's server log.

Read anywhere in this ADR, "403" now means "rejected, fail-closed, audited". The
credential path (`POST /auth/token`, including break-glass) is unchanged and
still answers `problem+json`: it is an API call made by the login page's script,
not a navigation.

## Amendment (2026-09-17): the Helm gap the first amendment named is now closed

The first amendment on this page, the one that withdrew D9's claim about the
slice leaves, says "'Helm-values driven' was never true of either [map]," tracked
the gap as #1143, and left it open. #1159 closes it:
`helm/leoflow/templates/oidc-config.yaml` now renders `auth.oidc.tenantClaims`
and `auth.oidc.roleMappings` into a mounted ConfigMap (the file the two
dotted-key maps still need, per D9's surviving claim that they are
config-file-only), and `LEOFLOW_CONFIG` is pointed at it. A Helm install can set
both settings today; see
[SSO with Google Workspace](/operate/sso-google-workspace/) and
`helm/leoflow/examples/values-oidc-google.yaml` for the resulting values shape.

## Amendment (2026-09-19): D2's parenthetical described a hazard, not a hardening

D2 sets the session cookie server-side, `HttpOnly; Secure; SameSite=Lax`, and
adds "(hardened vs the credential login page's client-JS cookie)". The
comparison is accurate and the conclusion drawn from it was wrong: it reads as
though the credential path having a weaker cookie were a known, acceptable
difference. It was neither, and #1191 is what it cost.

Two paths set the same cookie by different mechanisms. **A script cannot
overwrite an `HttpOnly` cookie**, so once an SSO session existed, the login
page's `document.cookie` write was silently discarded by the browser: the server
minted a token and answered `200`, the page navigated away, and the browser kept
sending the old SSO `_token`. A break-glass login appeared to do nothing while
every log recorded success, at the one moment break-glass exists for, when SSO
is already broken and the operator is already unsure what works.

The second consequence went unreported and is the wider one. A deployment on
`provider: jwt` never reaches this callback, so its session cookie was **only
ever** the page-set one: not `HttpOnly`, readable by anything running on the
page. The posture of the session token was decided by which button the user
pressed.

`POST /auth/token` now sets the cookie itself, through the same helper this
callback uses (`internal/api/session_cookie.go`), and the page's write is gone.
The response body still carries `access_token` for the CLI, the SPA and every
other API client; what changed is that the browser no longer depends on a script
to establish its own session. D2 stands; the parenthetical is withdrawn, and the
attributes it lists are now the attributes of both paths and of the logout that
clears them.

One knob came with it, `auth.session_cookie_insecure` (default `false`), because
`Secure` is now on a path that previously decided it from `location.protocol`.
A browser refuses a `Secure` cookie from a plain-http origin that is not
loopback, so without an escape hatch a plain-http deployment would have been
upgraded into a sign-in page that posts valid credentials and lands back on
itself. It cannot be derived from the request: behind a TLS-terminating ingress
the server sees plain http while the browser sees https, so request-derived
`Secure` would strip it from exactly the deployment that most needs it.
Operator-scoped, `WARN` at boot while it is on.

## Amendment (2026-09-19): what the tenant pin proves when the IdP is single-tenant

A production Cognito deployment reported that D6's tenant pin, which the chart
refuses to render without, has nothing honest to bind to behind an IdP that
issues no tenant claim. The report is correct, and the fix is smaller than it
first looked, so this records what the pin means in that shape rather than
changing the decision.

**The tautology is real.** A Cognito user-pool ID token carries no claim naming
the upstream domain. The obvious reach is `aud`, and `aud` is already validated
as the audience against the client id by go-oidc before `resolveTenant` runs
(`internal/oidc/verify.go`, `newVerifierWithProvider`). Pinning on it therefore
reproves what has been proven, and leaves a setting that reads as an access
control and is not one. The reporter's phrasing is the one worth keeping: anyone
reading those values later has to reconstruct the argument to know it is a
tautology, and the obvious reading is that a real boundary exists.

**The answer already existed and was not findable.** D6 does not require a
DOMAIN claim, only an IdP-issued one, and `iss` qualifies: it is unique per
Cognito user pool, per Okta org and per Keycloak realm. The Okta and Keycloak
guidance already says to pin on `iss` and describes the result honestly, as
"redundant with, rather than weaker than, the issuer check". That sentence is
the correct description for Cognito too. What was missing was a Cognito page
saying so, not a new mechanism.

**So D6 stands, with its scope stated.** The pin's job is to make the tenant an
IdP-attested fact rather than a deployment assumption, and to fail closed when a
token carries a value nobody mapped. On a multi-tenant issuer that is a real
boundary. On a single-tenant issuer it is a restatement of the issuer pin, and
the boundary that decides who may log in is `allowed_email_domains` plus
`email_verified`. Both are honest configurations; only one of them was written
down.

**A dedicated single-tenant mode is deliberately NOT adopted.** `auth.oidc.tenant:
default`, accepted instead of the pin, would let the config state the truth
directly, and it was proposed. It is declined for now because it buys a clearer
spelling of something that already works, at the cost of a second code path
through the one decision that decides which tenant's data a session reaches, and
because a required field that is sometimes optional is exactly the shape an
operator half-configures. If the redundant pin proves to be a recurring
misconfiguration rather than a recurring confusion, that trade changes.

**One thing changed in code, in #1192.** `resolveTenant` read the claim as a
string and nothing else, so an IdP emitting `aud` as an array (which OpenID
Connect permits) rejected every login as `tenant_not_allowed`: a message that
sends the operator to inspect a map that is correct. A string or an array is
accepted now; an array naming two accepted tenants is `tenant_ambiguous` rather
than resolved to one, because it identifies neither and the choice would decide
which tenant's data the session reaches. `iss`, the claim this amendment
recommends, is always a string on an ID token, so the pin this page describes
was never exposed to that gap.
