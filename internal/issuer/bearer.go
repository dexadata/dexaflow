package issuer

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
)

// ErrBearerDisabled is a bearer presented while Config.BearerAudiences is
// empty: the operator has not turned the bearer mode on.
var ErrBearerDisabled = errors.New("issuer: bearer tokens are not enabled")

// DefaultBearerMaxLifetime bounds exp - iat of a bearer when
// Config.BearerMaxLifetime is zero. A bearer is reused until it expires and
// cannot be revoked before then, so it is kept short; the user it names is
// still reloaded on every request.
const DefaultBearerMaxLifetime = 15 * time.Minute

// BearerEnabled reports whether any bearer audience is configured.
func (v *Verifier) BearerEnabled() bool { return len(v.cfg.BearerAudiences) > 0 }

// VerifyBearer checks a token sent as a bearer on an API request, the way a
// remote MCP client sends one on every call (ADR 0050 D9). It applies the
// handoff's checks (signature, issuer, expiry, subject, bounded lifetime,
// allowed tenant) with two differences: the audience must be one of
// BearerAudiences and never the handoff Audience, and there is no one-use
// rule, because the same token authenticates every request until it expires.
func (v *Verifier) VerifyBearer(ctx context.Context, raw string) (*Identity, error) {
	if !v.BearerEnabled() {
		return nil, ErrBearerDisabled
	}
	tok, err := v.bearer.Verify(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	if slices.Contains(tok.Audience, v.cfg.Audience) || !slices.ContainsFunc(tok.Audience, v.bearerAudience) {
		return nil, fmt.Errorf("%w: audience %v is not a bearer audience", ErrInvalidToken, tok.Audience)
	}
	id, claims, err := v.identity(tok, v.cfg.BearerMaxLifetime)
	if err != nil {
		return nil, err
	}
	if id.Scopes, err = bearerScopes(claims); err != nil {
		return nil, err
	}
	return id, nil
}

// bearerScopes reads the OAuth scope claim, a space-separated string (RFC
// 9068). A token without one may only read (ADR 0067), so tokens minted
// before scopes existed keep reading and nothing more; an issuer opts in to
// anything else explicitly. A claim of another type is refused.
func bearerScopes(claims map[string]any) ([]string, error) {
	raw, present := claims["scope"]
	if !present {
		return []string{auth.ScopeRead}, nil
	}
	s, ok := raw.(string)
	if !ok {
		return nil, fmt.Errorf("%w: scope claim is %T, want a space-separated string", ErrInvalidToken, raw)
	}
	return append([]string{}, strings.Fields(s)...), nil
}

func (v *Verifier) bearerAudience(aud string) bool {
	return slices.Contains(v.cfg.BearerAudiences, aud)
}
