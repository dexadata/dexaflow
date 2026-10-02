---
title: Single sign-on with Microsoft Entra ID, Okta, or another OIDC provider
linkTitle: SSO (Entra, Okta, other OIDC)
weight: 96
description: Turn on OIDC login against an IdP other than Google Workspace, and the two settings that differ per provider.
---

Leoflow's SSO is a single OIDC Authorization Code + PKCE flow (ADR 0057). Every
issuer configures through the same `auth.oidc.*` keys documented in the
[configuration reference](/reference/configuration/#oidc--sso-authoidc); the
[Google Workspace page](/operate/sso-google-workspace/) is IdP-specific only
because Google's tenant claim and group behavior are unusual. This page is the
equivalent starting point for Microsoft Entra ID, Okta, Keycloak, or any other
OIDC-compliant IdP.

Read [Roles](/operate/sso-google-workspace/#roles),
[Users](/operate/sso-google-workspace/#users),
[When a login is denied](/operate/sso-google-workspace/#when-a-login-is-denied)
and [When no login is even attempted](/operate/sso-google-workspace/#when-no-login-is-even-attempted)
on the Google page first: none of that content is Google-specific, and it is
not repeated here.

{{% alert title="Set break-glass accounts before you enable this" color="warning" %}}
Turning SSO on makes it the login path for everyone except
`auth.oidc.breakGlassEmails`. With that list empty, a wrong `tenantClaims` entry
or an IdP outage leaves nobody able to reach the UI, including the person who
has to fix it. Put at least one local admin address there first.
{{% /alert %}}

## The two settings that are provider-specific

Everything else in `auth.oidc.*` (issuer, client ID, client secret, redirect
URL, scopes, role mappings, break-glass emails, JIT provisioning) means the same
thing on every IdP. Two keys depend on which one you use:

**`tenantClaim` / `tenantClaims`.** The tenant pin resolves from whatever ID
token claim you name in `tenantClaim`: leoflow reads it as a plain string claim
and rejects (403, `tenant_not_allowed`) any value that is not a key in
`tenantClaims` (`internal/oidc/verify.go`, `resolveTenant`). There is nothing
Google-specific about the mechanism:

- **Microsoft Entra ID**: set `tenantClaim: tid`. The value is the Entra tenant
  GUID, not a domain, so `tenantClaims` maps that GUID to a Leoflow tenant name.
- **Amazon Cognito**: see [SSO with Amazon Cognito](/operate/sso-cognito/). The
  short version is that a user-pool token carries no domain claim, so pin on
  `iss` and do not reach for `aud`, which is already validated as the audience.
- **Okta, Keycloak, or another single-tenant IdP**: these do not emit an
  org/tenant claim by default the way Entra emits `tid`. Because the claim can
  be any string claim already present on the token, the common way to pin a
  single-tenant IdP is `tenantClaim: iss` with `tenantClaims` mapping that
  issuer's exact URL to your tenant name. The issuer is already unique per Okta
  org or per Keycloak realm, and it is already pinned separately by
  `auth.oidc.issuer` (ADR 0057 D6(a)), so this makes the tenant pin redundant
  with, rather than weaker than, the issuer check. If your IdP is configured to
  emit its own org or realm claim instead, name that claim there.

**Group→role mapping and `groupsClaim`.** `role_mappings` matches values from
whichever claim `auth.oidc.groups_claim` names (default `groups`). Entra
overflows that claim past roughly 200 group memberships and returns a
`_claim_names` pointer instead of the list; leoflow detects this and denies the
login (audited `group_claim_overage`) rather than silently treating it as "no
groups" (`internal/oidc/verify.go`, `ErrGroupOverage`). Configure Entra app
roles, or request the dedicated groups scope, to avoid that shape for your most
heavily grouped users. Okta and Keycloak emit `groups` directly once the
authorization server or client scope is configured to include it; consult your
IdP's ID token to confirm the claim name and set `groupsClaim` to match.

## Client secret

Google and Entra always register a server-side web application as a
confidential client, so `auth.oidc.client_secret` is required for both. Okta
and Keycloak register a confidential client too, unless you explicitly create
the client as public: an empty secret against a confidential client is rejected
at the code exchange with `invalid_client`, and the server logs a boot WARN
naming `auth.oidc.client_secret` for exactly this case
(`cmd/dexaflow-server/main.go`, `oidcClientSecretWarnings`).

## Redirect URL

The callback (`.../api/v2/auth/oidc/callback`) must be registered with the IdP
exactly as configured in `auth.oidc.redirectUrl`: the browser's URL for the
control plane, `https://`, never the in-cluster Service name. This is identical
across every IdP; see the worked example in
`helm/dexaflow/examples/values-oidc-google.yaml` for the shape of the values
file (swap `issuer`, `tenantClaim` and `tenantClaims` for your provider).
