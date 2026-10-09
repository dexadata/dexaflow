// Package issuer verifies tokens from a trusted external issuer, so a platform
// that already authenticates its users can open a Dexaflow UI session for them
// without holding Dexaflow's own signing secret (#1284).
//
// The issuer signs with its own key and publishes it as a JWKS; Dexaflow only
// ever reads public keys. A verified token names an existing user (by the
// issuer's subject) in an allowed tenant. It never creates users and never
// grants roles: roles stay whatever the user row holds.
package issuer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
)

// Verification failures. Each is fail-closed; callers log the class and show
// the browser nothing more specific than "not signed in".
var (
	// ErrInvalidToken covers a bad signature, issuer, audience, expiry or a
	// missing subject or jti: the token is not one the trusted issuer made for
	// us.
	ErrInvalidToken = errors.New("issuer: invalid token")
	// ErrLifetime is a token without iat, one valid for longer than
	// MaxLifetime, or one issued in the future. A handoff token is meant to be
	// used once, right away; a long or post-dated one is a long replay window.
	ErrLifetime = errors.New("issuer: token lifetime missing or too long")
	// ErrTenantNotAllowed is a tenant claim that is absent, not a string, or
	// not in AllowedTenants.
	ErrTenantNotAllowed = errors.New("issuer: tenant not allowed")
	// ErrReplayed is a token whose jti this server already accepted. A handoff
	// token opens one session; a second post of the same token is a replay.
	ErrReplayed = errors.New("issuer: token already used")
)

// DefaultMaxLifetime bounds exp - iat when Config.MaxLifetime is zero. A
// handoff is posted the moment it is minted, so two minutes is generous.
const DefaultMaxLifetime = 2 * time.Minute

// maxUsedIDs caps the used-jti set. Entries leave it when their token expires;
// at the default lifetime, filling it takes hundreds of sign-ins per second.
// A full set refuses new tokens rather than forgetting used ones.
const maxUsedIDs = 100_000

// clockSkew is how far ahead of this server's clock a token's iat may be. A
// post-dated iat would otherwise let a short exp - iat hide a long validity.
const clockSkew = time.Minute

// Config is one trusted issuer.
type Config struct {
	// Name identifies the issuer inside Dexaflow. Its users are linked by
	// (Provider(), subject), so it must stay stable once users exist.
	Name string
	// Issuer is the exact `iss` the tokens carry.
	Issuer string
	// JWKSURL is where the issuer publishes its public signing keys.
	JWKSURL string
	// Audience is the `aud` the tokens must carry for this Dexaflow.
	Audience string
	// TenantClaim names the string claim carrying the Dexaflow tenant name.
	TenantClaim string
	// AllowedTenants lists the tenants this issuer may sign in to; "*" allows
	// every tenant.
	AllowedTenants []string
	// MaxLifetime caps exp - iat; zero means DefaultMaxLifetime.
	MaxLifetime time.Duration
	// BearerAudiences are the `aud` values of tokens accepted as a bearer on
	// every request (VerifyBearer). Empty turns the bearer mode off. They must
	// differ from Audience, so a handoff token is never a bearer.
	BearerAudiences []string
	// BearerMaxLifetime caps exp - iat of a bearer; zero means
	// DefaultBearerMaxLifetime.
	BearerMaxLifetime time.Duration
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
	// bearer verifies signature, issuer and expiry of bearer tokens; their
	// audience is checked against BearerAudiences by VerifyBearer.
	bearer *gooidc.IDTokenVerifier
	now    func() time.Time
	used   usedIDs
}

// usedIDs remembers the jti of every accepted token until it expires, so each
// token opens at most one session. It lives in this process: with several
// replicas a token could be accepted once per replica, which is why the
// lifetime is kept short.
type usedIDs struct {
	mu  sync.Mutex
	exp map[string]time.Time
}

