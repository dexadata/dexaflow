---
title: "internal/auth"
linkTitle: "internal/auth"
weight: 8
---

```go
import "github.com/dexadata/dexaflow/internal/auth"
```

Package auth provides JWT authentication, password hashing, the RBAC permission model, and login rate limiting for the control plane \(ADR 0008\).

## Index

- [Constants](<#constants>)
- [Variables](<#variables>)
- [func HashPassword\(password string\) \(string, error\)](<#HashPassword>)
- [func MintUserToken\(secret string, ttl time.Duration, user User\) \(string, error\)](<#MintUserToken>)
- [func VerifyPassword\(hash, password string\) bool](<#VerifyPassword>)
- [type AgentIdentity](<#AgentIdentity>)
- [type Authenticator](<#Authenticator>)
- [type Credentials](<#Credentials>)
- [type JWTAuthenticator](<#JWTAuthenticator>)
  - [func NewJWTAuthenticator\(store UserStore, secret string, ttl time.Duration\) \*JWTAuthenticator](<#NewJWTAuthenticator>)
  - [func \(a \*JWTAuthenticator\) Authenticate\(ctx context.Context, token string\) \(\*User, error\)](<#JWTAuthenticator.Authenticate>)
  - [func \(a \*JWTAuthenticator\) AuthenticateAgent\(token string\) \(\*AgentIdentity, error\)](<#JWTAuthenticator.AuthenticateAgent>)
  - [func \(a \*JWTAuthenticator\) IssueAgentToken\(id AgentIdentity, ttl time.Duration\) \(string, error\)](<#JWTAuthenticator.IssueAgentToken>)
  - [func \(a \*JWTAuthenticator\) IssueToken\(ctx context.Context, creds Credentials\) \(string, error\)](<#JWTAuthenticator.IssueToken>)
  - [func \(a \*JWTAuthenticator\) RenewAgentToken\(token string, ttl, maxLifetime time.Duration\) \(renewed string, ok bool, err error\)](<#JWTAuthenticator.RenewAgentToken>)
  - [func \(a \*JWTAuthenticator\) RenewUserToken\(token string, ttl, maxLifetime time.Duration\) \(renewed string, ok bool, err error\)](<#JWTAuthenticator.RenewUserToken>)
- [type Permission](<#Permission>)
- [type RateLimiter](<#RateLimiter>)
  - [func NewRateLimiter\(limit int, window time.Duration\) \*RateLimiter](<#NewRateLimiter>)
  - [func \(r \*RateLimiter\) Allow\(key string\) bool](<#RateLimiter.Allow>)
  - [func \(r \*RateLimiter\) Blocked\(key string\) bool](<#RateLimiter.Blocked>)
- [type User](<#User>)
  - [func \(u \*User\) HasPermission\(action, resource string\) bool](<#User.HasPermission>)
- [type UserStore](<#UserStore>)


## Constants

<a name="DevTokenSubject"></a>DevTokenSubject is the subject of the in\-process token that \`leoflow dev\` mints for its admin. It intentionally has no user row, so Authenticate trusts its signed claims as the ONLY subject exempt from the per\-request DB authz reload.

```go
const DevTokenSubject = "leoflow-dev"
```

<a name="ScopeWarmWorker"></a>ScopeWarmWorker marks an agent token that authorizes ONLY the warm\-worker control channel — Register \+ AwaitAssignment — and NOTHING that resolves secrets or a task \(ADR 0058 D2\). It is the value of AgentIdentity.Scope for a warm worker's bootstrap credential; the empty scope is the default task credential, which resolves secrets exactly as before. The control plane gates every secret/task RPC on this value \(see agentrpc.requireAttemptToken\).

```go
const ScopeWarmWorker = "warm-worker"
```

## Variables

<a name="ErrInvalidCredentials"></a>ErrInvalidCredentials is returned when a username/password pair is rejected.

```go
var ErrInvalidCredentials = errors.New("invalid credentials")
```

<a name="ErrInvalidToken"></a>ErrInvalidToken is returned when a token is malformed, expired, or unsigned by us.

```go
var ErrInvalidToken = errors.New("invalid token")
```

<a name="ErrUserNotFound"></a>ErrUserNotFound is returned by UserStore.FindUserByID when no user has the given id. Authenticate treats it as a signal to trust the token's signed claims \(the in\-process minting path has no backing row\), distinct from a store failure, which fails closed.

```go
var ErrUserNotFound = errors.New("user not found")
```

<a name="HashPassword"></a>
## func [HashPassword](<https://github.com/dexadata/leoflow/blob/main/internal/auth/password.go#L13>)

```go
func HashPassword(password string) (string, error)
```

HashPassword hashes a plaintext password with bcrypt.

<a name="MintUserToken"></a>
## func [MintUserToken](<https://github.com/dexadata/leoflow/blob/main/internal/auth/jwt.go#L69>)

```go
func MintUserToken(secret string, ttl time.Duration, user User) (string, error)
```

MintUserToken signs a user JWT directly, without checking credentials against a store. It is for trusted in\-process callers only — notably \`leoflow dev\`, which runs its own control plane and must register DAGs without a login round\-trip. The token validates under Authenticate using the same secret.

<a name="VerifyPassword"></a>
## func [VerifyPassword](<https://github.com/dexadata/leoflow/blob/main/internal/auth/password.go#L22>)

```go
func VerifyPassword(hash, password string) bool
```

VerifyPassword reports whether password matches the stored bcrypt hash.

<a name="AgentIdentity"></a>
## type [AgentIdentity](<https://github.com/dexadata/leoflow/blob/main/internal/auth/agent_token.go#L29-L47>)

AgentIdentity is the identity a verified agent token represents. By default \(Scope == ""\) it is a single task instance — the task\-scoped credential that resolves secrets. When Scope == ScopeWarmWorker it is instead a warm worker's bootstrap credential, which names its dag\_version pool \(DagVersionID\) and its worker id \(WorkerID, the token Subject\) and carries NO task claims.

```go
type AgentIdentity struct {
    TaskInstanceID string
    TenantID       string
    DagID          string
    RunID          string
    TaskID         string
    TryNumber      int
    // Scope is "" for a task credential (the default, byte-compatible with every
    // token minted before this field existed) or ScopeWarmWorker for a warm
    // worker's control-channel-only bootstrap credential.
    Scope string
    // DagVersionID names the warm pool a warm-worker credential serves. Empty on a
    // task credential.
    DagVersionID string
    // WorkerID is the warm worker's stable identifier (its pod name), minted as the
    // token Subject for a warm-worker credential. Empty on a task credential (whose
    // Subject is TaskInstanceID instead).
    WorkerID string
}
```

<a name="Authenticator"></a>
## type [Authenticator](<https://github.com/dexadata/leoflow/blob/main/internal/auth/auth.go#L62-L65>)

Authenticator issues and validates authentication tokens. The MVP ships a JWT implementation; the interface keeps OIDC/LDAP pluggable \(ADR 0008\).

```go
type Authenticator interface {
    Authenticate(ctx context.Context, token string) (*User, error)
    IssueToken(ctx context.Context, creds Credentials) (string, error)
}
```

<a name="Credentials"></a>
## type [Credentials](<https://github.com/dexadata/leoflow/blob/main/internal/auth/auth.go#L54-L58>)

Credentials are the inputs to token issuance.

```go
type Credentials struct {
    Tenant   string
    Username string
    Password string
}
```

<a name="JWTAuthenticator"></a>
## type [JWTAuthenticator](<https://github.com/dexadata/leoflow/blob/main/internal/auth/jwt.go#L39-L47>)

JWTAuthenticator issues and validates HS256 JWTs against a UserStore.

```go
type JWTAuthenticator struct {
    // contains filtered or unexported fields
}
```

<a name="NewJWTAuthenticator"></a>
### func [NewJWTAuthenticator](<https://github.com/dexadata/leoflow/blob/main/internal/auth/jwt.go#L51>)

```go
func NewJWTAuthenticator(store UserStore, secret string, ttl time.Duration) *JWTAuthenticator
```

NewJWTAuthenticator builds a JWTAuthenticator with the given user store, HS256 secret, and token lifetime.

<a name="JWTAuthenticator.Authenticate"></a>
### func \(\*JWTAuthenticator\) [Authenticate](<https://github.com/dexadata/leoflow/blob/main/internal/auth/jwt.go#L103>)

```go
func (a *JWTAuthenticator) Authenticate(ctx context.Context, token string) (*User, error)
```

Authenticate validates a bearer token and resolves the current principal. After the signature and registered claims check out, it reloads the user's roles, permissions, and active flag from the store keyed by the token subject \(the user id\). The store — not the token — is the source of truth for authorization: this is what makes non\-admin role grants gate anything \(the token carries roles but never permissions\) and lets deactivating a user revoke their live tokens within the token TTL.

Two cases fall back to the signed claims instead of the reload: a nil store \(no data plane bound — the trusted in\-process minting context\) and a subject with no backing row \(a directly\-minted token, e.g. \`leoflow dev\`\). Any other store failure fails closed, so a flaky database cannot silently disable revocation.

<a name="JWTAuthenticator.AuthenticateAgent"></a>
### func \(\*JWTAuthenticator\) [AuthenticateAgent](<https://github.com/dexadata/leoflow/blob/main/internal/auth/agent_token.go#L162>)

```go
func (a *JWTAuthenticator) AuthenticateAgent(token string) (*AgentIdentity, error)
```

AuthenticateAgent validates an agent bearer token and returns the task instance it identifies.

<a name="JWTAuthenticator.IssueAgentToken"></a>
### func \(\*JWTAuthenticator\) [IssueAgentToken](<https://github.com/dexadata/leoflow/blob/main/internal/auth/agent_token.go#L76>)

```go
func (a *JWTAuthenticator) IssueAgentToken(id AgentIdentity, ttl time.Duration) (string, error)
```

IssueAgentToken mints a signed token that identifies a single task instance, valid for the given TTL. The control plane passes it to the worker pod.

The token's origin \(oiat\) is set to the mint time — this is a fresh dispatch. Renewal \(RenewAgentToken\) preserves that origin instead of resetting it.

<a name="JWTAuthenticator.IssueToken"></a>
### func \(\*JWTAuthenticator\) [IssueToken](<https://github.com/dexadata/leoflow/blob/main/internal/auth/jwt.go#L75>)

```go
func (a *JWTAuthenticator) IssueToken(ctx context.Context, creds Credentials) (string, error)
```

IssueToken validates the credentials against the store and returns a signed JWT.

<a name="JWTAuthenticator.RenewAgentToken"></a>
### func \(\*JWTAuthenticator\) [RenewAgentToken](<https://github.com/dexadata/leoflow/blob/main/internal/auth/agent_token.go#L134>)

```go
func (a *JWTAuthenticator) RenewAgentToken(token string, ttl, maxLifetime time.Duration) (renewed string, ok bool, err error)
```

RenewAgentToken validates an in\-flight agent bearer token and re\-mints it for the SAME task instance with a fresh short TTL, refreshing the live window without ever changing what AuthenticateAgent verifies \(signature, issuer, leoflow\-agent audience, HS256\). It is the heartbeat\-driven half of the short\-TTL\-plus\-renewal design \(ADR 0055 Fix \#4\): the short TTL bounds a stolen or finished token, while renewal keeps a genuinely live task's credential working.

The attempt's original dispatch time is preserved across every renewal \(the oiat claim\). maxLifetime is a hard ceiling on that total age: once the attempt has been alive longer than maxLifetime since first dispatch, renewal is refused \(ok=false, empty token\) so a runaway attempt's credential lapses instead of being kept alive forever. A non\-positive maxLifetime disables the ceiling. exp is always now\+ttl — never accumulated onto the previous exp.

An invalid incoming token \(bad signature, wrong audience, expired\) returns an error and is never re\-minted.

<a name="JWTAuthenticator.RenewUserToken"></a>
### func \(\*JWTAuthenticator\) [RenewUserToken](<https://github.com/dexadata/leoflow/blob/main/internal/auth/jwt.go#L185>)

```go
func (a *JWTAuthenticator) RenewUserToken(token string, ttl, maxLifetime time.Duration) (renewed string, ok bool, err error)
```

RenewUserToken validates a still\-valid user bearer token and re\-mints it for the SAME principal \(subject, tenant, email, roles\) with a fresh short TTL, without ever changing what Authenticate verifies \(signature, issuer, leoflow\-user audience, HS256\). It is the server half of transparent CLI token renewal \(EKS validation aresta \#5\): the short access\-token TTL still bounds a stolen token, while renewal keeps a genuinely live session working so a long dev session never has to \`leoflow auth login\` again on the hour. It is modeled directly on RenewAgentToken.

The session's original login time is preserved across every renewal \(the oiat claim, falling back to iat for a token minted before that claim existed\). maxLifetime is a hard ceiling on that total age: once the session has been alive longer than maxLifetime since first login, renewal is refused \(ok=false, empty token, no error\) so the user must re\-authenticate. A non\-positive maxLifetime disables the ceiling. exp is always now\+ttl — never accumulated.

An invalid incoming token \(bad signature, wrong audience, expired\) returns an error and is never re\-minted. Roles are copied from the incoming token, exactly as they were signed; Authenticate still reloads authorization from the store on every request, so a renewed token confers no more than the original did.

<a name="Permission"></a>
## type [Permission](<https://github.com/dexadata/leoflow/blob/main/internal/auth/auth.go#L23-L26>)

Permission is an action on a resource \(e.g. \{Action: "read", Resource: "dag"\}\).

```go
type Permission struct {
    Action   string `json:"action"`
    Resource string `json:"resource"`
}
```

<a name="RateLimiter"></a>
## type [RateLimiter](<https://github.com/dexadata/leoflow/blob/main/internal/auth/ratelimit.go#L10-L16>)

RateLimiter is a per\-key fixed\-window limiter used to throttle failed logins per client IP \(ADR 0008\).

```go
type RateLimiter struct {
    // contains filtered or unexported fields
}
```

<a name="NewRateLimiter"></a>
### func [NewRateLimiter](<https://github.com/dexadata/leoflow/blob/main/internal/auth/ratelimit.go#L24>)

```go
func NewRateLimiter(limit int, window time.Duration) *RateLimiter
```

NewRateLimiter builds a limiter allowing limit events per window per key.

<a name="RateLimiter.Allow"></a>
### func \(\*RateLimiter\) [Allow](<https://github.com/dexadata/leoflow/blob/main/internal/auth/ratelimit.go#L49>)

```go
func (r *RateLimiter) Allow(key string) bool
```

Allow records an event for key and reports whether it is within the limit.

<a name="RateLimiter.Blocked"></a>
### func \(\*RateLimiter\) [Blocked](<https://github.com/dexadata/leoflow/blob/main/internal/auth/ratelimit.go#L38>)

```go
func (r *RateLimiter) Blocked(key string) bool
```

Blocked reports whether key has already reached its limit in the current window, WITHOUT recording an attempt \(a peek\). The login handler uses it to reject an over\-limit caller up front while calling Allow only for actual failures — so a successful login never consumes the budget and a user who mistypes a few times is not locked out the moment they finally get it right.

<a name="User"></a>
## type [User](<https://github.com/dexadata/leoflow/blob/main/internal/auth/auth.go#L29-L35>)

User is an authenticated principal with its tenant, roles, and permissions.

```go
type User struct {
    ID          string
    TenantID    string
    Email       string
    Roles       []string
    Permissions []Permission
}
```

<a name="User.HasPermission"></a>
### func \(\*User\) [HasPermission](<https://github.com/dexadata/leoflow/blob/main/internal/auth/auth.go#L39>)

```go
func (u *User) HasPermission(action, resource string) bool
```

HasPermission reports whether the user may perform action on resource. The admin role, or an admin action / wildcard resource permission, grants access.

<a name="UserStore"></a>
## type [UserStore](<https://github.com/dexadata/leoflow/blob/main/internal/auth/auth.go#L68-L76>)

UserStore loads users for authentication. storage implements it.

```go
type UserStore interface {
    FindUserByLogin(ctx context.Context, tenant, username string) (user *User, passwordHash string, err error)
    // FindUserByID reloads a user's current authorization state (tenant, roles,
    // permissions) by id, along with whether the account is active. It is the
    // per-request source of truth for token validation: it makes non-admin role
    // grants take effect and lets deactivating a user revoke live tokens within
    // the token TTL. It returns ErrUserNotFound when no user has the id.
    FindUserByID(ctx context.Context, id string) (user *User, isActive bool, err error)
}
```

Generated by [gomarkdoc](<https://github.com/princjef/gomarkdoc>)
