// Package cioidc verifies the OIDC tokens CI providers mint for their jobs
// (GitHub Actions, GitLab CI, Google Cloud Build), so a pipeline deploys
// without a stored credential (ADR 0092, #1882).
//
// The operator lists the issuers it trusts. Each entry names the one tenant
// its tokens act in, and ordered rules that map the token's claims to a role
// of that tenant. The tenant is never read from a claim: a provider's claims
// are written by whoever owns the repository. A verified token whose claims
// match no rule is refused.
package cioidc

import (
	"errors"
	"time"
)

// The providers an entry may name. The provider decides which claims a rule
// must pin (identityClaims) and which pipeline `dexaflow ci init` writes.
const (
	// ProviderGitHub is GitHub Actions.
	ProviderGitHub = "github"
	// ProviderGitLab is GitLab CI, on gitlab.com or a self-managed instance.
	ProviderGitLab = "gitlab"
	// ProviderCloudBuild is Google Cloud Build, whose jobs sign in as a
	// service account with Google's ID tokens.
	ProviderCloudBuild = "cloudbuild"
	// ProviderOther is any other OIDC provider; its rules may pin any claim.
	ProviderOther = "other"
)

// DefaultMaxLifetime bounds exp - iat when an entry sets no MaxLifetime.
// CI tokens live minutes (five on GitHub Actions, an hour on Cloud Build), so
// an hour refuses a long-lived token minted elsewhere for the same audience.
const DefaultMaxLifetime = time.Hour

// MaxLifetimeCeiling is the longest MaxLifetime an entry may set.
const MaxLifetimeCeiling = 24 * time.Hour

// clockSkew is how far the provider's clock may be from ours, for exp, nbf
// and iat.
const clockSkew = time.Minute

// Verification failures. Each is fail-closed.
var (
	// ErrUnknownIssuer is a token whose iss and aud name no configured entry.
	ErrUnknownIssuer = errors.New("cioidc: no trusted issuer for this token")
	// ErrInvalidToken is a token the entry's provider did not mint for us, now:
	// a bad signature or algorithm, an issuer or audience that does not match,
	// or a missing, expired, post-dated or too long validity, or no subject.
	ErrInvalidToken = errors.New("cioidc: invalid token")
	// ErrNoMatchingRule is a valid token whose claims match no rule of its
	// entry.
	ErrNoMatchingRule = errors.New("cioidc: no rule matches the token's claims")
	// ErrKeysUnavailable is a token that could not be checked because the
	// provider's discovery document or keys could not be downloaded. It says
	// nothing about the token, so the API answers 503.
	ErrKeysUnavailable = errors.New("cioidc: signing keys unavailable")
)

// Rule maps claims to a role. It matches a token when every claim it names
// is a string claim of the token that matches its pattern (path.Match).
type Rule struct {
	// Claims maps a claim name to a pattern.
	Claims map[string]string `json:"claims" yaml:"claims"`
	// Role is the role of the entry's tenant the principal gets.
	Role string `json:"role" yaml:"role"`
	// Scopes narrow the principal (ADR 0067); empty means dexaflow:deploy.
	Scopes []string `json:"scopes,omitempty" yaml:"scopes"`
}

// Issuer is one trusted CI issuer.
type Issuer struct {
	// Name identifies the entry in logs, metrics, sessions and `ci init`.
	Name string `json:"name" yaml:"name"`
	// Provider is one of the Provider constants.
	Provider string `json:"provider" yaml:"provider"`
	// Issuer is the exact iss of the tokens.
	Issuer string `json:"issuer" yaml:"issuer"`
	// JWKSURL is where the keys are; empty discovers it from Issuer.
	JWKSURL string `json:"jwks_url,omitempty" yaml:"jwks_url"`
	// Audience is the aud the tokens must carry.
	Audience string `json:"audience" yaml:"audience"`
	// Tenant is the only tenant the entry's tokens act in.
	Tenant string `json:"tenant" yaml:"tenant"`
	// MaxLifetime caps exp - iat; zero means DefaultMaxLifetime.
	MaxLifetime time.Duration `json:"-" yaml:"-"`
	// Rules are tried in order; the first that matches decides.
	Rules []Rule `json:"rules" yaml:"rules"`
}

// Identity is what a verified token is allowed to be.
type Identity struct {
	// Issuer is the name of the entry that verified the token.
	Issuer string
	// Tenant is the entry's tenant.
	Tenant string
	// Subject is the token's sub, which names the job (on GitHub, the
	// repository and the environment or ref).
	Subject string
	// Role and Scopes are those of the rule that matched.
	Role   string
	Scopes []string
	// Fingerprint identifies the entry's configuration (Verifier.Fingerprint).
	Fingerprint string
}

// Error is a refusal, naming the entry when the token named one, for the
// metric and the log. It wraps one of the Err values.
type Error struct {
	// Issuer and Tenant are the entry's, empty for ErrUnknownIssuer.
	Issuer, Tenant string
	// Subject is the token's sub when it was read.
	Subject string
	// Err is the cause.
	Err error
}

// Error returns the cause's message.
func (e *Error) Error() string { return e.Err.Error() }

// Unwrap returns the cause.
func (e *Error) Unwrap() error { return e.Err }
