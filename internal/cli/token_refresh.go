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
// `dexaflow auth login` again on the hour (EKS validation aresta #5). It is the
// client half of the short-TTL-plus-renewal design: the access token stays
// short-lived (good security), while a genuinely live session is kept working
// silently.
//
// It is strictly best-effort. When the token is not a near-expiry Dexaflow JWT, or
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

// maxRenewRetryAfter bounds how long a renewal waits on a 503's Retry-After
// (#1100). The renewal only helps the command that runs it, so it never holds
// that command for long: a server asking for a longer wait gets no retry, and
// the command goes ahead with its current token.
const maxRenewRetryAfter = 10 * time.Second

// renewToken posts to /api/v2/auth/token/renew carrying the current bearer and
// returns the fresh access token, via the shared typed /api/v2 client (ADR 0050
// D8) rather than hand-rolling HTTP. A non-200 (notably 401 past max_lifetime)
// is an error, so the caller falls back to the current token. A 503 naming a
// Retry-After within maxRenewRetryAfter is retried once after that wait: the
// store could not check the token, which says nothing about it (#1097).
func renewToken(ctx context.Context, serverURL, token string) (string, error) {
	return renewTokenWaiting(ctx, serverURL, token, waitRetryAfter)
}

// renewTokenWaiting is renewToken with the wait between the 503 and the retry
// supplied by the caller.
func renewTokenWaiting(ctx context.Context, serverURL, token string, wait func(context.Context, time.Duration) error) (string, error) {
	renewed, retryAfter, err := renewOnce(ctx, serverURL, token)
	if err == nil || retryAfter < 0 {
		return renewed, err
	}
	if werr := wait(ctx, retryAfter); werr != nil {
		return "", fmt.Errorf("waiting %s to retry the renewal: %w", retryAfter, werr)
	}
	renewed, _, err = renewOnce(ctx, serverURL, token)
	return renewed, err
}

// renewOnce makes one renew call. On a 503 whose Retry-After is a number of
// seconds from 0 to maxRenewRetryAfter it also returns that wait; on every
// other outcome the wait is negative, meaning "do not retry".
func renewOnce(ctx context.Context, serverURL, token string) (string, time.Duration, error) {
	c, err := cliAPIClient(serverURL, token)
	if err != nil {
		return "", -1, err
	}
	resp, err := c.RenewTokenWithResponse(ctx)
	if err != nil {
		return "", -1, fmt.Errorf("posting to %s/api/v2/auth/token/renew: %w", serverURL, err)
	}
	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil && resp.JSON200.AccessToken != nil {
		return *resp.JSON200.AccessToken, -1, nil
	}
	return "", honoredRetryAfter(resp.Headers503), fmt.Errorf("renew returned %d", resp.StatusCode())
}

// honoredRetryAfter is the wait a 503's Retry-After asks for, or -1 when there
// is no 503, no header, or a value outside 0..maxRenewRetryAfter.
func honoredRetryAfter(h *apiclient.RenewTokenResponse503Headers) time.Duration {
	if h == nil || h.RetryAfter == nil {
		return -1
	}
	// A negative header is already a negative wait, which is "do not retry".
	d := time.Duration(*h.RetryAfter) * time.Second
	if d > maxRenewRetryAfter {
		return -1
	}
	return d
}

// waitRetryAfter waits d, or returns ctx's error when the command ends first.
func waitRetryAfter(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// autoRefreshWith is autoRefreshToken for a token stored somewhere other than
// the single-server session: persist saves the renewed token, and the original
// is returned unchanged when the token is not near expiry, the renewal fails,
// or persist does.
func autoRefreshWith(ctx context.Context, serverURL, token string, persist func(string) error) string {
	if serverURL == "" || token == "" {
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
	if err := persist(renewed); err != nil {
		return token
	}
	return renewed
}
