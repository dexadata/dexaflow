---
title: "ADR 0079: the CLI signs in with single sign-on through the server, which stays the only OIDC client"
linkTitle: "0079 · CLI single sign-on through the server"
weight: 790
description: "ADR 0079: dexaflow auth login signs in with the cluster's OIDC provider by driving the server's own sign-in in a browser. After a consent click, the server hands the CLI a one-time code on a loopback redirect, bound to a PKCE challenge, and the CLI exchanges it for the token a password login returns. The CLI never talks to the IdP."
---

**Status:** Accepted
**Date:** 2026-10-14
**Relates:** ADR 0057 (OIDC/SSO login, tenant pin and role mapping), ADR 0041 (deploy without a pipeline, `auth login` once), ADR 0074 (scoped API tokens for CI)
**Issues:** #1704

## Context

Since ADR 0057 a person signs in to the UI through the cluster's OIDC
provider, but `dexaflow auth login` only takes a password. Under
`auth.provider: oidc` only break-glass accounts have one, so everyone else
had no way to get a CLI token except copying the session cookie out of a
browser.

There are two ways to let the CLI use the provider. The CLI can be an OIDC
client itself: a public client registered at the IdP, which runs the
authorization code flow with PKCE straight against the IdP and sends the
resulting ID token to the server. Or the server, which is already the OIDC
client, can run the flow it runs for the UI and hand the result to the CLI.

## Decision

1. **The server stays the only OIDC client.** The CLI opens
   `GET /api/v2/auth/oidc/login` in the browser with `redirect_uri` (a
   loopback address), its own `state`, and the S256 `code_challenge` of a
   verifier it keeps. The server checks them, seals them in the state cookie
   with its own state, nonce and PKCE pair, and runs the browser flow of
   ADR 0057 unchanged: same callback, same ID token checks, same tenant pin,
   group mapping, JIT policy and role reconciliation. The client secret, the
   role mapping and the tenant rules exist in one place, and the IdP needs no
   second client registration or redirect URI.
2. **The callback of a CLI login ends with a consent page, not a cookie.**
   It mints the token exactly as for the UI, then answers a Dexaflow page
   that names the account, the tenant and the loopback port: "Sign in the
   Dexaflow CLI?". A refusal before that point redirects to the loopback with
   `error=access_denied` (or `server_error`) and no reason, which stays in
   the audit trail and the server log as for the UI.
3. **Nothing reaches the loopback without a click (RFC 8252 section 8.6).**
   Whoever starts a login picks the challenge and so holds the verifier. A
   web page can navigate a victim's browser to a CLI login with its own
   challenge, and an IdP that signs the victim in silently would deliver the
   victim's code to the victim's loopback with no interaction. Any local
   listener on that port, or a local service that records request URLs,
   would then hold the code, and the code with the attacker's verifier is
   the victim's token. So the pending sign-in is sealed in a `SameSite=Strict`,
   HttpOnly cookie scoped to `/api/v2/auth/cli/`, and only the page's own
   form, a same-origin `POST /api/v2/auth/cli/consent` carrying the CSRF token
   sealed in that cookie, issues the code and redirects to the loopback.
   Each consent is accepted once, lasts five minutes, and the page cannot be
   framed. Cancel redirects to the loopback with `error=access_denied`.
4. **The code is short-lived, single use and bound to the verifier.** It is
   the token sealed with AES-GCM under a key derived with HKDF from the JWT
   secret, with additional data naming its purpose and format version, so a
   consent can never be redeemed as a code. It carries the challenge, the
   redirect it was issued for and an expiry of 60 seconds.
   `POST /api/v2/auth/cli/token` redeems it for a well-formed verifier
   (43 to 128 unreserved characters) whose S256 is the challenge, and the
   same `redirect_uri`, and answers what `POST /auth/token` answers, with
   `expires_in` the token's remaining lifetime. Every refusal (unknown,
   expired, used, tampered, wrong verifier or redirect) is the same 400. The
   consent and the exchange are audited (`oidc.cli_consent`, `oidc.cli_code`)
   against the code's tenant and user when the code is authentic.
5. **Single use is per replica, and that is enough.** The code is stateless
   so that any replica can redeem it, and each replica remembers the codes
   and consents it accepted. Redeeming one twice on two replicas takes the
   verifier. The IdP-side state of a CLI login is claimed once too, but only
   after the IdP's code was exchanged and the ID token verified, so forged
   callbacks use up nothing. No storage and no migration are added.
   Rotating the JWT secret invalidates every outstanding code and consent,
   which costs at most one retried login.
6. **Only canonical loopback redirects.** `redirect_uri` must be exactly
   `http://127.0.0.1:<port>/<path>` or `http://[::1]:<port>/<path>`, with a
   path of letters, digits, `-`, `_` and `/`, and must rebuild to itself:
   no user info, query, fragment, escapes, dots or IPv4-mapped addresses
   (RFC 8252 section 7.3). `localhost` is refused because a name can resolve
   elsewhere. This keeps the code on the machine whose browser approved the
   sign-in; it does not protect the code from a local listener, which is
   what decision 3 is for.
7. **The CLI asks before it chooses.** `GET /api/v2/auth/methods` reports
   whether the server serves single sign-on and whether a password login can
   succeed, from the same settings the sign-in page renders from. With no
   username or password given, `auth login` uses single sign-on when it is
   offered and a password otherwise; a server without the route is treated
   as password only. It prints which method it chose and why. Its loopback
   ignores a redirect with another state, so a local process cannot abort
   the login, and completes once. It refuses to send a password over plain
   http to a host that is not loopback without `--allow-insecure-password`.
8. **The new routes are Dexaflow routes under `/api/v2/auth/`,** next to
   the OIDC and renewal routes, public like them, and share the per-address
   budget of the OIDC routes. They change nothing Airflow defines.

## Consequences

- No IdP configuration changes for CLI sign-in, and the CLI holds no IdP
  settings or secrets.
- A CLI sign-in takes one click more than the UI's, on the consent page.
- A CLI sign-in needs a browser that can reach both the server and the
  machine running the CLI. Headless machines need the device authorization
  grant, which is left for a later change: the server would run it against
  the IdP's device endpoint and the CLI would poll the server. CI keeps using
  scoped API tokens (ADR 0074).
- The token is the same short-lived token a password login returns, renewed
  the same way.
