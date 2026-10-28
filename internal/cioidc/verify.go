package cioidc

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/issuer"
)

// signingAlgs are the algorithms a CI token may be signed with: the public
// key ones the providers use. none and the HMAC algorithms are refused.
var signingAlgs = []string{gooidc.RS256, gooidc.ES256, gooidc.PS256}

// entry is one configured issuer with its verifier.
type entry struct {
	cfg         Issuer
	fingerprint string
	verifier    *gooidc.IDTokenVerifier
}

// Verifier checks CI tokens against the configured issuers.
type Verifier struct {
	entries []*entry
	now     func() time.Time
}

// New builds a Verifier for issuers, which Validate accepted. It makes no
// network call: discovery documents and keys are fetched on first use and
// cached, so a provider outage cannot block boot.
func New(issuers []Issuer) *Verifier {
	v := &Verifier{now: time.Now}
	now := func() time.Time { return v.now() }
	for _, is := range issuers {
		if is.MaxLifetime <= 0 {
			is.MaxLifetime = DefaultMaxLifetime
		}
		e := &entry{cfg: is, fingerprint: fingerprint(is)}
		e.verifier = gooidc.NewVerifier(is.Issuer, newLazyKeys(is.Issuer, is.JWKSURL, signingAlgs, now), &gooidc.Config{
			ClientID:             is.Audience,
			SupportedSigningAlgs: signingAlgs,
			// exp, nbf and iat are checked by checkTimes, with the same skew.
			SkipExpiryCheck: true,
			Now:             now,
		})
		v.entries = append(v.entries, e)
	}
	return v
}

