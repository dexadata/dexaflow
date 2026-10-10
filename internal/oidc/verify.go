package oidc

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"

	"github.com/dexadata/dexaflow/internal/config"
)

// Verification failures. Each is a fail-closed rejection the caller maps to a
// 403 and audits by reason; none of them ever falls back to a default tenant or
// a trusted default identity.
var (
	// ErrIssuerMismatch is returned when the ID token's iss differs from the
	// pinned issuer (H1a).
	ErrIssuerMismatch = errors.New("oidc: issuer mismatch")
	// ErrNonceMismatch is returned when the ID token's nonce does not match the
	// value bound in the state cookie (H4 — replay/CSRF on the token).
	ErrNonceMismatch = errors.New("oidc: nonce mismatch")
	// ErrAzpMismatch is returned when the authorized-party (azp) claim is present
	// and is not this client (H4).
	ErrAzpMismatch = errors.New("oidc: azp mismatch")
	// ErrTokenExpired is returned when exp/iat/nbf fall outside the allowed clock
	// skew (H4).
	ErrTokenExpired = errors.New("oidc: token outside validity window")
	// ErrMissingExpiry is returned when the ID token carries no exp claim; with
	// go-oidc's own expiry check disabled, an absent exp must fail closed rather
	// than grant an unbounded session.
	ErrMissingExpiry = errors.New("oidc: token has no expiry")
	// ErrEmailNotVerified is returned when email_verified is not true; an absent
	// claim is treated as false (D6c).
	ErrEmailNotVerified = errors.New("oidc: email not verified")
	// ErrTenantNotAllowed is returned when the tenant claim (tid/hd) is absent or
	// not present in the tenant_claims map (D6b/d) — never a fallback to default.
	ErrTenantNotAllowed = errors.New("oidc: tenant claim not allowed")
	// ErrTenantAmbiguous is a tenant claim whose array names MORE THAN ONE
	// accepted tenant. Picking one would be arbitrary and the choice would decide
	// which tenant's data the session reaches, so this fails closed instead.
	ErrTenantAmbiguous = errors.New("oidc: tenant claim names more than one accepted tenant")
	// ErrTenantClaimShape is a tenant claim that is neither a string nor an array
	// of strings. It is separate from ErrTenantNotAllowed on purpose: "not
	// allowed" sends an operator to inspect their tenant_claims map, which in this
	// case is correct, and nothing would tell them the value had a shape the pin
	// cannot read.
	ErrTenantClaimShape = errors.New("oidc: tenant claim is neither a string nor an array of strings")
	// ErrEmailDomainNotAllowed is returned when a non-empty allowed_email_domains
	// list does not include the verified email's domain (login-level allowlist).
	ErrEmailDomainNotAllowed = errors.New("oidc: email domain not allowed")
	// ErrNoSubject is returned when the ID token carries no subject — there is no
	// stable identity to key on.
	ErrNoSubject = errors.New("oidc: token has no subject")
	// ErrGroupOverage signals the IdP omitted the groups claim and returned a
	// _claim_names overage pointer instead (Azure Entra does this past ~200 group
	// memberships). Silently treating that as "no groups" would demote a heavily-
	// grouped user (often the most privileged) to default-deny / default_role with
	// no signal, so it fails closed with an actionable message: configure Entra
	// app roles (the "roles" claim) or narrow group membership. Full app-role
	// resolution is the EKS-validated follow-up.
	ErrGroupOverage = errors.New("oidc: group claim overage (IdP returned a _claim_names pointer, not the groups) — configure Entra app roles or the groups scope")
)

// VerifiedIdentity is the result of a fully-checked ID token: a trustworthy
// identity the caller may resolve to a Dexaflow user and mint a session for. It
// is only ever returned after every H1/H4 check and the tenant pin have passed.
type VerifiedIdentity struct {
	// Provider is the stable provider key stored on the user row (the pinned
	// issuer URL), half of the (provider, subject) link key.
	Provider string
	// Subject is the immutable IdP subject, the other half of the link key.
	Subject string
	// Email is the user's email; it is only trusted because EmailVerified is true.
	Email string
	// EmailVerified is always true on a returned identity (a false/absent claim
	// fails verification).
	EmailVerified bool
	// Tenant is the resolved Dexaflow tenant name (from the tid/hd pin).
	Tenant string
	// Groups are the raw IdP group values, for group→role mapping.
	Groups []string
}

// Verifier verifies ID tokens keylessly against the issuer's public JWKS and
// enforces the fail-closed identity gate (issuer pin, audience, nonce, azp,
// clock skew, email_verified, tenant pin, email-domain allowlist). It wraps
// go-oidc but does NOT rely on it for nonce, azp, or clock skew, which go-oidc
// does not check — those are enforced here explicitly (H4).
type Verifier struct {
	cfg      config.OIDCSection
	verifier *gooidc.IDTokenVerifier
	now      func() time.Time
}

