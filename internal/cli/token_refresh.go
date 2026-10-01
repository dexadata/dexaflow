package cli

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/dexadata/dexaflow/internal/config"
	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

// absoluteRenewWindow is the fallback near-expiry window used when a token carries
// no iat (so its full lifetime is unknown): renew once it is within this window of
// expiring. The primary rule is the halfway point of the token's own lifetime.
const absoluteRenewWindow = 15 * time.Minute

// autoRefreshToken transparently renews a near-expiry persisted session token and
// rewrites configPath with the fresh one, so a long CLI/dev session never has to
// `leoflow auth login` again on the hour (EKS validation aresta #5). It is the
// client half of the short-TTL-plus-renewal design: the access token stays
// short-lived (good security), while a genuinely live session is kept working
// silently.
//
// It is strictly best-effort. When the token is not a near-expiry Leoflow JWT, or
// the renew call fails (network, or a 401 because the session is past the
// server's max_lifetime), the ORIGINAL token is returned unchanged and the config
// is left untouched — the command then proceeds exactly as it does today and its
// own 401 handling asks the user to log in again. It never fails a command.
func autoRefreshToken(ctx context.Context, configPath, serverURL, token string) string {
	if configPath == "" || serverURL == "" || token == "" {
		return token
	}
	iat, exp, ok := tokenIssuedExpiry(token)
	if !ok || !nearExpiry(iat, exp, time.Now()) {
		return token
	}
	renewed, err := renewToken(ctx, serverURL, token)
	if err != nil || renewed == "" {
		return token
	}
	if err := config.PersistSession(configPath, serverURL, renewed); err != nil {
		return token
	}
	return renewed
}

// tokenIssuedExpiry decodes a JWT's iat/exp WITHOUT verifying its signature — the
// CLI holds the token but not the signing secret, and it only needs the timing to
// decide when to renew (the server re-validates the signature on renew). It
// returns ok=false for an opaque/non-JWT token or one without an exp claim, so
// such tokens are never touched.
func tokenIssuedExpiry(token string) (iat, exp time.Time, ok bool) {
	var c jwt.RegisteredClaims
	if _, _, err := jwt.NewParser().ParseUnverified(token, &c); err != nil {
		return time.Time{}, time.Time{}, false
	}
	if c.ExpiresAt == nil {
		return time.Time{}, time.Time{}, false
	}
	if c.IssuedAt != nil {
		iat = c.IssuedAt.Time
	}
	return iat, c.ExpiresAt.Time, true
}

// nearExpiry reports whether it is time to renew: the token is at or past the
// halfway point of its own lifetime (iat..exp). Renewing at the halfway mark keeps
// a comfortable margin so no command ever races the expiry. When iat is unknown,
// it falls back to renewing within absoluteRenewWindow of expiry.
func nearExpiry(iat, exp, now time.Time) bool {
	if iat.IsZero() || !exp.After(iat) {
		return exp.Sub(now) <= absoluteRenewWindow
	}
	half := exp.Sub(iat) / 2
	return exp.Sub(now) <= half
}

// renewToken posts to /api/v2/auth/token/renew carrying the current bearer and
// returns the fresh access token, via the shared typed /api/v2 client (ADR 0050
// D8) rather than hand-rolling HTTP. A non-200 (notably 401 past max_lifetime)
// is an error, so the caller falls back to the current token.
func renewToken(ctx context.Context, serverURL, token string) (string, error) {
	c, err := apiclient.New(serverURL, token)
	if err != nil {
		return "", err
	}
	resp, err := c.RenewTokenWithResponse(ctx)
	if err != nil {
		return "", fmt.Errorf("posting to %s/api/v2/auth/token/renew: %w", serverURL, err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil || resp.JSON200.AccessToken == nil {
		return "", fmt.Errorf("renew returned %d", resp.StatusCode())
	}
	return *resp.JSON200.AccessToken, nil
}
