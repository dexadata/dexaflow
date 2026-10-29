---
title: Users, roles and API tokens
linkTitle: Users and tokens
weight: 94
description: Local and single sign-on users, the built-in roles and what each can do, session tokens, and long-lived API tokens for CI and scripts.
---

Every request to the control plane acts as a user of one tenant. What the user
may do is the union of its roles' permissions. A request carries the user's
identity as a short session token, from a sign-in, or as a long-lived API
token the user created, which can only narrow what the user may do.

## Local users and single sign-on users

A **local user** signs in with an email and a password. The first one is the
bootstrap admin, created at boot from `bootstrap.email` and
`bootstrap.password_hash` (or `bootstrap.password`) in tenant `default`; on
Lite it is the admin `dexaflow lite` sets up. An admin creates more:

```sh
dexaflow auth create-user --email ana@example.com --password-stdin \
  --role operator < password.txt
```

or through the API, `POST /api/v2/users` with
`{"email": "...", "password": "...", "roles": ["operator"]}`. The email is
lowercased and must be unique in the tenant; the password must be at least 8
characters; each role must exist in the tenant. Creating users needs
`write:user`, which only `admin` has. `GET /api/v2/users` lists them.

A **single sign-on user** has no password. With OIDC
([Google Workspace](/operate/sso-google-workspace/),
[Entra ID, Okta and others](/operate/sso-other-providers/),
[Cognito](/operate/sso-cognito/)), the user row is created at the first sign-in
when `auth.oidc.jit_provisioning` is on, with the roles that
`auth.oidc.role_mappings` gives the user's groups, or `auth.oidc.default_role`
when none match. With a [trusted issuer](/reference/configuration/#trusted-issuer-bearer-tokens),
the platform in front of Dexaflow creates the user and sets its roles through
the operator service API. While OIDC is on, password sign-in is refused except
for the emails in `auth.oidc.break_glass_emails`.

An admin deactivates or reactivates a user of their tenant with
`PATCH /api/v2/users/{user_id}` and the body `{"is_active": false}` (or
`true`). The admin needs `write:user` and cannot deactivate their own account.
A deactivated user is refused at its next request, and so are its session,
its token renewals and its API tokens; a new sign-in is refused too. The
change is recorded in the [audit log](/operate/audit-log/) as
`user.deactivate` or `user.activate`. Reactivating restores the user with the
roles it had. Changing a local user's roles or password through the API is
not available yet ([#2229](https://github.com/dexadata/dexaflow/issues/2229)).

## Built-in roles

Every tenant has these roles. They are system roles: they cannot be deleted,
and an upgrade does not assign them to anyone.

| Role | Can |
|---|---|
| `viewer` | Read DAGs, runs, task instances, XComs, pools, connections, variables and the configuration. |
| `editor` | What `viewer` can, plus author DAGs and write connections and variables. |
| `operator` | What `editor` can, plus trigger runs, set run and task states, clear tasks, and write pools. |
| `deployer` | Read DAGs and their versions, register versions and set the current version. Nothing else: meant for CI. |
| `admin` | Everything in its tenant, including users, every user's API tokens and the [audit log](/operate/audit-log/). |

Permissions are pairs such as `read:dag` or `write:pool`; a route that a role
lacks the permission for answers `403`. A user with several roles gets every
permission of each.

## Sessions

Signing in, in the UI or with `dexaflow auth login`, gives a session token
that lives `auth.jwt.token_ttl_seconds` (one hour by default). The UI renews
it while it is open, up to `auth.jwt.max_lifetime_seconds` (one day by default)
after the first sign-in; then the user signs in again.

`dexaflow auth login` saves the session for the server it signed in to, and
the other commands use it. With single sign-on it opens the browser, or prints
a code to confirm on another device with `--device`. To sign in with a
password from a script, pass the credentials in the environment rather than on
the command line:

```sh
export DEXAFLOW_USERNAME=ana@example.com
export DEXAFLOW_PASSWORD=...
dexaflow auth login --server https://dexaflow.example.com
```

A password is not sent over plain `http` to a server that is not on the same
machine unless you pass `--allow-insecure-password`.

## API tokens

An API token is a long-lived credential a user creates for CI or a script. It
starts with `dxf_`, is shown once, and the server keeps only its SHA-256 hash.
It acts as its owner, with the owner's roles read again on every request, and
its scope narrows them:

- `deploy` reads DAGs and their versions, registers versions and sets the
  current version. Everything else is refused, whatever the owner's roles.
  This is the token for a CI pipeline.
- `read` reads everything its owner can read.

Create one while signed in:

```sh
dexaflow auth create-token --scope deploy --name github-my-dags --ttl 90d
```

or with `POST /api/v2/apiTokens` and
`{"name": "github-my-dags", "scope": "deploy", "expires_in_seconds": 7776000}`.
The owner's roles must allow what the scope does, or the create answers `403`.

- **Lifetime.** 90 days by default, from one hour to 365 days. An expired
  token answers `401`.
- **Listing.** `dexaflow auth tokens list`, `GET /api/v2/apiTokens`, or
  **User > API tokens** in the UI show the name, scope, owner, expiry and last
  use, never the token. A user sees its own tokens; an admin sees every token
  of the tenant.
- **Revoking.** `dexaflow auth tokens revoke <id>`,
  `DELETE /api/v2/apiTokens/{id}`, or **Revoke** in the UI stops the token on
  its next request. An admin can revoke any token of the tenant.
- **Limits.** A user holds at most 50 live tokens, with names unique among
  them. A token cannot create, list or revoke tokens, and it works only in the
  `Authorization` header, never as a browser session.
- **Audit.** Creating, revoking and retiring a token are recorded in the
  [audit log](/operate/audit-log/), with the token's id and name.
- **Turned off.** `auth.api_tokens.disabled: true` stops serving the token
  routes and screen and refuses every API token, for platforms that decide
  access at each sign-in.

The [CI deploy guide](/operate/cicd-deploy/#credentials-for-the-control-plane)
walks through a service user with the `deployer` role and its deploy token. A
CI job can also skip stored tokens and exchange its own identity token
([CI OIDC](/operate/cicd-deploy/#deploying-without-a-stored-token-ci-oidc)).

## Tokens from a trusted issuer

A platform in front of Dexaflow can send its own signed tokens as bearer
tokens. Their scopes (`dexaflow:read`, `dexaflow:run`, `dexaflow:deploy`)
narrow the user's roles the same way. The
[configuration reference](/reference/configuration/#trusted-issuer-bearer-tokens)
lists what each scope grants.
