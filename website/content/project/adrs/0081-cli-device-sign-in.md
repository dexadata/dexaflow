---
title: "ADR 0081: the CLI signs in from a machine with no browser through a device sign-in served by Dexaflow"
linkTitle: "0081 · CLI device sign-in"
weight: 810
description: "ADR 0081: dexaflow auth login --device follows the shape of the device authorization grant (RFC 8628), served by Dexaflow rather than by the IdP. The person opens a Dexaflow page, signs in through the server's own OIDC flow, then enters the short code and confirms it before approving; the CLI polls the server. Pending sign-ins live in the database so any replica can answer."
---

**Status:** Accepted
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
   page in any browser, which starts the server's own OIDC login, the UI's
   round trip of ADR 0057 with ADR 0079's state cookie marking a device
   sign-in instead of a loopback. Same callback, ID token checks, tenant
   pin, group mapping, JIT policy and role reconciliation. The IdP needs no
   change. Only then is the code asked for, as GitHub and Microsoft do:
   the page that opens the sign-in looks no code up, and the code of
   `verification_uri_complete` rides through the signed state, unchecked,
   only to fill in the field after the sign-in. The signed-in account is
   sealed into a single-use, CSRF-bound, SameSite=Strict entry form of its
   own purpose, like the consent of ADR 0079 decision 3.
2. **The consent shows the code and asks for it to be confirmed.** The
   device flow's weakness is phishing (RFC 8628 section 5.4): whoever starts
   a sign-in can send the page and the code to someone else, and their
   approval signs the sender's CLI in as them. So a live code leads to a
   consent page that shows the account, the tenant and the code, says to
   cancel a code someone sent, and approves only with a ticked confirmation
   that the terminal shows that code, through the same-origin, CSRF-bound,
   SameSite=Strict, single-use consent form of ADR 0079 decision 3 (sealed
   for its own purpose, so neither consent can stand in for the other). The
   page also says when and from which address the sign-in was started, next
   to the address of the browser approving it (RFC 8628 section 5.4). When
   both are the same private, loopback or unknown address, most likely a
   proxy missing from `server.trusted_proxies`, the comparison says nothing
   and is left out; the server warns at startup when it serves the device
   sign-in with no trusted proxies.
3. **Codes.** The user code is eight letters from twenty consonants (34.6
   bits, no words, nothing read as a digit), drawn without modulo bias and
   typed in any case with or without the dash. Codes are entered only by a
   signed-in person, so a guesser needs an IdP account the tenant pin and
   the role mapping accept, and each account gets ten wrong codes a minute
   on each replica (the budget is in memory, like the other limits). The
   budget is spent before the lookup, in one step under the limiter's lock,
   and given back when the code is live, so concurrent submissions cannot
   overrun it and successes cost nothing. Over a code's ten minutes, with
   the cluster at its cap of 10,000 live codes, an account finds one with
   probability about 1 in 25,600 per replica, and a found code only signs a
   stranger's CLI in as the guesser. No budget is shared between accounts
   or addresses, so nobody can spend someone else's. Taking codes before
   the sign-in would need such a shared budget, since addresses are not
   scarce, and anyone could spend it to refuse everyone. An unknown and a
   malformed code get the same page, and each is audited as the account
   that entered it (`oidc.cli_device_verify`). The page that opens the
   sign-in costs one of ten a minute per address. The device code is 256
   random bits and never printed.
4. **The token route polls.** `POST /api/v2/auth/cli/token` with `grant_type`
   `urn:ietf:params:oauth:grant-type:device_code` answers 400 with
   `authorization_pending`, `slow_down` (the interval grows by five seconds),
   `access_denied` (once), `expired_token`, or `invalid_grant` for any device
   code that is not live, all as problem documents with an `error` member;
   then the token `POST /auth/token` answers. A device code that is not live
   is logged, not audited. Polls have their own budget per address (120 a
   minute), because several CLIs behind one address poll every five seconds
   and the interval of each sign-in is enforced by `slow_down`. The code
   exchange keeps the OIDC routes' budget. Every per-address budget of the
   device sign-in counts an IPv6 address by its /64, each is spent before
   the work it bounds, and the limiter drops buckets whose window ended, so
   anonymous routes cannot grow it without bound.
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
   deleted ten minutes after they expire, by every start and by a sweep
   each minute on every server, so the address a sign-in was started from
   is kept about 21 minutes at most. A start costs one of three a minute
   per address (its /64 for IPv6). In a transaction serialized with every
   other start by an advisory lock, waited for at most two seconds (past
   that the start is refused with 503 rather than queueing connections), it
   is refused when 10,000 sign-ins are live, or five from the same
   requester, counted by a keyed hash of its address: an IPv4 address or an
   IPv6 /56, the block a subscriber is commonly given, so a /48 counts as
   256 requesters and not 65,536. The address itself is kept with the row
   for the consent page.
6. **No token is stored.** The approval records the user and the roles the
   sign-in resolved; the token is minted when the CLI redeems it, with the
   configured TTL, so the database never holds a usable credential. The
   roles are those of the approval: a group change at the IdP in the seconds
   before redemption reaches the token only at the next sign-in, as for any
   token, while each request reloads the roles from the database.
7. **The CLI chooses it when the browser sign-in cannot work.**
   `GET /api/v2/auth/methods` gains `device`. `dexaflow auth login --device`
   asks for it. With no flag, single sign-on offered, a session with no
   browser to open (an SSH session, or Linux or a BSD with neither `DISPLAY`
   nor `WAYLAND_DISPLAY`, and no `BROWSER`), and output to a terminal, the
   CLI uses it when the server serves it and says why; `--sso` keeps the
   browser sign-in. The CLI prints the page and the code, never the device
   code, warns when the page is on another host than the server, and stops
   at `--timeout` or the sign-in's expiry.
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
- The anonymous start remains a denial-of-service lever, bounded but not
  closed. Starting sign-ins from 2,000 or more IPv4 addresses or IPv6 /56s
  can hold the cluster at its cap of live sign-ins for ten minutes at a
  time, refusing new device sign-ins with 503 while the browser sign-in and
  password logins keep working. It shows in the server log; an operator
  facing it rate limits `POST /api/v2/auth/cli/device` at the ingress. The
  code entry has no such lever: it is reached only after single sign-on and
  its budgets are per account.
- A person signs in before seeing whether their code is right, one more
  step than taking the code first; a wrong code asks again on the same
  page without a new sign-in.
- Behind a proxy that is not in `server.trusted_proxies`, every caller has
  the proxy's address: they share the per-address budgets, and the consent
  page cannot say where a sign-in started. The startup warning and the
  operator guide say so.
- `verification_uri_complete` carries the user code in its query, so access
  logs of an ingress or proxy may record it. The code is useless without the
  approving person's sign-in and lives ten minutes, but operators who keep
  such logs should know it is there.
- The device sign-in can be phished like every device grant; the confirmed
  code on the consent page is the defense, and its refusals and approvals
  are in the audit trail.
- The consequence of ADR 0079 that sketched the grant against the IdP's
  device endpoint is replaced by this decision.
