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
    redirectUrl: https://dexaflow.example.com/api/v2/auth/oidc/callback

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

## Signing in from the CLI

`dexaflow auth login` uses the same single sign-on. With no username or
password given, it asks the server whether single sign-on is on
(`GET /api/v2/auth/methods`) and, when it is, opens the browser on the
server's sign-in:

```bash
dexaflow auth login --target dev
# Opening your browser to sign in. If it does not open, visit:
#   https://flow-dev.acme.dev/api/v2/auth/oidc/login?redirect_uri=http%3A%2F%2F127.0.0.1%3A53121%2Fcallback&...
```

The sign-in in the browser is the UI's: the same Google page, the same tenant
pin, role mapping and just-in-time provisioning, and the same audit event
(`oidc.login.success`, with `client: cli`). Instead of a session cookie, the
server then shows a page that names the account and the port: **Sign in the
Dexaflow CLI?** Approve it only if you just ran the command. A page you
visited can open this sign-in too, and without that click the token could
reach whatever program listens on that port. After **Sign in the CLI**, the
server sends the browser back to the port the CLI listens on at `127.0.0.1`,
with a one-time code that the CLI exchanges, with a PKCE verifier only it
holds, for the token a password login would have saved for that server. The
code expires after 60 seconds and works once per server replica; the
approval page lasts five minutes. Both steps are audited (`oidc.cli_consent`,
`oidc.cli_code`).

Nothing changes at the IdP for this: the redirect URL Google calls is still
`.../api/v2/auth/oidc/callback`, and the CLI never talks to Google.

- `--no-browser` prints the URL instead of opening a browser, for a machine
  whose browser is somewhere else. The browser must still be able to reach
  `127.0.0.1` on the machine running the CLI, so on a remote host forward the
  port (for example `ssh -L`), or use `--device` (below).
- `--sso` asks for single sign-on in a browser explicitly and fails when the
  server does not offer it. `--timeout` (default 5m) bounds the wait for the
  approval. The command prints which method it chose and why.
- `--username`, `--password` or `--password-stdin` (or `DEXAFLOW_USERNAME` /
  `DEXAFLOW_PASSWORD`) sign in with a password instead, which is the
  break-glass path when single sign-on is broken. A password is not sent
  over plain `http://` to a server that is not on this machine unless you
  pass `--allow-insecure-password`.
- A refused sign-on ends the command with `single sign-on was refused`; the
  reason is in the audit trail and the server log, as for the UI.
- CI should not sign in as a person: use a scoped API token
  (`dexaflow auth create-token --scope deploy`).

### From a machine with no browser

In an SSH session or a container, `dexaflow auth login --device` signs in
with a browser on any other device: your laptop or your phone. The command
prints a page and a short code, and waits:

```bash
dexaflow auth login --device --target dev
# Signing in with single sign-on on another device (--device)
# To sign in, open this page in a browser on any device:
#
#   https://flow-dev.acme.dev/api/v2/auth/device
#
# sign in, and enter the code:
#
#   BCDF-GHJK
```

Open the page and sign in with Google as for the UI: the same tenant pin,
role mapping, just-in-time provisioning and audit event
(`oidc.login.success` with `client: cli` and the device `grant`). Only then
does the server ask for the code (filled in already when you opened the
link that carries it). For a live code it shows **Sign in the Dexaflow CLI
on another device?** with the account, the tenant, the code, and when and
from which address the sign-in was started, next to the address of your
browser. Check that the code is the one your terminal shows, tick the box
that says so, and approve; the command finishes on its own and
saves the token as any other login does. Cancel if you did not start the
command yourself: a link or a code someone sent you is how a device
sign-in is phished, and approving it would sign their CLI in as you.

With no flag, `auth login` picks the device sign-in by itself when the
server serves it, its output is a terminal, and the session has no browser
to open (an SSH session, or Linux with neither `DISPLAY` nor
`WAYLAND_DISPLAY`, and no `BROWSER` set), and says so; `--sso` keeps the
browser sign-in on this machine. It warns when the page is on another host
than the server it talks to, which usually means the OIDC `redirectUrl`
does not name the public address.

What the server enforces:

- The code is eight letters from twenty consonants (about 34.6 bits) and a
  sign-in lasts 10 minutes. It is asked for only after single sign-on, so
  nobody can guess codes without an account. Each account may enter ten
  wrong codes a minute on each replica; an unknown code and a malformed one get the same
  page, and each is audited as the account (`oidc.cli_device_verify`).
- Each address may open the sign-in page ten times a minute and start three
  device sign-ins a minute (IPv6 by its /64), and hold five in progress
  (IPv6 by its /56); the cluster holds at most 10,000.
- The CLI polls every 5 seconds; a faster poll is told `slow_down` and the
  interval grows. The approved sign-in is redeemed once, and the server
  keeps only keyed hashes of the codes, never a token: the token is minted
  when the CLI redeems the approval. Both steps are audited
  (`oidc.cli_consent`, `oidc.cli_code`, with the device `grant`).
- The token carries the roles the sign-in resolved when you approved it,
  as any token carries the roles of its login.
- The pending sign-ins live in the database (migration 045,
  `cli_device_authorizations`), so the poll and the approval may reach
  different replicas. Rotating the JWT secret invalidates the ones in
  progress. A row, with the address it was started from, is deleted ten
  minutes after it expires, by a sweep each minute: about 21 minutes after
  the start at the latest.
- The budgets and the consent page's addresses need the caller's real
  address. Behind an ingress or a load balancer, list it in
  `server.trusted_proxies`; without that every caller has the proxy's
  address and shares its budgets, the consent page leaves the addresses out
  when both are the same private one, and the server warns at startup.
- The page's address comes from the configured OIDC `redirectUrl`, the
  server's public origin; nothing changes at the IdP, which does not need a
  device endpoint of its own.
- The link that fills in the code carries it as `?user_code=`, so ingress
  and proxy access logs may record it. It is useless without your sign-in
  and expires in 10 minutes; leave it out of shared logs if you can.
- The start, the verification page and the token route are public. Expect
  503 from the start when the cluster is at its cap or starts are busy, and
  429 from an address over its budgets; heavy abuse of the start is for the
  ingress to rate limit.

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

## Related

- [Users, roles and API tokens](/operate/users-roles-tokens/): the built-in
  roles your role mappings grant, sessions, and API tokens for CI.
