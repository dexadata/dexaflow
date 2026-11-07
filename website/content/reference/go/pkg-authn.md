---
title: "pkg/authn"
linkTitle: "pkg/authn"
weight: 19
---

```go
import "github.com/dexadata/dexaflow/pkg/authn"
```

Package authn is the contract of the two sign\-in seams a program built on pkg/server can replace \(ADR 0099\): the [Authenticator](<#Authenticator>) that checks the token of each API request and issues one for a username and password, and the [TrustedIssuer](<#TrustedIssuer>) that verifies the handoff token another platform posts to open a UI session.

A replaced component never decides more than it is given here. The server keeps minting its own session, agent and API tokens, keeps authorizing every request by the [User](<#User>)'s roles and permissions, and clears the fields of a User that only it may set \(see [User](<#User>)\).

Everything exported here follows the pkg/ compatibility rule of ADR 0099: no breaking change in a patch release.

## Index

- [Variables](<#variables>)
- [type Authenticator](<#Authenticator>)
- [type Credentials](<#Credentials>)
- [type Identity](<#Identity>)
- [type Permission](<#Permission>)
- [type TrustedIssuer](<#TrustedIssuer>)
- [type User](<#User>)
  - [func \(u \*User\) HasPermission\(action, resource string\) bool](<#User.HasPermission>)
  - [func \(u \*User\) HasScope\(scope string\) bool](<#User.HasScope>)


## Variables

<a name="ErrInvalidCredentials"></a>ErrInvalidCredentials is the error, or the wrapped error, IssueToken returns for a username and password it refuses. The API answers 401 and counts the attempt toward the login rate limit; any other error is an outage and answers 503.

```go
var ErrInvalidCredentials = errors.New("invalid credentials")
```

<a name="ErrInvalidToken"></a>ErrInvalidToken is the error, or the wrapped error, Authenticate returns for a token that is malformed, expired or not its own. The API then tries the request's other credentials and answers 401 if none authenticates; any other error means the token could not be checked, and answers 503.

```go
var ErrInvalidToken = errors.New("invalid token")
```

<a name="ErrIssuerUnavailable"></a>ErrIssuerUnavailable is the error, or the wrapped error, a [TrustedIssuer](<#TrustedIssuer>) returns from Verify when it could not check the token at all, such as when the issuer's signing keys cannot be downloaded. The API answers 503 and does not record a failed sign\-in; any other error answers 401.

```go
var ErrIssuerUnavailable = errors.New("trusted issuer unavailable")
```

<a name="Authenticator"></a>
## type [Authenticator](<https://github.com/dexadata/dexaflow/blob/main/pkg/authn/authn.go#L123-L126>)

Authenticator checks the token of a request and issues tokens for a username and password.

Authenticate receives the bearer of the Authorization header, or the session cookie. It returns the principal the token stands for, or an error wrapping [ErrInvalidToken](<#ErrInvalidToken>) when the token is not one it recognizes, so the server can try its own tokens next. IssueToken checks creds and returns a token Authenticate accepts; the API hands it to the client and sets it as the session cookie of a browser sign\-in.

```go
type Authenticator interface {
    Authenticate(ctx context.Context, token string) (*User, error)
    IssueToken(ctx context.Context, creds Credentials) (string, error)
}
```

<a name="Credentials"></a>
## type [Credentials](<https://github.com/dexadata/dexaflow/blob/main/pkg/authn/authn.go#L108-L112>)

Credentials are what POST /api/v2/auth/token was sent: the tenant name \("default" when the request named none\), the username and the password. The tenant and the username are trimmed of surrounding space; the password never is.

```go
type Credentials struct {
    Tenant   string
    Username string
    Password string
}
```

<a name="Identity"></a>
## type [Identity](<https://github.com/dexadata/dexaflow/blob/main/pkg/authn/authn.go#L129-L144>)

Identity is what a trusted issuer's token says about its holder.

```go
type Identity struct {
    // Subject is the holder's id at the issuer; a user is linked to the
    // issuer under it.
    Subject string
    // Email is the holder's email, for logs and the audit trail.
    Email string
    // Tenant is the name of the tenant the token is for. The linked user
    // must belong to it.
    Tenant string
    // Scopes are a bearer token's OAuth scopes (ADR 0067). The handoff
    // ignores them: the session it opens is not scoped.
    Scopes []string
    // DeployAudience is set by the built-in verifier for a bearer minted for
    // auth.trusted_issuer.deploy_audience. The handoff ignores it.
    DeployAudience bool
}
```

<a name="Permission"></a>
## type [Permission](<https://github.com/dexadata/dexaflow/blob/main/pkg/authn/authn.go#L44-L47>)

Permission is an action on a resource, such as \{Action: "read", Resource: "dag"\}. The action "admin" grants every action on its resource, and the resource "\*" every resource.

```go
type Permission struct {
    Action   string `json:"action"`
    Resource string `json:"resource"`
}
```

<a name="TrustedIssuer"></a>
## type [TrustedIssuer](<https://github.com/dexadata/dexaflow/blob/main/pkg/authn/authn.go#L150-L157>)

TrustedIssuer verifies the handoff tokens of the operator's trusted issuer \(POST /api/v2/auth/session\). A verified token opens a UI session for the active user of the identity's tenant that is linked to Provider under the identity's Subject. A token never creates a user and never brings roles.

```go
type TrustedIssuer interface {
    // Provider is the key the issuer's users are linked under.
    Provider() string
    // Verify checks a token and returns the identity it carries. A token
    // it refuses returns an error, and one it could not check an error
    // wrapping [ErrIssuerUnavailable].
    Verify(ctx context.Context, raw string) (*Identity, error)
}
```

<a name="User"></a>
## type [User](<https://github.com/dexadata/dexaflow/blob/main/pkg/authn/authn.go#L51-L83>)

User is an authenticated principal: who it is, the tenant it acts in, and what it may do there.

```go
type User struct {
    // ID is the user's id. A user the server knows is found by it.
    ID  string
    // TenantID names the tenant the user acts in: the tenant's name, such
    // as "default", which is what the server's sessions carry. A user
    // without one is refused. When auth.trusted_issuer.host_pattern binds
    // hosts to tenants, its session cookie works only on its tenant's host.
    TenantID string
    // Email is shown in the UI and the audit log.
    Email string
    // Roles are role names; "admin" grants everything in the tenant.
    Roles []string
    // Permissions are what the roles grant.
    Permissions []Permission
    // Scoped marks a principal authenticated by a client token that carries
    // OAuth scopes (ADR 0067): the scopes then narrow what its roles allow.
    // Only the server sets it: it is cleared on a User a replaced
    // Authenticator returns.
    Scoped bool
    // Scopes are the scopes a Scoped principal's token carries. Cleared with
    // Scoped.
    Scopes []string
    // DeployAudience marks a principal authenticated by a trusted-issuer
    // bearer minted for auth.trusted_issuer.deploy_audience. Only the server
    // sets it: it is cleared on a User a replaced Authenticator returns.
    DeployAudience bool
    // SessionsValidAfter is when the user was last deactivated, zero if
    // never. A session that began at or before it is refused, so
    // reactivating a user does not bring back the sessions it had. Only the
    // server sets it: it is cleared on a User a replaced Authenticator
    // returns.
    SessionsValidAfter time.Time
}
```

<a name="User.HasPermission"></a>
### func \(\*User\) [HasPermission](<https://github.com/dexadata/dexaflow/blob/main/pkg/authn/authn.go#L92>)

```go
func (u *User) HasPermission(action, resource string) bool
```

HasPermission reports whether the user may perform action on resource. The admin role, or an admin action or wildcard resource permission, grants it.

<details><summary>Example</summary>
<p>

The server authorizes a request by the user's roles and permissions: the admin role grants everything, an admin action every action on its resource, and the resource "\*" every resource.

```go
package main

import (
	"fmt"

	"github.com/dexadata/dexaflow/pkg/authn"
)

func main() {
	viewer := &authn.User{Permissions: []authn.Permission{{Action: "read", Resource: "dag"}}}
	operator := &authn.User{Permissions: []authn.Permission{{Action: "admin", Resource: "dag_run"}}}
	admin := &authn.User{Roles: []string{"admin"}}

	fmt.Println(viewer.HasPermission("read", "dag"), viewer.HasPermission("edit", "dag"))
	fmt.Println(operator.HasPermission("delete", "dag_run"), operator.HasPermission("read", "dag"))
	fmt.Println(admin.HasPermission("delete", "connection"))
}
```

#### Output

```
true false
true false
true
```

</p>
</details>

<a name="User.HasScope"></a>
### func \(\*User\) [HasScope](<https://github.com/dexadata/dexaflow/blob/main/pkg/authn/authn.go#L86>)

```go
func (u *User) HasScope(scope string) bool
```

HasScope reports whether the principal's token carries scope.

Generated by [gomarkdoc](<https://github.com/princjef/gomarkdoc>)
