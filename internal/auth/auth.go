// Package auth provides JWT authentication, password hashing, the RBAC
// permission model, and login rate limiting for the control plane (ADR 0008).
package auth

import (
	"context"
	"errors"

	"github.com/dexadata/dexaflow/pkg/authn"
)

// ErrInvalidCredentials is returned when a username/password pair is rejected.
// It is the public contract's sentinel (ADR 0099).
var ErrInvalidCredentials = authn.ErrInvalidCredentials

// ErrInvalidToken is returned when a token is malformed, expired, or unsigned by us.
// It is the public contract's sentinel (ADR 0099).
var ErrInvalidToken = authn.ErrInvalidToken

// ErrTenantlessToken is joined with ErrInvalidToken when a token is trusted
// from its signed claims but names no tenant: such a principal cannot be scoped
// to any tenant's data, so it is refused rather than given a default one.
var ErrTenantlessToken = errors.New("token names no tenant")

// ErrUserNotFound is returned by UserStore.FindUserByID when no user has the
// given id. Authenticate treats it as a signal to trust the token's signed
// claims (the in-process minting path has no backing row), distinct from a
// store failure, which fails closed.
var ErrUserNotFound = errors.New("user not found")

// Permission is an action on a resource (e.g. {Action: "read", Resource: "dag"}).
// It is defined by the public contract (ADR 0099).
type Permission = authn.Permission

// User is an authenticated principal with its tenant, roles, and permissions.
// It is defined by the public contract (ADR 0099), with HasScope and
// HasPermission.
type User = authn.User

// The scopes a trusted-issuer bearer token may carry (ADR 0067). No scope
// implies another.
const (
	// ScopeRead grants the read routes.
	ScopeRead = "dexaflow:read"
	// ScopeRun grants run-state writes: trigger, clear, mark, pause.
	ScopeRun = "dexaflow:run"
	// ScopeDeploy grants registering a DAG version.
	ScopeDeploy = "dexaflow:deploy"
)

// Credentials are the inputs to token issuance.
type Credentials = authn.Credentials

// Authenticator issues and validates authentication tokens. The MVP ships a
// JWT implementation; the interface keeps OIDC/LDAP pluggable (ADR 0008).
type Authenticator = authn.Authenticator

// UserStore loads users for authentication. storage implements it.
type UserStore interface {
	FindUserByLogin(ctx context.Context, tenant, username string) (user *User, passwordHash string, err error)
	// FindUserByID reloads a user's current authorization state (tenant, roles,
	// permissions) by id, along with whether the account is active. It is the
	// per-request source of truth for token validation: it makes non-admin role
	// grants take effect and lets deactivating a user revoke live tokens within
	// the token TTL. It returns ErrUserNotFound when no user has the id.
	FindUserByID(ctx context.Context, id string) (user *User, isActive bool, err error)
}
