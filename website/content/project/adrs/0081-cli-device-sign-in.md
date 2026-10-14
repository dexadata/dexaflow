---
title: "ADR 0081: the CLI signs in from a machine with no browser through a device sign-in served by Dexaflow"
linkTitle: "0081 · CLI device sign-in"
weight: 810
description: "ADR 0081: dexaflow auth login --device follows the shape of the device authorization grant (RFC 8628), served by Dexaflow rather than by the IdP. The person enters a short code on a Dexaflow page, signs in through the server's own OIDC flow, and confirms the code before approving; the CLI polls the server. Pending sign-ins live in the database so any replica can answer."
---

**Status:** Proposed
**Date:** 2026-10-14
**Relates:** ADR 0079 (CLI single sign-on through the server), ADR 0057 (OIDC/SSO login, tenant pin and role mapping), ADR 0074 (scoped API tokens for CI)
**Issues:** #1704

## Context

ADR 0079 signs the CLI in through a loopback redirect, which needs a browser
that can reach `127.0.0.1` on the machine running the CLI. In an SSH session
or a container there is none, short of forwarding the port. ADR 0079 left the
device authorization grant for later and sketched it as the server running
the grant against the IdP's device endpoint while the CLI polls the server.

That sketch does not hold for the IdPs Dexaflow is deployed with. Google
does not offer the device grant to the web application client the server
signs in with (only to "TV and limited input" clients, with a narrow set of
scopes); Cognito has no device endpoint at all; others offer it only on a
separate public client. Using it would mean a second client registration
per IdP, a second set of claims to pin the tenant from, and a different
ID token path from the one the UI and ADR 0079 verify.

## Decision

1. **Dexaflow serves the device sign-in, and the server stays the only OIDC
   client.** `POST /api/v2/auth/cli/device` answers what RFC 8628 section 3.2
   answers: a `device_code`, a `user_code`, `verification_uri` (and
   `_complete`), `expires_in` (600) and `interval` (5). The person opens the
   page in any browser and enters the code; that starts the server's own
   OIDC login, the UI's round trip of ADR 0057 with ADR 0079's state cookie
   carrying the pending sign-in instead of a loopback. Same callback, ID
   token checks, tenant pin, group mapping, JIT policy and role
   reconciliation. The IdP needs no change.
2. **The consent shows the code and asks for it to be confirmed.** The
   device flow's weakness is phishing (RFC 8628 section 5.4): whoever starts
   a sign-in can send the page and the code to someone else, and their
   approval signs the sender's CLI in as them. So the callback ends on a
   consent page that shows the account, the tenant and the code, says to
   cancel a code someone sent, and approves only with a ticked confirmation
   that the terminal shows that code, through the same-origin, CSRF-bound,
   SameSite=Strict, single-use consent form of ADR 0079 decision 3 (sealed
   for its own purpose, so neither consent can stand in for the other).
3. **Codes.** The user code is eight letters from twenty consonants (34.6
   bits, no words, nothing read as a digit), drawn without modulo bias and
   typed in any case with or without the dash. The verification form allows
   ten attempts a minute per address, gives an unknown and a malformed code
   the same page, and audits each refusal (`oidc.cli_device_verify`). The
   device code is 256 random bits and never printed.
4. **The token route polls.** `POST /api/v2/auth/cli/token` with `grant_type`
   `urn:ietf:params:oauth:grant-type:device_code` answers 400 with
   `authorization_pending`, `slow_down` (the interval grows by five seconds),
   `access_denied` (once), `expired_token`, or `invalid_grant` for any device
   code that is not live, all as problem documents with an `error` member;
   then the token `POST /auth/token` answers. Polls have their own budget per
   address (120 a minute), because several CLIs behind one address poll
   every five seconds and the interval of each sign-in is enforced by
   `slow_down`. The code exchange keeps the OIDC routes' budget.
5. **Pending sign-ins live in the database.** Unlike ADR 0079's sealed codes,
   a device sign-in needs shared state: the CLI may poll one replica while
   the person approves on another. Table `cli_device_authorizations`
   (migration 045) keys each by the HMAC-SHA256 of its codes under a key
   derived with HKDF from the JWT secret, so neither code is stored and
   rotating the secret ends the sign-ins in progress. A row has no tenant
   until it is approved: the approval names the approver's user and tenant,
   checked against `users` in the same statement. The approval is a
   conditional update, so it happens once across replicas; the poll that
   redeems it locks and deletes the row, so it happens once too. Rows are
   deleted ten minutes after they expire, and at most 10,000 may be live, so
   the unauthenticated start route cannot fill the table.
6. **No token is stored.** The approval records the user and the roles the
   sign-in resolved; the token is minted when the CLI redeems it, with the
   configured TTL, so the database never holds a usable credential.
7. **The CLI chooses it when the browser sign-in cannot work.**
   `GET /api/v2/auth/methods` gains `device`. `dexaflow auth login --device`
   asks for it. With no flag, single sign-on offered, and a session with no
   browser to open (an SSH session, or Linux or a BSD with neither `DISPLAY`
   nor `WAYLAND_DISPLAY`), the CLI uses it when the server serves it and says
   why; `--sso` keeps the browser sign-in. The CLI prints the page and the
   code, never the device code, and stops at `--timeout` or the sign-in's
   expiry.
8. **The routes are Dexaflow routes under `/api/v2/auth/`,** public like the
   other sign-in routes. The verification page is `/api/v2/auth/device`, kept
   short because people type it; its address is built from the origin of the
   configured OIDC redirect URL, the server's public address, never from
   request headers. Without single sign-on, a store, or a usable redirect
   URL, none of the routes is registered and `device` is false.

## Consequences

- A headless machine signs a person in with single sign-on and no port
  forwarding; CI still uses scoped API tokens (ADR 0074).
- One migration and a table of short-lived rows; the browser sign-in of
  ADR 0079 stays stateless.
- The device sign-in can be phished like every device grant; the confirmed
  code on the consent page is the defense, and its refusals and approvals
  are in the audit trail.
- The consequence of ADR 0079 that sketched the grant against the IdP's
  device endpoint is replaced by this decision.