// NewVerifier discovers the issuer's OIDC metadata (which pins the issuer: the
// discovery document's issuer must equal cfg.Issuer) and builds an ID-token
// verifier that checks the signature against the published JWKS and the
// audience against the client id. Expiry is deferred to this package's
// skew-aware check, so the verifier is configured to skip go-oidc's own
// expiry check.
func NewVerifier(ctx context.Context, cfg config.OIDCSection) (*Verifier, error) {
	provider, err := gooidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery for %q: %w", cfg.Issuer, err)
	}
	return newVerifierWithProvider(provider, cfg), nil
}

// newVerifierWithProvider builds the verifier from an already-discovered
// provider; split out so a test can supply a provider pointed at a local IdP.
func newVerifierWithProvider(provider *gooidc.Provider, cfg config.OIDCSection) *Verifier {
	return &Verifier{
		cfg: cfg,
		verifier: provider.Verifier(&gooidc.Config{
			ClientID: cfg.ClientID,
			// This package enforces exp/iat/nbf with a configurable skew, so it is
			// the single time authority; go-oidc's own (leeway-less) expiry check is
			// disabled to avoid rejecting a token that is valid within the skew.
			SkipExpiryCheck: true,
		}),
		now: time.Now,
	}
}

// idClaims are the security-relevant claims pulled from the ID token in addition
// to the fields go-oidc exposes on its *IDToken. Groups and the tenant claim are
// read separately because their claim names are configurable.
type idClaims struct {
	Azp           string `json:"azp"`
	NotBefore     int64  `json:"nbf"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
}

// Verify performs the full fail-closed check on a raw ID token and returns the
// resulting identity. expectedNonce is the value bound in the state cookie. The
// order matters: signature/audience/issuer (go-oidc) → nonce → azp → clock skew
// → subject → email_verified → tenant pin → email-domain allowlist. The domain
// allowlist is deliberately last (and only reached once email_verified is true)
// so a spoofable domain can never substitute for the tid/hd pin.
func (v *Verifier) Verify(ctx context.Context, rawIDToken, expectedNonce string) (*VerifiedIdentity, error) {
	idToken, err := v.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		// Covers a tampered/invalid signature, a wrong audience, and an issuer that
		// does not match the pinned discovery issuer (go-oidc phrases the last as
		// "issued by a different provider").
		if strings.Contains(err.Error(), "different provider") || strings.Contains(err.Error(), "issuer") {
			return nil, ErrIssuerMismatch
		}
		return nil, fmt.Errorf("oidc: token verification failed: %w", err)
	}
	// Explicit issuer pin (H1a), independent of go-oidc's own check.
	if idToken.Issuer != v.cfg.Issuer {
		return nil, ErrIssuerMismatch
	}
	var claims idClaims
	if cerr := idToken.Claims(&claims); cerr != nil {
		return nil, fmt.Errorf("oidc: decoding claims: %w", cerr)
	}
	// The explicit H4 checks go-oidc does not perform (nonce, azp, clock skew),
	// plus subject presence and email_verified.
	if berr := v.checkBindings(idToken, claims, expectedNonce); berr != nil {
		return nil, berr
	}
	// Tenant pin (D6b/d): resolve from the configured tid/hd claim; absent or
	// unmapped is a hard reject, never a default fallback.
	tenant, err := v.resolveTenant(idToken)
	if err != nil {
		return nil, err
	}
	// Login-level email-domain allowlist, layered on top of the pin. Only reached
	// after email_verified, so the domain is trustworthy here.
	if derr := v.checkEmailDomain(claims.Email); derr != nil {
		return nil, derr
	}

	groups, err := extractGroups(idToken, v.cfg.GroupsClaim)
	if err != nil {
		return nil, err
	}
	return &VerifiedIdentity{
		Provider:      v.cfg.Issuer,
		Subject:       idToken.Subject,
		Email:         claims.Email,
		EmailVerified: true,
		Tenant:        tenant,
		Groups:        groups,
	}, nil
}

// checkBindings performs the fail-closed checks go-oidc does not: the nonce
// binding to the state cookie, azp when present, the clock-skew-aware validity
// window, subject presence, and email_verified.
func (v *Verifier) checkBindings(idToken *gooidc.IDToken, claims idClaims, expectedNonce string) error {
	// Nonce binding (H4): constant-time compare against the state cookie.
	if expectedNonce == "" || subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(expectedNonce)) != 1 {
		return ErrNonceMismatch
	}
	// azp, when present, must be this client (H4).
	if claims.Azp != "" && claims.Azp != v.cfg.ClientID {
		return ErrAzpMismatch
	}
	// Clock-skew-aware exp/iat/nbf (H4).
	if terr := v.checkTimes(idToken, claims.NotBefore); terr != nil {
		return terr
	}
	if idToken.Subject == "" {
		return ErrNoSubject
	}
	// email_verified must be explicitly true (absent → false, D6c).
	if !claims.EmailVerified {
		return ErrEmailNotVerified
	}
	return nil
}

// checkTimes enforces exp/iat/nbf within the configured clock skew.
func (v *Verifier) checkTimes(idToken *gooidc.IDToken, nbfUnix int64) error {
	skew := time.Duration(v.cfg.ClockSkewSeconds) * time.Second
	now := v.now()
	// A token with no exp has no validity window. Because go-oidc runs with
	// SkipExpiryCheck (this package is the time authority), a missing exp would
	// otherwise sail through unbounded — fail closed instead.
	if idToken.Expiry.IsZero() {
		return ErrMissingExpiry
	}
	if now.After(idToken.Expiry.Add(skew)) {
		return ErrTokenExpired
	}
	if !idToken.IssuedAt.IsZero() && idToken.IssuedAt.After(now.Add(skew)) {
		return ErrTokenExpired
	}
	if nbfUnix != 0 {
		nbf := time.Unix(nbfUnix, 0)
		if now.Before(nbf.Add(-skew)) {
			return ErrTokenExpired
		}
	}
	return nil
}

// resolveTenant maps the configured tid/hd claim value to a Dexaflow tenant,
// failing closed when the claim is absent or unmapped.
func (v *Verifier) resolveTenant(idToken *gooidc.IDToken) (string, error) {
	if v.cfg.TenantClaim == "" {
		return "", ErrTenantNotAllowed
	}
	var raw map[string]any
	if err := idToken.Claims(&raw); err != nil {
		return "", fmt.Errorf("oidc: decoding tenant claim: %w", err)
	}
	values, err := tenantClaimValues(raw[v.cfg.TenantClaim])
	if err != nil {
		return "", err
	}
	var tenant string
	for _, val := range values {
		mapped, ok := v.cfg.TenantClaims[val]
		if !ok || mapped == "" {
			continue // an unmapped value is not a claim about any tenant of ours
		}
		if tenant != "" && mapped != tenant {
			return "", ErrTenantAmbiguous
		}
		tenant = mapped
	}
	if tenant == "" {
		return "", ErrTenantNotAllowed
	}
	return tenant, nil
}

// tenantClaimValues reads a tenant claim as the set of strings it carries.
//
// A claim is a string for hd (Google) and tid (Entra), and those were the only
// shapes this handled. OpenID Connect defines aud as a string OR an array of
// strings, and aud is what an operator reaches for behind an IdP that issues no
// domain claim at all, so the array form is reachable in a real deployment and
// used to reject every login.
//
// An array is decoded from JSON as []any, so each element is checked rather than
// assumed: a mixed array is a shape error, not a partial read.
func tenantClaimValues(claim any) ([]string, error) {
	switch v := claim.(type) {
	case nil:
		return nil, ErrTenantNotAllowed // the claim is absent, which is the allowlist's answer
	case string:
		if v == "" {
			return nil, ErrTenantNotAllowed
		}
		return []string{v}, nil
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			s, ok := e.(string)
			if !ok {
				return nil, ErrTenantClaimShape
			}
			if s != "" {
				out = append(out, s)
			}
		}
		if len(out) == 0 {
			return nil, ErrTenantNotAllowed
		}
		return out, nil
	default:
		return nil, ErrTenantClaimShape
	}
}

// checkEmailDomain enforces the optional login-level domain allowlist. An empty
// list imposes no restriction (the tid/hd pin is the sole boundary).
func (v *Verifier) checkEmailDomain(email string) error {
	if len(v.cfg.AllowedEmailDomains) == 0 {
		return nil
	}
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return ErrEmailDomainNotAllowed
	}
	domain := strings.ToLower(email[at+1:])
	for _, allowed := range v.cfg.AllowedEmailDomains {
		if strings.ToLower(strings.TrimSpace(allowed)) == domain {
			return nil
		}
	}
	return ErrEmailDomainNotAllowed
}

// extractGroups reads the configured groups claim as a list of strings,
// tolerating both a JSON array and a single string value.
func extractGroups(idToken *gooidc.IDToken, claimName string) ([]string, error) {
	if claimName == "" {
		return nil, nil
	}
	var raw map[string]any
	if err := idToken.Claims(&raw); err != nil {
		return nil, fmt.Errorf("oidc: decoding groups claim: %w", err)
	}
	switch v := raw[claimName].(type) {
	case nil:
		// The claim is absent. Distinguish a genuine no-groups token from an Entra
		// group-overage, where the IdP omits `groups` and points to it under
		// `_claim_names` — treating the latter as "no groups" would silently
		// deny/demote a heavily-grouped (often privileged) user. Fail closed.
		if names, ok := raw["_claim_names"].(map[string]any); ok {
			if _, overage := names[claimName]; overage {
				return nil, ErrGroupOverage
			}
		}
		return nil, nil
	case string:
		if v == "" {
			return nil, nil
		}
		return []string{v}, nil
	case []any:
		groups := make([]string, 0, len(v))
		for _, g := range v {
			if s, ok := g.(string); ok && s != "" {
				groups = append(groups, s)
			}
		}
		return groups, nil
	default:
		return nil, nil
	}
}