// claim records id as used until exp and reports whether it was unused. It
// drops expired entries first, and refuses when the set is still full.
func (u *usedIDs) claim(id string, exp, now time.Time) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.exp == nil {
		u.exp = make(map[string]time.Time)
	}
	if until, seen := u.exp[id]; seen && now.Before(until) {
		return false
	}
	if len(u.exp) >= maxUsedIDs {
		for k, until := range u.exp {
			if !now.Before(until) {
				delete(u.exp, k)
			}
		}
		if len(u.exp) >= maxUsedIDs {
			return false
		}
	}
	u.exp[id] = exp
	return true
}

// New builds a Verifier. It makes no network call: the JWKS is fetched on the
// first Verify and cached, refreshed when a token names an unknown key id, so
// key rotation needs no restart and an issuer outage cannot block boot.
func New(ctx context.Context, cfg Config) *Verifier {
	if cfg.MaxLifetime <= 0 {
		cfg.MaxLifetime = DefaultMaxLifetime
	}
	if cfg.BearerMaxLifetime <= 0 {
		cfg.BearerMaxLifetime = DefaultBearerMaxLifetime
	}
	ver := &Verifier{cfg: cfg, now: time.Now}
	keys := gooidc.NewRemoteKeySet(ctx, cfg.JWKSURL)
	algs := []string{gooidc.RS256, gooidc.ES256, gooidc.PS256}
	now := func() time.Time { return ver.now() }
	ver.v = gooidc.NewVerifier(cfg.Issuer, keys, &gooidc.Config{
		ClientID:             cfg.Audience,
		SupportedSigningAlgs: algs,
		Now:                  now,
	})
	ver.bearer = gooidc.NewVerifier(cfg.Issuer, keys, &gooidc.Config{
		SkipClientIDCheck:    true,
		SupportedSigningAlgs: algs,
		Now:                  now,
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
	id, claims, err := v.identity(tok, v.cfg.MaxLifetime)
	if err != nil {
		return nil, err
	}
	jti, ok := claims["jti"].(string)
	if !ok || jti == "" {
		return nil, fmt.Errorf("%w: no jti", ErrInvalidToken)
	}
	// Last, so a token refused for any other reason does not use up its jti.
	if !v.used.claim(jti, tok.Expiry, v.now()) {
		return nil, fmt.Errorf("%w: jti %q", ErrReplayed, jti)
	}
	return id, nil
}

// identity applies the checks a handoff and a bearer share to a token whose
// signature, issuer and expiry already verified: a subject, a bounded and not
// post-dated lifetime, and an allowed tenant. It returns the claims too, for
// the checks that differ.
func (v *Verifier) identity(tok *gooidc.IDToken, maxLifetime time.Duration) (*Identity, map[string]any, error) {
	if tok.Subject == "" {
		return nil, nil, fmt.Errorf("%w: no subject", ErrInvalidToken)
	}
	if tok.IssuedAt.IsZero() || tok.Expiry.Sub(tok.IssuedAt) > maxLifetime || tok.IssuedAt.After(v.now().Add(clockSkew)) {
		return nil, nil, fmt.Errorf("%w: iat %v, exp %v, max %v", ErrLifetime, tok.IssuedAt, tok.Expiry, maxLifetime)
	}
	var claims map[string]any
	if err := tok.Claims(&claims); err != nil {
		return nil, nil, fmt.Errorf("%w: decoding claims: %w", ErrInvalidToken, err)
	}
	tenant, ok := claims[v.cfg.TenantClaim].(string)
	if !ok || tenant == "" || !v.allowed(tenant) {
		return nil, nil, fmt.Errorf("%w: claim %q = %v", ErrTenantNotAllowed, v.cfg.TenantClaim, claims[v.cfg.TenantClaim])
	}
	id := &Identity{Subject: tok.Subject, Tenant: tenant}
	if email, ok := claims["email"].(string); ok {
		id.Email = email
	}
	return id, claims, nil
}

func (v *Verifier) allowed(tenant string) bool {
	for _, t := range v.cfg.AllowedTenants {
		if t == "*" || t == tenant {
			return true
		}
	}
	return false
}
