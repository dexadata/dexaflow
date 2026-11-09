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
- [func APITokenScopes\(scope string\) \(\[\]string, bool\)](<#APITokenScopes>)
- [func AddressBlock\(ip string, v6Bits int\) string](<#AddressBlock>)
- [func AddressBucket\(ip string\) string](<#AddressBucket>)
- [func HashAPIToken\(token string\) string](<#HashAPIToken>)
- [func HashPassword\(password string\) \(string, error\)](<#HashPassword>)
- [func IsAPIToken\(token string\) bool](<#IsAPIToken>)
- [func IsCISession\(token string\) bool](<#IsCISession>)
- [func MintCISession\(secret string, ttl time.Duration, now time.Time, s CISession\) \(string, time.Time, error\)](<#MintCISession>)
- [func MintUserToken\(secret string, ttl time.Duration, user User\) \(string, error\)](<#MintUserToken>)
- [func NewAPIToken\(\) \(token, hash string, err error\)](<#NewAPIToken>)
- [func VerifyPassword\(hash, password string\) bool](<#VerifyPassword>)
- [type APIToken](<#APIToken>)
- [type APITokenAuthenticator](<#APITokenAuthenticator>)
  - [func NewAPITokenAuthenticator\(next Authenticator, tokens APITokenStore, users UserStore\) \*APITokenAuthenticator](<#NewAPITokenAuthenticator>)
  - [func \(a \*APITokenAuthenticator\) Authenticate\(ctx context.Context, token string\) \(\*User, error\)](<#APITokenAuthenticator.Authenticate>)
  - [func \(a \*APITokenAuthenticator\) IssueToken\(ctx context.Context, creds Credentials\) \(string, error\)](<#APITokenAuthenticator.IssueToken>)
- [type APITokenStore](<#APITokenStore>)
- [type AdminLookup](<#AdminLookup>)
- [type AdminResolution](<#AdminResolution>)
  - [func ResolveAdminEmail\(ctx context.Context, configured string, lookup AdminLookup\) \(AdminResolution, error\)](<#ResolveAdminEmail>)
- [type AdminState](<#AdminState>)
- [type AgentIdentity](<#AgentIdentity>)
- [type AgentTokenRejection](<#AgentTokenRejection>)
  - [func \(r \*AgentTokenRejection\) Error\(\) string](<#AgentTokenRejection.Error>)
- [type Authenticator](<#Authenticator>)
- [type CIIssuerFingerprints](<#CIIssuerFingerprints>)
- [type CISession](<#CISession>)
- [type CISessionAuthenticator](<#CISessionAuthenticator>)
  - [func NewCISessionAuthenticator\(next Authenticator, secret string, issuers CIIssuerFingerprints, roles RolePermissionStore\) \*CISessionAuthenticator](<#NewCISessionAuthenticator>)
  - [func \(a \*CISessionAuthenticator\) Authenticate\(ctx context.Context, token string\) \(\*User, error\)](<#CISessionAuthenticator.Authenticate>)
  - [func \(a \*CISessionAuthenticator\) IssueToken\(ctx context.Context, creds Credentials\) \(string, error\)](<#CISessionAuthenticator.IssueToken>)
- [type Credentials](<#Credentials>)
- [type DeviceAuthorization](<#DeviceAuthorization>)
- [type DeviceAuthorizationRequest](<#DeviceAuthorizationRequest>)
- [type DevicePoll](<#DevicePoll>)
- [type DevicePollOutcome](<#DevicePollOutcome>)
- [type JWTAuthenticator](<#JWTAuthenticator>)
  - [func NewJWTAuthenticator\(store UserStore, secret string, ttl time.Duration\) \*JWTAuthenticator](<#NewJWTAuthenticator>)
  - [func \(a \*JWTAuthenticator\) Authenticate\(ctx context.Context, token string\) \(\*User, error\)](<#JWTAuthenticator.Authenticate>)
  - [func \(a \*JWTAuthenticator\) AuthenticateAgent\(token string\) \(\*AgentIdentity, error\)](<#JWTAuthenticator.AuthenticateAgent>)
  - [func \(a \*JWTAuthenticator\) IssueAgentToken\(id AgentIdentity, ttl time.Duration\) \(string, error\)](<#JWTAuthenticator.IssueAgentToken>)
  - [func \(a \*JWTAuthenticator\) IssueToken\(ctx context.Context, creds Credentials\) \(string, error\)](<#JWTAuthenticator.IssueToken>)
  - [func \(a \*JWTAuthenticator\) RenewAgentToken\(token string, ttl, maxLifetime time.Duration\) \(renewed string, ok bool, err error\)](<#JWTAuthenticator.RenewAgentToken>)
  - [func \(a \*JWTAuthenticator\) RenewUserToken\(ctx context.Context, token string, ttl, maxLifetime time.Duration\) \(renewed string, ok bool, err error\)](<#JWTAuthenticator.RenewUserToken>)
- [type Permission](<#Permission>)
- [type RateLimiter](<#RateLimiter>)
  - [func NewRateLimiter\(limit int, window time.Duration\) \*RateLimiter](<#NewRateLimiter>)
  - [func \(r \*RateLimiter\) Allow\(key string\) bool](<#RateLimiter.Allow>)
  - [func \(r \*RateLimiter\) Blocked\(key string\) bool](<#RateLimiter.Blocked>)
  - [func \(r \*RateLimiter\) Refund\(key string\)](<#RateLimiter.Refund>)
  - [func \(r \*RateLimiter\) Share\(counter SharedCounter, scope string\) \*RateLimiter](<#RateLimiter.Share>)
- [type RolePermissionStore](<#RolePermissionStore>)
- [type SharedCounter](<#SharedCounter>)
- [type User](<#User>)
- [type UserStore](<#UserStore>)


## Constants

<a name="AgentTokenExpired"></a>Reasons an agent bearer token is rejected \(\#1125\). They are a closed set so a log line or a metric label built from one never carries anything derived from the token itself. Each points the operator at a different fault:

- expired: the credential lapsed. A running attempt renews on every heartbeat, so this means the agent could not reach the control plane for a whole TTL, or its pod started after the credential minted at dispatch had already run out.
- wrong\_audience: a token minted for another surface \(a user token\) reached the agent service: a wiring bug.
- bad\_signature: the token was signed with another key \(key rotation, or a token from another install\) or with a method the server does not accept.
- wrong\_issuer, not\_yet\_valid, malformed: the token is not one this control plane minted as it stands.
- invalid: any other verification failure.

```go
const (
    AgentTokenExpired       = "expired"
    AgentTokenWrongAudience = "wrong_audience"
    AgentTokenBadSignature  = "bad_signature"
    AgentTokenWrongIssuer   = "wrong_issuer"
    AgentTokenNotYetValid   = "not_yet_valid"
    AgentTokenMalformed     = "malformed"
    AgentTokenInvalid       = "invalid"
)
```

<a name="ScopeRead"></a>The scopes a trusted\-issuer bearer token may carry \(ADR 0067\). No scope implies another.

```go
const (
    // ScopeRead grants the read routes.
    ScopeRead = "dexaflow:read"
    // ScopeRun grants run-state writes: trigger, clear, mark, pause.
    ScopeRun = "dexaflow:run"
    // ScopeDeploy grants registering a DAG version.
    ScopeDeploy = "dexaflow:deploy"
)
```

<a name="APITokenPrefix"></a>APITokenPrefix starts every API token \(ADR 0074\). It tells an API token apart from a JWT without parsing it, and lets secret scanners recognize a leaked one.

```go
const APITokenPrefix = "dxf_"
```

<a name="CISessionPrefix"></a>CISessionPrefix starts every CI OIDC session token \(ADR 0092\). It tells one apart from a user JWT and an API token without parsing it.

```go
const CISessionPrefix = "dxci_"
```

<a name="DefaultAdminEmail"></a>DefaultAdminEmail is the email of the bootstrap admin a new install creates: a fresh Lite setup, and a server booted with a bootstrap password and no bootstrap email.

```go
const DefaultAdminEmail = "admin@dexaflow.local"
```

<a name="DevTokenSubject"></a>DevTokenSubject is the subject of the in\-process token that \`dexaflow lite\` mints for its admin. It intentionally has no user row, so Authenticate trusts its signed claims as the ONLY subject exempt from the per\-request DB authz reload.

```go
const DevTokenSubject = "leoflow-dev"
```

<a name="LegacyAdminEmail"></a>LegacyAdminEmail is the bootstrap admin email of installs created before the rename to Dexaflow. Those installs keep their admin under this address; see ResolveAdminEmail.

```go
const LegacyAdminEmail = "admin@leoflow.local"
```

<a name="ScopeWarmWorker"></a>ScopeWarmWorker marks an agent token that authorizes ONLY the warm\-worker control channel — Register \+ AwaitAssignment — and NOTHING that resolves secrets or a task \(ADR 0058 D2\). It is the value of AgentIdentity.Scope for a warm worker's bootstrap credential; the empty scope is the default task credential, which resolves secrets exactly as before. The control plane gates every secret/task RPC on this value \(see agentrpc.requireAttemptToken\).

```go
const ScopeWarmWorker = "warm-worker"
```

## Variables

<a name="ErrDeviceSignInsFull"></a>The refusals of a start over a cap.

```go
var (
    // ErrDeviceSignInsFull: the cluster holds as many sign-ins in progress
    // as it allows.
    ErrDeviceSignInsFull = errors.New("too many device sign-ins are in progress")
    // ErrDeviceSignInsFromRequester: the starting address holds as many
    // sign-ins in progress as one address may.
    ErrDeviceSignInsFromRequester = errors.New("too many device sign-ins are in progress from this address")
    // ErrDeviceStartsBusy: the start could not take its turn among
    // concurrent starts in time, so it was not recorded.
    ErrDeviceStartsBusy = errors.New("device sign-in starts are busy")
)
```

<a name="ErrAPITokenNotFound"></a>ErrAPITokenNotFound is returned by APITokenStore.FindAPITokenByHash when no token has the given hash.

```go
var ErrAPITokenNotFound = errors.New("api token not found")
```

<a name="ErrInvalidCredentials"></a>ErrInvalidCredentials is returned when a username/password pair is rejected. It is the public contract's sentinel \(ADR 0099\).

```go
var ErrInvalidCredentials = authn.ErrInvalidCredentials
```

<a name="ErrInvalidToken"></a>ErrInvalidToken is returned when a token is malformed, expired, or unsigned by us. It is the public contract's sentinel \(ADR 0099\).

```go
var ErrInvalidToken = authn.ErrInvalidToken
```

<a name="ErrRoleNotFound"></a>ErrRoleNotFound is returned by RolePermissionStore.RolePermissions when the tenant has no role of that name, or there is no such tenant.

```go
var ErrRoleNotFound = errors.New("role not found")
```

<a name="ErrTenantlessToken"></a>ErrTenantlessToken is joined with ErrInvalidToken when a token is trusted from its signed claims but names no tenant: such a principal cannot be scoped to any tenant's data, so it is refused rather than given a default one.

```go
var ErrTenantlessToken = errors.New("token names no tenant")
```

<a name="ErrUserNotFound"></a>ErrUserNotFound is returned by UserStore.FindUserByID when no user has the given id. Authenticate treats it as a signal to trust the token's signed claims \(the in\-process minting path has no backing row\), distinct from a store failure, which fails closed.

```go
var ErrUserNotFound = errors.New("user not found")
```

<a name="APITokenScopes"></a>
## func [APITokenScopes](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/api_token.go#L67>)

```go
func APITokenScopes(scope string) ([]string, bool)
```

APITokenScopes returns the scopes an API token created for scope carries, and false when scope is not one a token may be created for.

<a name="AddressBlock"></a>
## func [AddressBlock](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/ratelimit.go#L201>)

```go
func AddressBlock(ip string, v6Bits int) string
```

AddressBlock is AddressBucket with the IPv6 prefix length v6Bits: an IPv4 address as is, an IPv6 one by its /v6Bits, anything else as is.

<a name="AddressBucket"></a>
## func [AddressBucket](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/ratelimit.go#L195>)

```go
func AddressBucket(ip string) string
```

AddressBucket is the rate\-limit key of a client address: an IPv4 address \(or an IPv4\-mapped IPv6 one\) as is, and an IPv6 address by its /64, the smallest block one subscriber is usually given, so rotating addresses within it does not buy a fresh budget. Anything that does not parse is its own key.

<a name="HashAPIToken"></a>
## func [HashAPIToken](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/api_token.go#L89>)

```go
func HashAPIToken(token string) string
```

HashAPIToken returns the hex SHA\-256 of token, the only form of it the server keeps. A fast hash is enough: the secret is 256 random bits, so there is no dictionary to try, and the hash is the lookup key.

<a name="HashPassword"></a>
## func [HashPassword](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/password.go#L13>)

```go
func HashPassword(password string) (string, error)
```

HashPassword hashes a plaintext password with bcrypt.

<a name="IsAPIToken"></a>
## func [IsAPIToken](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/api_token.go#L95>)

```go
func IsAPIToken(token string) bool
```

IsAPIToken reports whether token has the API token form.

<a name="IsCISession"></a>
## func [IsCISession](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/ci_session.go#L54>)

```go
func IsCISession(token string) bool
```

IsCISession reports whether token has the CI session form.

<a name="MintCISession"></a>
## func [MintCISession](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/ci_session.go#L60>)

```go
func MintCISession(secret string, ttl time.Duration, now time.Time, s CISession) (string, time.Time, error)
```

MintCISession signs a CI session valid for ttl from now with the engine secret, and returns it with its expiry \(whole seconds, as in the token\).

<a name="MintUserToken"></a>
## func [MintUserToken](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/jwt.go#L69>)

```go
func MintUserToken(secret string, ttl time.Duration, user User) (string, error)
```

MintUserToken signs a user JWT directly, without checking credentials against a store. It is for trusted in\-process callers only, notably \`dexaflow lite\`, which runs its own control plane and must register DAGs without a login round\-trip. The token validates under Authenticate using the same secret.

<a name="NewAPIToken"></a>
## func [NewAPIToken](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/api_token.go#L77>)

```go
func NewAPIToken() (token, hash string, err error)
```

NewAPIToken returns a fresh API token and the hash to store for it. The token is shown to its owner once and never stored.

<a name="VerifyPassword"></a>
## func [VerifyPassword](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/password.go#L22>)

```go
func VerifyPassword(hash, password string) bool
```

VerifyPassword reports whether password matches the stored bcrypt hash.

<a name="APIToken"></a>
## type [APIToken](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/api_token.go#L30-L42>)

APIToken is the stored record of a long\-lived API token \(ADR 0074\). It never holds the token itself, only what describes it.

```go
type APIToken struct {
    ID  string
    // TenantID is the tenant name of the owner, as in User.TenantID.
    TenantID   string
    UserID     string
    UserEmail  string
    Name       string
    Scopes     []string
    CreatedAt  time.Time
    ExpiresAt  time.Time
    LastUsedAt *time.Time
    RevokedAt  *time.Time
}
```

<a name="APITokenAuthenticator"></a>
## type [APITokenAuthenticator](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/api_token.go#L103-L108>)

APITokenAuthenticator authenticates API tokens and hands every other token to the next Authenticator \(ADR 0074\). An API token resolves to its owner, reloaded from the store on every request like a JWT subject, narrowed to the token's scopes.

```go
type APITokenAuthenticator struct {
    // contains filtered or unexported fields
}
```

<a name="NewAPITokenAuthenticator"></a>
### func [NewAPITokenAuthenticator](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/api_token.go#L112>)

```go
func NewAPITokenAuthenticator(next Authenticator, tokens APITokenStore, users UserStore) *APITokenAuthenticator
```

NewAPITokenAuthenticator wraps next so that API tokens are checked against tokens and their owners reloaded from users.

<a name="APITokenAuthenticator.Authenticate"></a>
### func \(\*APITokenAuthenticator\) [Authenticate](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/api_token.go#L126>)

```go
func (a *APITokenAuthenticator) Authenticate(ctx context.Context, token string) (*User, error)
```

Authenticate resolves an API token to its scoped owner, or delegates any other token. A token that is unknown, revoked or expired, or whose owner is missing, inactive or in another tenant, is ErrInvalidToken. A store failure is returned as it is, so the request answers 503 rather than 401.

<a name="APITokenAuthenticator.IssueToken"></a>
### func \(\*APITokenAuthenticator\) [IssueToken](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/api_token.go#L118>)

```go
func (a *APITokenAuthenticator) IssueToken(ctx context.Context, creds Credentials) (string, error)
```

IssueToken delegates to the next authenticator: API tokens are created through the API by a signed\-in user, never from a password.

<a name="APITokenStore"></a>
## type [APITokenStore](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/api_token.go#L46-L54>)

APITokenStore resolves API tokens by the hash of their secret and records their use. storage implements it.

```go
type APITokenStore interface {
    // FindAPITokenByHash returns the token whose secret hashes to hash, revoked
    // and expired ones included, or ErrAPITokenNotFound.
    FindAPITokenByHash(ctx context.Context, hash string) (*APIToken, error)
    // TouchAPIToken records that the token was used at at, unless its last
    // recorded use is less than five minutes before at, and reports whether
    // it wrote. The store decides, so concurrent requests write once.
    TouchAPIToken(ctx context.Context, id string, at time.Time) (bool, error)
}
```

<a name="AdminLookup"></a>
## type [AdminLookup](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/admin_email.go#L32>)

AdminLookup reports the AdminState of an email address in the tenant the caller is bootstrapping.

```go
type AdminLookup func(ctx context.Context, email string) (AdminState, error)
```

<a name="AdminResolution"></a>
## type [AdminResolution](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/admin_email.go#L35-L44>)

AdminResolution is the address ResolveAdminEmail chose and why.

```go
type AdminResolution struct {
    // Email is the address the bootstrap admin lives under.
    Email string
    // KeptLegacy reports that Email is LegacyAdminEmail, chosen in place of the
    // default address because the install predates the rename.
    KeptLegacy bool
    // BothExist reports that users exist under both addresses, which the
    // caller should warn about: only Email is bootstrapped.
    BothExist bool
}
```

<a name="ResolveAdminEmail"></a>
### func [ResolveAdminEmail](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/admin_email.go#L58>)

```go
func ResolveAdminEmail(ctx context.Context, configured string, lookup AdminLookup) (AdminResolution, error)
```

ResolveAdminEmail returns the address the bootstrap admin lives under.

An empty configured email means DefaultAdminEmail. Any address other than DefaultAdminEmail is returned unchanged, with no lookup: an operator who named an address gets exactly that address. For DefaultAdminEmail the user holding the admin role wins, the new address first, so an install created before the rename \(or a reinstall over its kept database\) keeps the admin it has instead of gaining a second one, and the bootstrap never writes a password to a non\-admin account at the new address while the legacy admin exists. Without an admin at either address, an existing user wins, the new address first; with no user at all, the new address is created. The existing user is never renamed.

<a name="AdminState"></a>
## type [AdminState](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/admin_email.go#L19>)

AdminState is what the user store knows about one email address.

```go
type AdminState int
```

<a name="UserAbsent"></a>

```go
const (
    // UserAbsent means no user has the address.
    UserAbsent AdminState = iota
    // UserWithoutAdminRole means a user has the address but not the admin role.
    UserWithoutAdminRole
    // UserIsAdmin means a user has the address and holds the admin role.
    UserIsAdmin
)
```

<a name="AgentIdentity"></a>
## type [AgentIdentity](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/agent_token.go#L29-L57>)

AgentIdentity is the identity a verified agent token represents. By default \(Scope == ""\) it is a single task instance — the task\-scoped credential that resolves secrets. When Scope == ScopeWarmWorker it is instead a warm worker's bootstrap credential, which names its dag\_version pool \(DagVersionID\) and its worker id \(WorkerID, the token Subject\) and carries NO task claims.

```go
type AgentIdentity struct {
    TaskInstanceID string
    TenantID       string
    DagID          string
    RunID          string
    TaskID         string
    TryNumber      int
    // AttemptEpoch identifies which execution of TryNumber this credential
    // belongs to (ADR 0051 amendment): an infra re-place, a reschedule poke or a
    // repeated dispatch reuses the try but never the epoch. It is meaningful
    // only when HasAttemptEpoch is set.
    AttemptEpoch int
    // HasAttemptEpoch is the presence bit for AttemptEpoch. It is false for a
    // token minted before the epoch existed (a legacy token, which the fence
    // treats as epoch 0) and for a warm-worker credential, so "absent" stays
    // distinguishable from epoch 0.
    HasAttemptEpoch bool
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

<a name="AgentTokenRejection"></a>
## type [AgentTokenRejection](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/agent_token_rejection.go#L44-L52>)

AgentTokenRejection describes why AuthenticateAgent refused a bearer token. AuthenticateAgent joins it into its error \(alongside ErrInvalidToken\), so a caller recovers it with errors.As.

It never holds the token or any part of it. Identity is set only when the token's signature, issuer and audience all verified and the token failed on time alone \(expired or not yet valid\): its claims are then authentic and name the attempt the credential belonged to. For any other reason the claims are unauthenticated or belong to another surface, so they are not surfaced.

```go
type AgentTokenRejection struct {
    // Reason is one of the AgentToken* reason constants.
    Reason string
    // Identity is the attempt an authentic but out-of-window token belonged to,
    // or nil.
    Identity *AgentIdentity
    // ExpiredAt is when an expired token lapsed; zero for any other reason.
    ExpiredAt time.Time
}
```

<a name="AgentTokenRejection.Error"></a>
### func \(\*AgentTokenRejection\) [Error](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/agent_token_rejection.go#L55>)

```go
func (r *AgentTokenRejection) Error() string
```

Error names the reason only.

<a name="Authenticator"></a>
## type [Authenticator](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/auth.go#L56>)

Authenticator issues and validates authentication tokens. The MVP ships a JWT implementation; the interface keeps OIDC/LDAP pluggable \(ADR 0008\).

```go
type Authenticator = authn.Authenticator
```

<a name="CIIssuerFingerprints"></a>
## type [CIIssuerFingerprints](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/ci_session.go#L82-L84>)

CIIssuerFingerprints answers the configuration fingerprint of a CI issuer entry by name, false when the entry is not configured. cioidc.Verifier implements it.

```go
type CIIssuerFingerprints interface {
    Fingerprint(name string) (string, bool)
}
```

<a name="CISession"></a>
## type [CISession](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/ci_session.go#L28-L41>)

CISession is what a CI OIDC session acts as: the role a rule of a trusted CI issuer named, in that issuer's tenant \(ADR 0092\).

```go
type CISession struct {
    // Issuer is the name of the CI issuer entry that verified the CI token.
    Issuer string
    // Fingerprint is that entry's configuration fingerprint when the session
    // was minted; a changed entry refuses the session.
    Fingerprint string
    // Tenant is the entry's tenant.
    Tenant string
    // Subject is the CI token's sub, which names the job.
    Subject string
    // Role and Scopes are the matching rule's.
    Role   string
    Scopes []string
}
```

<a name="CISessionAuthenticator"></a>
## type [CISessionAuthenticator](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/ci_session.go#L97-L103>)

CISessionAuthenticator authenticates CI sessions and hands every other token to the next Authenticator \(ADR 0092\). A session resolves to the role it names in its tenant, read from the store on every request, narrowed to its scopes.

```go
type CISessionAuthenticator struct {
    // contains filtered or unexported fields
}
```

<a name="NewCISessionAuthenticator"></a>
### func [NewCISessionAuthenticator](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/ci_session.go#L107>)

```go
func NewCISessionAuthenticator(next Authenticator, secret string, issuers CIIssuerFingerprints, roles RolePermissionStore) *CISessionAuthenticator
```

NewCISessionAuthenticator wraps next so that CI sessions signed with secret are checked against the configured issuers and the tenant's roles.

<a name="CISessionAuthenticator.Authenticate"></a>
### func \(\*CISessionAuthenticator\) [Authenticate](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/ci_session.go#L121>)

```go
func (a *CISessionAuthenticator) Authenticate(ctx context.Context, token string) (*User, error)
```

Authenticate resolves a CI session, or delegates any other token. A session that does not verify, is expired, names an entry that is gone or changed, names no tenant, or names a role its tenant does not have is ErrInvalidToken. A store failure is returned as it is \(503\).

<a name="CISessionAuthenticator.IssueToken"></a>
### func \(\*CISessionAuthenticator\) [IssueToken](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/ci_session.go#L113>)

```go
func (a *CISessionAuthenticator) IssueToken(ctx context.Context, creds Credentials) (string, error)
```

IssueToken delegates to the next authenticator: a CI session comes only from the exchange.

<a name="Credentials"></a>
## type [Credentials](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/auth.go#L52>)

Credentials are the inputs to token issuance.

```go
type Credentials = authn.Credentials
```

<a name="DeviceAuthorization"></a>
## type [DeviceAuthorization](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/device.go#L11-L22>)

DeviceAuthorization is a device sign\-in of the CLI waiting for a person to approve it \(\`dexaflow auth login \-\-device\`, ADR 0081\), as storage returns it. Neither of its codes is here: storage keeps only their keyed hashes.

```go
type DeviceAuthorization struct {
    // ID names the pending sign-in between the verification page, the IdP
    // round trip and the consent page.
    ID  string
    // CreatedAt is when the CLI started the sign-in.
    CreatedAt time.Time
    // ExpiresAt is when the sign-in can no longer be approved or redeemed.
    ExpiresAt time.Time
    // RequesterAddr is the address that started the sign-in, as the server
    // saw it, shown on the consent page.
    RequesterAddr string
}
```

<a name="DeviceAuthorizationRequest"></a>
## type [DeviceAuthorizationRequest](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/device.go#L27-L40>)

DeviceAuthorizationRequest is what starting a device sign\-in stores: the keyed hashes of its codes and of the address that started it, that address for the consent page, and the limits it starts under.

```go
type DeviceAuthorizationRequest struct {
    DeviceCodeHash string
    UserCodeHash   string
    // RequesterHash is the keyed hash of the starting address (its /56 for
    // IPv6), which MaxLivePerRequester counts by.
    RequesterHash string
    RequesterAddr string
    TTL           time.Duration
    Interval      time.Duration
    // MaxLive caps the sign-ins in progress in all, and
    // MaxLivePerRequester those from one requester.
    MaxLive             int
    MaxLivePerRequester int
}
```

<a name="DevicePoll"></a>
## type [DevicePoll](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/device.go#L82-L85>)

DevicePoll is the result of one poll: its outcome and, for an approved sign\-in, the user it was approved as, with the roles the sign\-in resolved.

```go
type DevicePoll struct {
    Outcome DevicePollOutcome
    User    User
}
```

<a name="DevicePollOutcome"></a>
## type [DevicePollOutcome](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/device.go#L56>)

DevicePollOutcome is what one poll of a device sign\-in found.

```go
type DevicePollOutcome int
```

<a name="DevicePollUnknown"></a>The outcomes of a poll. Each maps to one answer of the token route \(RFC 8628 section 3.5\).

```go
const (
    // DevicePollUnknown is a device code nobody issued, or one already
    // redeemed or refused: invalid_grant.
    DevicePollUnknown DevicePollOutcome = iota
    // DevicePollPending is a sign-in nobody approved yet:
    // authorization_pending.
    DevicePollPending
    // DevicePollSlowDown is a poll that came sooner than the interval: the
    // interval grew by five seconds, slow_down.
    DevicePollSlowDown
    // DevicePollExpired is a sign-in past its expiry: expired_token.
    DevicePollExpired
    // DevicePollDenied is a sign-in the person canceled: access_denied. It
    // is answered once; later polls find nothing.
    DevicePollDenied
    // DevicePollApproved is an approved sign-in, redeemed by this poll: the
    // token is minted for User. Later polls find nothing.
    DevicePollApproved
)
```

<a name="JWTAuthenticator"></a>
## type [JWTAuthenticator](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/jwt.go#L39-L47>)

JWTAuthenticator issues and validates HS256 JWTs against a UserStore.

```go
type JWTAuthenticator struct {
    // contains filtered or unexported fields
}
```

<a name="NewJWTAuthenticator"></a>
### func [NewJWTAuthenticator](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/jwt.go#L51>)

```go
func NewJWTAuthenticator(store UserStore, secret string, ttl time.Duration) *JWTAuthenticator
```

NewJWTAuthenticator builds a JWTAuthenticator with the given user store, HS256 secret, and token lifetime.

<a name="JWTAuthenticator.Authenticate"></a>
### func \(\*JWTAuthenticator\) [Authenticate](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/jwt.go#L103>)

```go
func (a *JWTAuthenticator) Authenticate(ctx context.Context, token string) (*User, error)
```

Authenticate validates a bearer token and resolves the current principal. After the signature and registered claims check out, it reloads the user's roles, permissions, and active flag from the store keyed by the token subject \(the user id\). The store, not the token, is the source of truth for authorization: this is what makes non\-admin role grants gate anything \(the token carries roles but never permissions\) and lets deactivating a user revoke their live tokens within the token TTL.

Two cases fall back to the signed claims instead of the reload: a nil store \(no data plane bound, the trusted in\-process minting context\) and a subject with no backing row \(a directly\-minted token, e.g. \`dexaflow lite\`\). Any other store failure fails closed, so a flaky database cannot silently disable revocation.

<a name="JWTAuthenticator.AuthenticateAgent"></a>
### func \(\*JWTAuthenticator\) [AuthenticateAgent](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/agent_token.go#L180>)

```go
func (a *JWTAuthenticator) AuthenticateAgent(token string) (*AgentIdentity, error)
```

AuthenticateAgent validates an agent bearer token and returns the task instance it identifies. A rejection's error matches ErrInvalidToken and carries an \*AgentTokenRejection saying why \(\#1125\).

<a name="JWTAuthenticator.IssueAgentToken"></a>
### func \(\*JWTAuthenticator\) [IssueAgentToken](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/agent_token.go#L92>)

```go
func (a *JWTAuthenticator) IssueAgentToken(id AgentIdentity, ttl time.Duration) (string, error)
```

IssueAgentToken mints a signed token that identifies a single task instance, valid for the given TTL. The control plane passes it to the worker pod.

The token's origin \(oiat\) is set to the mint time — this is a fresh dispatch. Renewal \(RenewAgentToken\) preserves that origin instead of resetting it.

<a name="JWTAuthenticator.IssueToken"></a>
### func \(\*JWTAuthenticator\) [IssueToken](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/jwt.go#L75>)

```go
func (a *JWTAuthenticator) IssueToken(ctx context.Context, creds Credentials) (string, error)
```

IssueToken validates the credentials against the store and returns a signed JWT.

<a name="JWTAuthenticator.RenewAgentToken"></a>
### func \(\*JWTAuthenticator\) [RenewAgentToken](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/agent_token.go#L151>)

```go
func (a *JWTAuthenticator) RenewAgentToken(token string, ttl, maxLifetime time.Duration) (renewed string, ok bool, err error)
```

RenewAgentToken validates an in\-flight agent bearer token and re\-mints it for the SAME task instance with a fresh short TTL, refreshing the live window without ever changing what AuthenticateAgent verifies \(signature, issuer, leoflow\-agent audience, HS256\). It is the heartbeat\-driven half of the short\-TTL\-plus\-renewal design \(ADR 0055 Fix \#4\): the short TTL bounds a stolen or finished token, while renewal keeps a genuinely live task's credential working.

The attempt's original dispatch time is preserved across every renewal \(the oiat claim\). maxLifetime is a hard ceiling on that total age: once the attempt has been alive longer than maxLifetime since first dispatch, renewal is refused \(ok=false, empty token\) so a runaway attempt's credential lapses instead of being kept alive forever. A non\-positive maxLifetime disables the ceiling. exp is always now\+ttl — never accumulated onto the previous exp.

An invalid incoming token \(bad signature, wrong audience, expired\) returns an error and is never re\-minted.

<a name="JWTAuthenticator.RenewUserToken"></a>
### func \(\*JWTAuthenticator\) [RenewUserToken](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/jwt.go#L231>)

```go
func (a *JWTAuthenticator) RenewUserToken(ctx context.Context, token string, ttl, maxLifetime time.Duration) (renewed string, ok bool, err error)
```

RenewUserToken validates a still\-valid user bearer token and re\-mints it for the SAME principal \(subject, tenant, email, roles\) with a fresh short TTL, without ever changing what Authenticate verifies \(signature, issuer, leoflow\-user audience, HS256\). It is the server half of transparent CLI token renewal \(EKS validation aresta \#5\): the short access\-token TTL still bounds a stolen token, while renewal keeps a genuinely live session working so a long dev session never has to \`dexaflow auth login\` again on the hour. It is modeled directly on RenewAgentToken.

The session's original login time is preserved across every renewal \(the oiat claim, falling back to iat for a token minted before that claim existed\). maxLifetime is a hard ceiling on that total age: once the session has been alive longer than maxLifetime since first login, renewal is refused \(ok=false, empty token, no error\) so the user must re\-authenticate. A non\-positive maxLifetime disables the ceiling. exp is always now\+ttl — never accumulated.

An invalid incoming token \(bad signature, wrong audience, expired\) returns an error and is never re\-minted.

Renewal RE\-PROVES the principal: after the claims check and the ceiling check it reloads the user from the store, exactly as Authenticate does, and refuses \(ErrInvalidToken\) when the account is inactive or its row is gone. Issuing a credential is not weaker than using one, so a deactivated user must stop receiving fresh tokens, not merely stop being able to spend them. The same two carve\-outs Authenticate makes apply verbatim: a nil store \(no data plane bound, the trusted in\-process minting context\) and the dev\-token subject, which has no backing row by design. Any other store failure fails closed; that adds no failure mode, because a database blip that refuses this renewal would refuse the following request anyway, and the client's refresh is best\-effort. Roles on the re\-minted token come from the reload, so a role revoked mid\-session is reflected at once rather than carried forward from the old claims.

<a name="Permission"></a>
## type [Permission](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/auth.go#L33>)

Permission is an action on a resource \(e.g. \{Action: "read", Resource: "dag"\}\). It is defined by the public contract \(ADR 0099\).

```go
type Permission = authn.Permission
```

<a name="RateLimiter"></a>
## type [RateLimiter](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/ratelimit.go#L35-L51>)

RateLimiter is a per\-key fixed\-window limiter used to throttle failed logins per client IP \(ADR 0008\).

By itself it counts in process, so N API replicas allow N times the limit. Share gives it a SharedCounter: the limit then holds across every replica, counted in fixed windows aligned to the window length, and when the counter fails the limiter falls back to its own map, which keeps the per replica bound and locks nobody out.

```go
type RateLimiter struct {
    // contains filtered or unexported fields
}
```

<a name="NewRateLimiter"></a>
### func [NewRateLimiter](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/ratelimit.go#L59>)

```go
func NewRateLimiter(limit int, window time.Duration) *RateLimiter
```

NewRateLimiter builds a limiter allowing limit events per window per key.

<a name="RateLimiter.Allow"></a>
### func \(\*RateLimiter\) [Allow](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/ratelimit.go#L128>)

```go
func (r *RateLimiter) Allow(key string) bool
```

Allow records an event for key and reports whether it is within the limit. The check and the spend are one step under the lock, so concurrent callers of a key never get more than the limit between them: a caller that must bound concurrent work spends with Allow before the work, not after a Blocked peek, and gives the event back with Refund when it should not count.

<a name="RateLimiter.Blocked"></a>
### func \(\*RateLimiter\) [Blocked](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/ratelimit.go#L105>)

```go
func (r *RateLimiter) Blocked(key string) bool
```

Blocked reports whether key has already reached its limit in the current window, WITHOUT recording an attempt \(a peek\). The login handler uses it to reject an over\-limit caller up front while calling Allow only for actual failures — so a successful login never consumes the budget and a user who mistypes a few times is not locked out the moment they finally get it right.

<a name="RateLimiter.Refund"></a>
### func \(\*RateLimiter\) [Refund](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/ratelimit.go#L154>)

```go
func (r *RateLimiter) Refund(key string)
```

Refund gives back one event of key's window, never going below zero. A window that ended needs none: the next Allow starts a fresh one.

<a name="RateLimiter.Share"></a>
### func \(\*RateLimiter\) [Share](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/ratelimit.go#L71>)

```go
func (r *RateLimiter) Share(counter SharedCounter, scope string) *RateLimiter
```

Share makes the limiter count through counter under scope, which keeps the keys of each limiter apart \(login, renew, ...\), and returns it. A nil counter leaves the limiter in process.

<a name="RolePermissionStore"></a>
## type [RolePermissionStore](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/ci_session.go#L88-L91>)

RolePermissionStore reads the grants of one role of one tenant. storage implements it.

```go
type RolePermissionStore interface {
    // RolePermissions returns the role's grants, or ErrRoleNotFound.
    RolePermissions(ctx context.Context, tenant, role string) ([]Permission, error)
}
```

<a name="SharedCounter"></a>
## type [SharedCounter](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/ratelimit.go#L16-L20>)

SharedCounter is a counter every API replica reaches, such as Redis, that a RateLimiter shares its budget through \(\#2357\). Incr adds one event to key and returns the count, expiring key after ttl; Get returns the count; Decr takes one event back without going below zero.

```go
type SharedCounter interface {
    Incr(ctx context.Context, key string, ttl time.Duration) (int64, error)
    Get(ctx context.Context, key string) (int64, error)
    Decr(ctx context.Context, key string) error
}
```

<a name="User"></a>
## type [User](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/auth.go#L38>)

User is an authenticated principal with its tenant, roles, and permissions. It is defined by the public contract \(ADR 0099\), with HasScope and HasPermission.

```go
type User = authn.User
```

<a name="UserStore"></a>
## type [UserStore](<https://github.com/dexadata/dexaflow/blob/main/internal/auth/auth.go#L59-L67>)

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
