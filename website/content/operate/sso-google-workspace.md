---
title: Single sign-on with Google Workspace
linkTitle: SSO (Google Workspace)
weight: 95
description: Turn on OIDC login against Google Workspace, and read the audit log when it denies.
---

Dexaflow's SSO is OIDC Authorization Code + PKCE, so Google Workspace works
through the same settings as any other issuer. Two things about Google make it
worth its own page: it emits **no `groups` claim** unless Directory API group
sync is configured, and its tenant claim is `hd`, a domain rather than an opaque
id.

This page assumes the Helm chart. Every setting has a plain config-file or
environment equivalent in the [configuration
reference](/reference/configuration/#oidc--sso-authoidc).

## Before you start

Create the OAuth client in Google Cloud Console, in the same organization as the
Workspace domain:

1. **APIs & Services → Credentials → Create credentials → OAuth client ID**,
   application type **Web application**.
2. Add your callback as an **Authorized redirect URI**. It must be the browser's
   URL for the control plane plus `/api/v2/auth/oidc/callback`, `https://`, and
   the ingress hostname, never the in-cluster Service name.
3. Keep the **client ID** and the **client secret**. Google registers a web
   application as a confidential client, so the secret is required: without it
   the code exchange is rejected with `invalid_client` and the login fails at the
   last step.

{{% alert title="Set break-glass accounts before you enable this" color="warning" %}}
Turning SSO on makes it the login path for everyone except
`auth.oidc.breakGlassEmails`. With that list empty, a wrong `tenantClaims` entry
or a Google outage leaves nobody able to reach the UI, including the person who
has to fix it. Put at least one local admin address there first.
{{% /alert %}}

## Values

```yaml
auth:
  oidc:
    enabled: true
    issuer: https://accounts.google.com
    clientId: "<client-id>.apps.googleusercontent.com"
    existingSecret: dexaflow-google-oidc   # key: oidcClientSecret
    redirectUrl: https://leoflow.example.com/api/v2/auth/oidc/callback

    # The tenant pin. On Google the claim is hd and its value is the Workspace
    # domain. A login whose hd is absent or not listed here is rejected, and it
    # never falls back to the default tenant.
    tenantClaim: hd
    tenantClaims:
      corp.example: default

    # Give role resolution a floor. See "Roles" below: without it, every login
    # on Google resolves to zero roles and CLEARS the user's grants.
    defaultRole: viewer

    breakGlassEmails:
      - admin@corp.example
```

Create the secret separately so it is never in a values file:

```bash
kubectl create secret generic dexaflow-google-oidc --from-literal=oidcClientSecret='<client-secret>'
```

With exactly one entry in `tenantClaims` and `tenantClaim: hd`, the login
redirect carries Google's `hd` parameter, so the account chooser offers only
accounts in that domain.

{{% alert title="Not yet verified against a real Workspace tenant" color="info" %}}
The `hd` parameter is covered by unit tests and by a browser test against a fake
identity provider, but no release so far has exercised it against a real Google
client ([#1177](https://github.com/dexadata/dexaflow/issues/1177)). If it does
not behave as described, what you will see is the account chooser still offering
personal accounts, which the tenant pin then rejects. It cannot admit an account
the pin would refuse: the pin reads the verified `hd` **claim** on the returned
token, not this parameter.
{{% /alert %}} That is a convenience, not a boundary: the tenant pin
verifies the `hd` **claim** on the returned token, which is what actually decides
whether the login is accepted.

## Roles

Google emits no `groups` claim unless you configure Directory API group sync, so
on a default Workspace `roleMappings` matches nothing no matter how it is
written. That matters more than it sounds, because roles are IdP-authoritative:
each login resolves a role set and the user's grants are reconciled to
**exactly** that set.

So a login that resolves to zero roles does not leave the user's existing grants
alone. It removes them. An administrator who logs in through SSO for the first
time comes back out with no roles at all.

`defaultRole: viewer` gives resolution a floor and makes that impossible. Set it
before your first login, not after. The server logs a WARN at boot when
`roleMappings` is set and `defaultRole` is not, precisely because this
combination looks correct and is the one Google lands in.

To map real groups, configure the Directory API group sync in Google Cloud so the
ID token carries a groups claim, then set `groupsClaim` and `roleMappings` to
match. Keep `defaultRole` set regardless: it costs nothing and it is what stops a
sync problem from turning into a mass de-provisioning.

## Users

`jitProvisioning` is on by default in the chart, and it needs to stay on. A login
is matched to a user only by `(oidc_provider, oidc_subject)`, and just-in-time
provisioning is the only code path that writes those columns. Nothing else can
pre-create an account that an SSO login will match, so with it off every first
login is denied.

One case it cannot serve: an address that already has a **local password
account** in the same tenant collides on the users table's unique
`(tenant, email)` and is denied `jit_failed`. Do not create local accounts for
the addresses your users sign in with.

If your Google client belongs to another team and adding a redirect URL is a
ticket, federating Google behind Amazon Cognito is the usual way around it: see
[SSO with Amazon Cognito](/operate/sso-cognito/), which also explains why the
tenant pin works differently there.

## When a login is denied

The user is returned to the sign-in page, which says that single sign-on did not
complete and nothing more. That is on purpose: telling a browser why a login was
refused tells whoever is probing the deployment the same thing. So the cause goes
to two places the operator can read and the browser cannot, the **audit log** and
the server log, and the user has no way to tell a wrong domain from a missing
role. Expect to be asked, and read the audit log rather than the screenshot.

Read the audit log directly, since an SSO-only deployment may have nobody able to
log in to read it through the UI:

```sql
SELECT occurred_at, action, metadata->>'reason' AS reason, metadata->>'email' AS email
FROM audit_log
WHERE action LIKE 'oidc.%'
ORDER BY occurred_at DESC
LIMIT 20;
```

| `reason` | What happened | What to change |
|---|---|---|
| `tenant_not_allowed` | the token's `hd` is absent or not a key in `tenantClaims` | a personal `@gmail.com` account was chosen, or the domain is misspelled. Add the domain, or have the user pick the work account |
| `no_user_jit_off` | no user matches and provisioning is off | set `jitProvisioning: true`; nothing else can create the account |
| `jit_failed` | the row could not be created | usually a local password account already holds that address in the tenant |
| `unknown_role` | `defaultRole` or a `roleMappings` value names a role that does not exist for the tenant | correct the name, or create the role |
| `email_not_verified` | the token's `email_verified` is not true | rare on Workspace; check the account |
| `email_domain_not_allowed` | `allowedEmailDomains` is set and the verified email is outside it | this is the extra allowlist, not the tenant pin. Widen or clear it |
| `inactive` | the user exists and is deactivated | reactivate the user |
| `tenant_mismatch` | the stored user belongs to a different tenant than the token resolves to | the account was created against another tenant; do not move it without deciding which one is right |
| `issuer_mismatch` | the token's `iss` is not the configured issuer | `issuer` must be exactly `https://accounts.google.com` |
| `token_expired` | the token is outside its validity window | clock drift between Google and the control plane; check NTP before raising `clockSkewSeconds` |
| `invalid_state` / `state_mismatch` / `missing_state` | the state cookie did not survive the round trip | usually a redirect URL on a different host than the one the browser started on, or a proxy dropping cookies |
| `token_missing_expiry` | the ID token carries no `exp` | an IdP problem; a token with no expiry is refused rather than treated as eternal |
| `token_no_subject` | the ID token carries no `sub` | as above: there is no stable identity to match a user on |
| `group_claim_overage` | the IdP returned a pointer instead of the groups | Entra only, past roughly 200 group memberships. It hits the most heavily grouped, usually most privileged, users and nobody else. Configure app roles or the groups scope |
| `tenant_ambiguous` | the tenant claim is an array naming two tenants you accept | it identifies neither, so it is refused rather than resolved to one. Narrow `tenant_claims`, or pin on a claim that carries one value |
| `tenant_claim_shape` | the tenant claim is neither a string nor an array of strings | the pin cannot read it; your `tenant_claim` probably names the wrong claim |
| `token_invalid` | verification failed for any other reason | the server log carries the underlying error |

If the page instead says Dexaflow could not complete the sign-in *on its side*,
the failure is ours and not a configuration problem: the error is in the server
log, and a retry may well work.

## When no login is even attempted

Some failures happen before the user sees Google. These are named at **boot**, in
the server log, so check it first when SSO looks dead:

- **`auth.oidc.client_secret` empty**: the code exchange will be rejected by
  Google with `invalid_client`.
- **`auth.oidc.jit_provisioning` off**: every first login will be denied.
- **`auth.oidc.role_mappings` set with no `default_role`**: logins will clear
  grants.
- **discovery timed out**: the pod could not fetch
  `https://accounts.google.com/.well-known/openid-configuration` within 15
  seconds. The error narrows this to the failures that wait rather than refuse
  (a refused connection or an unknown host both fail fast instead): a
  NetworkPolicy or firewall that drops egress instead of rejecting it, a name
  that never resolves, an egress proxy or intercepting TLS middlebox holding the
  request open, or an issuer that accepts the connection and never answers.

Boot fails outright, with the missing keys named in one message, when `issuer`,
`clientId`, `redirectUrl` or the tenant pin are absent.
