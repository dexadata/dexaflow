// Package auth provides JWT authentication, password hashing, the RBAC
// permission model, and login rate limiting for the control plane (ADR 0008).
package auth

import (
	"context"
	"errors"
)

// ErrInvalidCredentials is returned when a username/password pair is rejected.
var ErrInvalidCredentials = errors.New("invalid credentials")

// ErrInvalidToken is returned when a token is malformed, expired, or unsigned by us.
var ErrInvalidToken = errors.New("invalid token")

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
type Permission struct {
	Action   string `json:"action"`
	Resource string `json:"resource"`
}

// User is an authenticated principal with its tenant, roles, and permissions.
type User struct {
	ID          string
	TenantID    string
	Email       string
	Roles       []string
	Permissions []Permission
}

// HasPermission reports whether the user may perform action on resource. The
// admin role, or an admin action / wildcard resource permission, grants access.
func (u *User) HasPermission(action, resource string) bool {
	for _, r := range u.Roles {
		if r == "admin" {
			return true
		}
	}
	for _, p := range u.Permissions {
		if (p.Action == action || p.Action == "admin") && (p.Resource == resource || p.Resource == "*") {
			return true
		}
	}
	return false
}

// Credentials are the inputs to token issuance.
type Credentials struct {
	Tenant   string
	Username string
	Password string
}

// Authenticator issues and validates authentication tokens. The MVP ships a
// JWT implementation; the interface keeps OIDC/LDAP pluggable (ADR 0008).
type Authenticator interface {
	Authenticate(ctx context.Context, token string) (*User, error)
	IssueToken(ctx context.Context, creds Credentials) (string, error)
}

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
