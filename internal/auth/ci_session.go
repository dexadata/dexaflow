package auth

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// CISessionPrefix starts every CI OIDC session token (ADR 0092). It tells
// one apart from a user JWT and an API token without parsing it.
const CISessionPrefix = "dxci_"

// audienceCI is the audience of a CI session's JWT, so it is never accepted
// as a user token (audienceUser) and a user token is never accepted as one.
const audienceCI = "dexaflow-ci"

// ErrRoleNotFound is returned by RolePermissionStore.RolePermissions when the
// tenant has no role of that name, or there is no such tenant.
var ErrRoleNotFound = errors.New("role not found")

// CISession is what a CI OIDC session acts as: the role a rule of a trusted
// CI issuer named, in that issuer's tenant (ADR 0092).
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

// ciClaims is the JWT payload of a CI session.
type ciClaims struct {
	TenantID    string   `json:"tenant_id"`
	CIIssuer    string   `json:"ci_issuer"`
	Fingerprint string   `json:"ci_fp"`
	Role        string   `json:"role"`
	Scopes      []string `json:"scopes"`
	jwt.RegisteredClaims
}

// IsCISession reports whether token has the CI session form.
func IsCISession(token string) bool {
	return strings.HasPrefix(token, CISessionPrefix)
}

// MintCISession signs a CI session valid for ttl from now with the engine
// secret, and returns it with its expiry (whole seconds, as in the token).
func MintCISession(secret string, ttl time.Duration, now time.Time, s CISession) (string, time.Time, error) {
	exp := now.Add(ttl).Truncate(time.Second)
	c := ciClaims{
		TenantID: s.Tenant, CIIssuer: s.Issuer, Fingerprint: s.Fingerprint, Role: s.Role, Scopes: s.Scopes,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   s.Subject,
			Issuer:    tokenIssuer,
			Audience:  jwt.ClaimStrings{audienceCI},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
		},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString([]byte(secret))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("signing ci session: %w", err)
	}
	return CISessionPrefix + signed, exp, nil
}

// CIIssuerFingerprints answers the configuration fingerprint of a CI issuer
// entry by name, false when the entry is not configured. cioidc.Verifier
// implements it.
type CIIssuerFingerprints interface {
	Fingerprint(name string) (string, bool)
}

// RolePermissionStore reads the grants of one role of one tenant. storage
// implements it.
type RolePermissionStore interface {
	// RolePermissions returns the role's grants, or ErrRoleNotFound.
	RolePermissions(ctx context.Context, tenant, role string) ([]Permission, error)
}

// CISessionAuthenticator authenticates CI sessions and hands every other
// token to the next Authenticator (ADR 0092). A session resolves to the
// role it names in its tenant, read from the store on every request,
// narrowed to its scopes.
type CISessionAuthenticator struct {
	next    Authenticator
	secret  []byte
	issuers CIIssuerFingerprints
	roles   RolePermissionStore
	now     func() time.Time
}

// NewCISessionAuthenticator wraps next so that CI sessions signed with
// secret are checked against the configured issuers and the tenant's roles.
func NewCISessionAuthenticator(next Authenticator, secret string, issuers CIIssuerFingerprints, roles RolePermissionStore) *CISessionAuthenticator {
	return &CISessionAuthenticator{next: next, secret: []byte(secret), issuers: issuers, roles: roles, now: time.Now}
}

// IssueToken delegates to the next authenticator: a CI session comes only
// from the exchange.
func (a *CISessionAuthenticator) IssueToken(ctx context.Context, creds Credentials) (string, error) {
	return a.next.IssueToken(ctx, creds)
}

// Authenticate resolves a CI session, or delegates any other token. A session
// that does not verify, is expired, names an entry that is gone or changed,
// names no tenant, or names a role its tenant does not have is
// ErrInvalidToken. A store failure is returned as it is (503).
func (a *CISessionAuthenticator) Authenticate(ctx context.Context, token string) (*User, error) {
	if !IsCISession(token) {
		return a.next.Authenticate(ctx, token)
	}
	var c ciClaims
	parsed, err := jwt.ParseWithClaims(strings.TrimPrefix(token, CISessionPrefix), &c, func(*jwt.Token) (any, error) {
		return a.secret, nil
	}, jwt.WithIssuer(tokenIssuer), jwt.WithAudience(audienceCI), jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithExpirationRequired(), jwt.WithTimeFunc(a.now))
	if err != nil || !parsed.Valid {
		return nil, errors.Join(ErrInvalidToken, err)
	}
	if fp, ok := a.issuers.Fingerprint(c.CIIssuer); !ok || fp != c.Fingerprint {
		return nil, fmt.Errorf("%w: ci issuer %q is no longer configured as it was", ErrInvalidToken, c.CIIssuer)
	}
	if c.TenantID == "" {
		return nil, errors.Join(ErrInvalidToken, ErrTenantlessToken)
	}
	perms, err := a.roles.RolePermissions(ctx, c.TenantID, c.Role)
	if errors.Is(err, ErrRoleNotFound) {
		return nil, fmt.Errorf("%w: role %q is not a role of tenant %q", ErrInvalidToken, c.Role, c.TenantID)
	}
	if err != nil {
		return nil, fmt.Errorf("loading the role of a ci session: %w", err)
	}
	return &User{
		ID:          "ci:" + c.CIIssuer,
		TenantID:    c.TenantID,
		Email:       "ci:" + c.CIIssuer + ":" + c.Subject,
		Roles:       []string{c.Role},
		Permissions: perms,
		Scoped:      true,
		Scopes:      slices.Clone(c.Scopes),
	}, nil
}
