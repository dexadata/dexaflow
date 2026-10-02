// Package issuer verifies tokens from a trusted external issuer, so a platform
// that already authenticates its users can open a Leoflow UI session for them
// without holding Leoflow's own signing secret (#1284).
//
// The issuer signs with its own key and publishes it as a JWKS; Leoflow only
// ever reads public keys. A verified token names an existing user (by the
// issuer's subject) in an allowed tenant. It never creates users and never
// grants roles: roles stay whatever the user row holds.
package issuer

import (
	"context"
	"errors"
	"fmt"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
)

// Verification failures. Each is fail-closed; callers log the class and show
// the browser nothing more specific than "not signed in".
var (
	// ErrInvalidToken covers a bad signature, issuer, audience, expiry or a
	// missing subject: the token is not one the trusted issuer made for us.
	ErrInvalidToken = errors.New("issuer: invalid token")
	// ErrLifetime is a token without iat or one valid for longer than
	// MaxLifetime. A handoff token is meant to be used once, right away; a long
	// one is a long replay window.
	ErrLifetime = errors.New("issuer: token lifetime missing or too long")
	// ErrTenantNotAllowed is a tenant claim that is absent, not a string, or
	// not in AllowedTenants.
	ErrTenantNotAllowed = errors.New("issuer: tenant not allowed")
)

// DefaultMaxLifetime bounds exp - iat when Config.MaxLifetime is zero.
const DefaultMaxLifetime = 15 * time.Minute

// Config is one trusted issuer.
type Config struct {
	// Name identifies the issuer inside Leoflow. Its users are linked by
	// (Provider(), subject), so it must stay stable once users exist.
	Name string
	// Issuer is the exact `iss` the tokens carry.
	Issuer string
	// JWKSURL is where the issuer publishes its public signing keys.
	JWKSURL string
	// Audience is the `aud` the tokens must carry for this Leoflow.
	Audience string
	// TenantClaim names the string claim carrying the Leoflow tenant name.
	TenantClaim string
	// AllowedTenants lists the tenants this issuer may sign in to; "*" allows
	// every tenant.
	AllowedTenants []string
	// MaxLifetime caps exp - iat; zero means DefaultMaxLifetime.
	MaxLifetime time.Duration
}

// Identity is what a verified token says about the user.
type Identity struct {
	Subject string
	Email   string
	Tenant  string
}

// Verifier checks tokens against one trusted issuer.
type Verifier struct {
	cfg Config
	v   *gooidc.IDTokenVerifier
	now func() time.Time
}

// New builds a Verifier. It makes no network call: the JWKS is fetched on the
// first Verify and cached, refreshed when a token names an unknown key id, so
// key rotation needs no restart and an issuer outage cannot block boot.
func New(ctx context.Context, cfg Config) *Verifier {
	if cfg.MaxLifetime <= 0 {
		cfg.MaxLifetime = DefaultMaxLifetime
	}
	ver := &Verifier{cfg: cfg, now: time.Now}
	keys := gooidc.NewRemoteKeySet(ctx, cfg.JWKSURL)
	ver.v = gooidc.NewVerifier(cfg.Issuer, keys, &gooidc.Config{
		ClientID:             cfg.Audience,
		SupportedSigningAlgs: []string{gooidc.RS256, gooidc.ES256, gooidc.PS256},
		Now:                  func() time.Time { return ver.now() },
	})
	return ver
}

// Provider is the key this issuer's users are linked under (the users table's
// oidc_provider). The "issuer:" prefix keeps it apart from OIDC provider names.
func (v *Verifier) Provider() string { return "issuer:" + v.cfg.Name }

// Verify checks raw and returns the identity it carries.
func (v *Verifier) Verify(ctx context.Context, raw string) (*Identity, error) {
	tok, err := v.v.Verify(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	if tok.Subject == "" {
		return nil, fmt.Errorf("%w: no subject", ErrInvalidToken)
	}
	if tok.IssuedAt.IsZero() || tok.Expiry.Sub(tok.IssuedAt) > v.cfg.MaxLifetime {
		return nil, fmt.Errorf("%w: iat %v, exp %v, max %v", ErrLifetime, tok.IssuedAt, tok.Expiry, v.cfg.MaxLifetime)
	}
	var claims map[string]any
	if err := tok.Claims(&claims); err != nil {
		return nil, fmt.Errorf("%w: decoding claims: %w", ErrInvalidToken, err)
	}
	tenant, ok := claims[v.cfg.TenantClaim].(string)
	if !ok || tenant == "" || !v.allowed(tenant) {
		return nil, fmt.Errorf("%w: claim %q = %v", ErrTenantNotAllowed, v.cfg.TenantClaim, claims[v.cfg.TenantClaim])
	}
	id := &Identity{Subject: tok.Subject, Tenant: tenant}
	if email, ok := claims["email"].(string); ok {
		id.Email = email
	}
	return id, nil
}

func (v *Verifier) allowed(tenant string) bool {
	for _, t := range v.cfg.AllowedTenants {
		if t == "*" || t == tenant {
			return true
		}
	}
	return false
}