// fingerprint is the hex SHA-256 of the entry's configuration: a session
// minted under one configuration is refused once the entry changes.
func fingerprint(is Issuer) string {
	b, err := json.Marshal(struct {
		Issuer
		MaxLifetime int64 `json:"max_lifetime"`
	}{is, int64(is.MaxLifetime)})
	if err != nil {
		// Marshal of plain strings, maps and slices cannot fail; an empty
		// fingerprint would match nothing.
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Fingerprint returns the fingerprint of the entry called name, and false
// when no entry has that name.
func (v *Verifier) Fingerprint(name string) (string, bool) {
	for _, e := range v.entries {
		if e.cfg.Name == name {
			return e.fingerprint, true
		}
	}
	return "", false
}

// Advertised returns the entries of tenant as GET /api/v2/deployDefaults
// lists them for `dexaflow ci init`: name, provider, issuer and audience.
func (v *Verifier) Advertised(tenant string) []domain.CIOIDCIssuer {
	var out []domain.CIOIDCIssuer
	for _, e := range v.entries {
		if e.cfg.Tenant == tenant {
			out = append(out, domain.CIOIDCIssuer{Name: e.cfg.Name, Provider: e.cfg.Provider, Issuer: e.cfg.Issuer, Audience: e.cfg.Audience})
		}
	}
	return out
}

// Verify checks raw against the entry its iss and aud name and returns what
// the first matching rule allows. A refusal is an *Error wrapping
// ErrUnknownIssuer, ErrInvalidToken, ErrNoMatchingRule or ErrKeysUnavailable.
func (v *Verifier) Verify(ctx context.Context, raw string) (*Identity, error) {
	iss, aud, err := precheck(raw)
	if err != nil {
		return nil, &Error{Err: err}
	}
	e := v.entryFor(iss, aud)
	if e == nil {
		return nil, &Error{Err: fmt.Errorf("%w: issuer %q with audience %v", ErrUnknownIssuer, iss, aud)}
	}
	fail := func(err error, sub string) error {
		return &Error{Issuer: e.cfg.Name, Tenant: e.cfg.Tenant, Subject: sub, Err: err}
	}
	ctx, outage := issuer.TrackKeyOutage(ctx)
	tok, err := e.verifier.Verify(ctx, raw)
	if err != nil {
		if outage() {
			return nil, fail(fmt.Errorf("%w: %w", ErrKeysUnavailable, err), "")
		}
		return nil, fail(fmt.Errorf("%w: %w", ErrInvalidToken, err), "")
	}
	var claims map[string]any
	if err := tok.Claims(&claims); err != nil {
		return nil, fail(fmt.Errorf("%w: decoding claims: %w", ErrInvalidToken, err), "")
	}
	if err := v.checkTimes(tok, claims, e.cfg.MaxLifetime); err != nil {
		return nil, fail(err, tok.Subject)
	}
	rule, ok := matchRule(e.cfg.Rules, claims)
	if !ok {
		return nil, fail(fmt.Errorf("%w of issuer %s", ErrNoMatchingRule, e.cfg.Name), tok.Subject)
	}
	scopes := rule.Scopes
	if len(scopes) == 0 {
		scopes = []string{auth.ScopeDeploy}
	}
	return &Identity{
		Issuer: e.cfg.Name, Tenant: e.cfg.Tenant, Subject: tok.Subject,
		Role: rule.Role, Scopes: slices.Clone(scopes), Fingerprint: e.fingerprint,
	}, nil
}

// entryFor returns the entry whose issuer is iss and whose audience is in
// aud, nil when none is. Validate keeps (issuer, audience) unique.
func (v *Verifier) entryFor(iss string, aud []string) *entry {
	for _, e := range v.entries {
		if e.cfg.Issuer == iss && slices.Contains(aud, e.cfg.Audience) {
			return e
		}
	}
	return nil
}

// checkTimes applies the validity checks go-oidc is told to skip, with one
// skew: a subject, an exp not past, an nbf (when present) reached, an iat
// present and not in the future, and exp - iat within maxLifetime.
func (v *Verifier) checkTimes(tok *gooidc.IDToken, claims map[string]any, maxLifetime time.Duration) error {
	now := v.now()
	switch {
	case tok.Subject == "":
		return fmt.Errorf("%w: no subject", ErrInvalidToken)
	case tok.Expiry.IsZero():
		return fmt.Errorf("%w: no exp", ErrInvalidToken)
	case !now.Before(tok.Expiry.Add(clockSkew)):
		return fmt.Errorf("%w: expired at %s", ErrInvalidToken, tok.Expiry.UTC().Format(time.RFC3339))
	case tok.IssuedAt.IsZero():
		return fmt.Errorf("%w: no iat", ErrInvalidToken)
	case tok.IssuedAt.After(now.Add(clockSkew)):
		return fmt.Errorf("%w: issued in the future", ErrInvalidToken)
	case tok.Expiry.Sub(tok.IssuedAt) > maxLifetime:
		return fmt.Errorf("%w: valid for %s, longer than %s", ErrInvalidToken, tok.Expiry.Sub(tok.IssuedAt), maxLifetime)
	}
	if nbf, ok := claims["nbf"].(float64); ok && time.Unix(int64(nbf), 0).After(now.Add(clockSkew)) {
		return fmt.Errorf("%w: not valid before %s", ErrInvalidToken, time.Unix(int64(nbf), 0).UTC().Format(time.RFC3339))
	}
	return nil
}

// matchRule returns the first rule all of whose claims match, and false
// when none does.
func matchRule(rules []Rule, claims map[string]any) (Rule, bool) {
	for _, r := range rules {
		if ruleMatches(r, claims) {
			return r, true
		}
	}
	return Rule{}, false
}

// ruleMatches reports whether every claim r names is a string claim that
// matches its pattern.
func ruleMatches(r Rule, claims map[string]any) bool {
	for name, pattern := range r.Claims {
		got, ok := claims[name].(string)
		if !ok {
			return false
		}
		if matched, err := path.Match(pattern, got); err != nil || !matched {
			return false
		}
	}
	return true
}

// precheck reads the header and payload of raw without verifying them: the
// algorithm must be one of signingAlgs, and the iss and aud are returned to
// pick the entry. Nothing is downloaded for a token that fails here.
func precheck(raw string) (iss string, aud []string, err error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return "", nil, fmt.Errorf("%w: not a compact JWS", ErrInvalidToken)
	}
	var header struct {
		Alg string `json:"alg"`
	}
	if err := decodeSegment(parts[0], &header); err != nil {
		return "", nil, fmt.Errorf("%w: header: %w", ErrInvalidToken, err)
	}
	if !slices.Contains(signingAlgs, header.Alg) {
		return "", nil, fmt.Errorf("%w: algorithm %q is not one of %s", ErrInvalidToken, header.Alg, strings.Join(signingAlgs, ", "))
	}
	var payload struct {
		Iss string          `json:"iss"`
		Aud json.RawMessage `json:"aud"`
	}
	if err := decodeSegment(parts[1], &payload); err != nil {
		return "", nil, fmt.Errorf("%w: payload: %w", ErrInvalidToken, err)
	}
	return payload.Iss, audiences(payload.Aud), nil
}

// decodeSegment decodes one base64url JSON segment of a JWS into out.
func decodeSegment(seg string, out any) error {
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return fmt.Errorf("not base64url: %w", err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("not a JSON object: %w", err)
	}
	return nil
}

// audiences reads an aud claim, a string or an array of strings; anything
// else is no audience.
func audiences(raw json.RawMessage) []string {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return []string{one}
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		return many
	}
	return nil
}
