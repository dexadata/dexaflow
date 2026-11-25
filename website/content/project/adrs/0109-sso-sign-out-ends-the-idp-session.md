---
title: "ADR 0109: SSO sign-out ends the identity provider session, and the browser keeps the ID token for it"
linkTitle: "0109 · SSO sign-out at the IdP"
weight: 1090
description: "ADR 0109: under provider oidc, sign-out sends the browser to the issuer's end_session_endpoint (OpenID Connect RP-Initiated Logout) by default, with the login's ID token as id_token_hint. The ID token is kept in an HttpOnly cookie scoped to the sign-out route, never on the server. auth.oidc.logout: local keeps the local-only sign-out, and an issuer with no end_session_endpoint gets it with a boot warning."
---

**Status:** Accepted
**Date:** 2026-11-25
**Relates:** ADR 0057 (OIDC SSO), #1165, #1160 (SSO control on the sign-in page), #1288 (external sign-in and sign-out)

## Context

Sign-out cleared this server's session cookie and returned to the sign-in page.
Under `auth.provider: oidc` the identity provider's session was untouched, so
"Sign in with single sign-on" signed the user straight back in without asking
for anything. From the user's side, sign-out did nothing.

OpenID Connect RP-Initiated Logout 1.0 ends the IdP session: the relying party
sends the browser to the issuer's `end_session_endpoint`, published in the
discovery document the server already reads at boot. Four points had to be
decided rather than assumed (#1165):

1. Not every issuer publishes `end_session_endpoint`. Google and Cognito do not;
   Entra, Okta and Keycloak do.
2. `post_logout_redirect_uri` must be registered with the IdP, like the callback.
3. Most issuers want the session's ID token as `id_token_hint`. Okta refuses a
   sign-out without it, and Keycloak asks the user to confirm. The server did not
   keep the ID token past the login.
4. Whether sign-out should end the IdP session is a policy choice. On a shared
   workstation it should; for a user with several applications on the same IdP
   session it is surprising.

## Decision

1. **The IdP sign-out is the default.** `auth.oidc.logout` takes `idp` (the
   default) or `local`. With `idp`, a session that came from SSO signs out
   through the issuer's `end_session_endpoint` with `client_id`, the ID token as
   `id_token_hint`, and `auth.oidc.post_logout_redirect_url` when it is set.
   This is what a user expects the button to do. `local` keeps the previous
   behavior for deployments that share the IdP session across applications.
2. **An issuer without `end_session_endpoint` gets the local sign-out**, and
   the server logs a warning at boot naming `auth.oidc.logout: local` as the way
   to acknowledge it. There is no standard way to end such a session, and
   provider-specific sign-out URLs are left to `auth.external_signout_url`,
   which keeps winning over both modes.
3. **`post_logout_redirect_url` is optional.** Empty sends none and the IdP shows
   its own signed-out page, so the default needs no extra registration.
4. **The browser keeps the ID token, the server does not.** At an SSO browser
   login the callback sets `_oidc_idt`: HttpOnly, Secure (unless
   `auth.session_cookie_insecure`), SameSite=Lax, scoped to
   `/api/v2/auth/logout`, and living as long as the longest session a renewal
   can reach. Sign-out reads and clears it. A CLI or device login sets none.
   An ID token larger than a cookie can hold is replaced by a marker, and the
   IdP sign-out is then sent without the hint.
5. **Only an SSO session signs out at the IdP.** The cookie is what marks one:
   a break-glass password login has none and signs out locally.

## Consequences

- The ID token now lives in the browser as long as the session. It is an
  assertion addressed to this server (its audience is our client id), not an
  access token for any API, and the path scope means it is sent on exactly one
  request. A script cannot read it. Its claims (email, groups) are the same ones
  the session token already carries.
- A deployment that relied on sign-out leaving the IdP session alone sets
  `auth.oidc.logout: local`. The version notes for 0.6.0 say so.
- A deployment with `post_logout_redirect_url` set and not registered at the IdP
  gets the IdP's error page at sign-out; the configuration reference says to
  register it.
- Sessions that started before the upgrade have no `_oidc_idt` cookie, so their
  first sign-out after it is local.
