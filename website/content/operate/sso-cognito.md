---
title: Single sign-on with Amazon Cognito
linkTitle: SSO (Amazon Cognito)
weight: 97
description: Turn on OIDC login against a Cognito user pool, including the three things that differ from every other provider.
---

Cognito is worth its own page for a reason that is not technical: it is how many
organizations federate an IdP **they do not control**. If the Google or Entra app
registration belongs to another team, adding a redirect URL is a ticket, while a
Cognito app client is yours to create in minutes. So Cognito is often not a
choice about identity at all, it is a choice about who has to approve a change.

Everything in [SSO with Entra, Okta, or another OIDC
provider](/operate/sso-other-providers/) applies. Three things are specific to
Cognito, and none of them is guessable from the configuration reference.

## 1. There is no `hd` equivalent: pin on `iss`

A Cognito user-pool ID token carries no claim identifying the upstream domain.
If Google is federated behind the pool, the token you receive is Cognito's, not
Google's, and nothing in it says `example.com`.

The instinct is to pin on `aud`, since it is the only claim that identifies
anything. **Do not.** `aud` is already validated as the audience against your
client ID before the tenant pin runs, so pinning on it reproves what has been
proven and leaves a setting that looks like an access control and is not one.

Pin on the issuer instead:

```yaml
tenantClaim: iss
tenantClaims:
  "https://cognito-idp.us-east-1.amazonaws.com/us-east-1_aBcDeFgHi": "default"
```

The issuer is unique per user pool and is already pinned separately by
`auth.oidc.issuer`, so this pin is **redundant with the issuer check rather than
weaker than it**, which is the honest description and the same advice this
project gives for Okta and Keycloak.

**What actually bounds who can log in, then:** the issuer pin, the audience
check, `email_verified`, and `allowedEmailDomains`. On a pool that federates
several upstream IdPs, or that allows self-registration, `allowedEmailDomains`
is the setting doing the work, not the tenant pin. Set it.

## 2. `groupsClaim` must be `cognito:groups`

Cognito names the claim `cognito:groups`. The default is `groups`, which Cognito
never emits, so `roleMappings` matches nothing and every login resolves to zero
roles.

```yaml
groupsClaim: "cognito:groups"
```

Roles are IdP-authoritative and reconciled to exactly the resolved set, so a
login that resolves to zero roles **removes** the grants the user already had.
The server warns at boot when `roleMappings` is set and `defaultRole` is not,
which is the shape this mistake produces, but the warning names `default_role`
and not the claim. If you see it on Cognito, check `groupsClaim` first.

Federated users are not placed in Cognito groups automatically. If your users
arrive through an upstream IdP, either map them into groups with a pre-token
generation Lambda, or leave `roleMappings` empty and use `defaultRole`.

## 3. The issuer and the login page are on different hostnames

This one costs people an afternoon because it looks like a misconfiguration.

- **Issuer**: `https://cognito-idp.<region>.amazonaws.com/<poolId>`
- **Authorization endpoint**: your pool's hosted UI domain, for example
  `https://<prefix>.auth.<region>.amazoncognito.com/oauth2/authorize`

They are supposed to differ. Dexaflow pins only the **issuer**, and discovers the
authorization endpoint from the pool's discovery document, so the split is
handled. Put the `cognito-idp` URL in `auth.oidc.issuer` and do not try to make
the two agree.

Your redirect URL goes in the app client's **Allowed callback URLs**, and must be
the browser's URL for the control plane plus `/api/v2/auth/oidc/callback`.

## A full values block

```yaml
auth:
  oidc:
    enabled: true
    issuer: https://cognito-idp.us-east-1.amazonaws.com/us-east-1_aBcDeFgHi
    clientId: "<app client id>"
    existingSecret: cognito-oidc      # key: oidcClientSecret
    redirectUrl: https://leoflow.example.com/api/v2/auth/oidc/callback

    tenantClaim: iss
    tenantClaims:
      "https://cognito-idp.us-east-1.amazonaws.com/us-east-1_aBcDeFgHi": "default"

    groupsClaim: "cognito:groups"
    defaultRole: viewer

    allowedEmailDomains:
      - corp.example

    breakGlassEmails:
      - admin@leoflow.local
```

Cognito lets you create an app client with or without a secret. If yours has one
it is a confidential client, so `clientSecret`/`existingSecret` is required:
without it the authorization-code exchange is rejected with `invalid_client`.
The server warns at boot when it is empty; ignore the warning if you
deliberately created a public client (no secret) instead.

## When it does not work

Two behaviors are worth relying on before you start guessing.

**Discovery is checked at boot, before the HTTP listener binds.** If the issuer
is wrong or unreachable, the pod fails to start and says so, naming the issuer.
That means a boot failure is never an IdP problem you have to go and confirm: it
is either the URL or the network path to it.

**Every denied login writes one WARN and one audit row.** So the absence of a
denial line is itself evidence: if a login fails and nothing was logged, the
request never reached the callback, and the problem is in front of Dexaflow.

The [audit reason table](/operate/sso-google-workspace/#when-a-login-is-denied)
decodes the rest.
