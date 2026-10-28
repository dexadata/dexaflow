package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/dexadata/dexaflow/internal/config"
)

// testJWT builds a signed HS256 token carrying the given iat/exp. autoRefreshToken
// reads those claims WITHOUT verifying the signature, so any secret works here.
func testJWT(t *testing.T, iat, exp time.Time) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject:   "u1",
		IssuedAt:  jwt.NewNumericDate(iat),
		ExpiresAt: jwt.NewNumericDate(exp),
	})
	s, err := tok.SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatalf("signing test JWT: %v", err)
	}
	return s
}

// TestAutoRefreshTokenRenewsNearExpiry: a persisted session token past the halfway
// point of its life is transparently renewed against the control plane, and the
// fresh token is written back to the config file — no prompt, no re-login (aresta
// #5). The renew call carries the current bearer.
func TestAutoRefreshTokenRenewsNearExpiry(t *testing.T) {
	t.Parallel()
	now := time.Now()
	old := testJWT(t, now.Add(-55*time.Minute), now.Add(5*time.Minute)) // 5m left of a 60m TTL

	var called bool
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/auth/token/renew" {
			t.Errorf("renew posted to %q, want /api/v2/auth/token/renew", r.URL.Path)
		}
		called = true
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"renewed-jwt","token_type":"bearer","expires_in":3600}`)
	}))
	defer srv.Close()

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.PersistSession(cfgPath, srv.URL, old); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	got := autoRefreshToken(context.Background(), cfgPath, srv.URL, old)
	if !called {
		t.Fatal("a near-expiry token must trigger a renew call")
	}
	if got != "renewed-jwt" {
		t.Errorf("returned token = %q, want renewed-jwt", got)
	}
	if gotAuth != "Bearer "+old {
		t.Errorf("renew Authorization = %q, want the current bearer", gotAuth)
	}
	cfg, err := config.Load(cfgPath, nil)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if cfg.Token != "renewed-jwt" {
		t.Errorf("persisted token = %q, want the renewed one", cfg.Token)
	}
}

// TestAutoRefreshTokenSkipsHealthyToken: a token with plenty of life left is not
// renewed — no network call, config untouched.
func TestAutoRefreshTokenSkipsHealthyToken(t *testing.T) {
	t.Parallel()
	now := time.Now()
	healthy := testJWT(t, now, now.Add(60*time.Minute)) // full life ahead

	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("a healthy token must NOT trigger a renew call")
	}))
	defer srv.Close()

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.PersistSession(cfgPath, srv.URL, healthy); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	if got := autoRefreshToken(context.Background(), cfgPath, srv.URL, healthy); got != healthy {
		t.Errorf("returned token = %q, want the unchanged healthy token", got)
	}
	cfg, _ := config.Load(cfgPath, nil)
	if cfg.Token != healthy {
		t.Errorf("config token changed to %q, want it untouched", cfg.Token)
	}
}

// TestAutoRefreshTokenFallsBackOnRenewFailure: when renewal fails (e.g. past
// max_lifetime -> 401), the original token is returned and the config is left
// untouched, so the command proceeds and its own 401 handling asks the user to log
// in again — never worse than today's behavior.
func TestAutoRefreshTokenFallsBackOnRenewFailure(t *testing.T) {
	t.Parallel()
	now := time.Now()
	old := testJWT(t, now.Add(-55*time.Minute), now.Add(5*time.Minute))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.PersistSession(cfgPath, srv.URL, old); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	if got := autoRefreshToken(context.Background(), cfgPath, srv.URL, old); got != old {
		t.Errorf("on renew failure, returned %q, want the original token", got)
	}
	cfg, _ := config.Load(cfgPath, nil)
	if cfg.Token != old {
		t.Errorf("config token = %q, want the original (untouched on failure)", cfg.Token)
	}
}

// TestAutoRefreshTokenIgnoresNonJWT: a token that is not a decodable JWT (e.g. an
// opaque CI token) is returned as-is with no network call — refresh only applies
// to leoflow's own expiring JWTs.
func TestAutoRefreshTokenIgnoresNonJWT(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("a non-JWT token must NOT trigger a renew call")
	}))
	defer srv.Close()

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.PersistSession(cfgPath, srv.URL, "opaque-ci-token"); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if got := autoRefreshToken(context.Background(), cfgPath, srv.URL, "opaque-ci-token"); got != "opaque-ci-token" {
		t.Errorf("returned %q, want the opaque token unchanged", got)
	}
}

// renewOutageServer answers the first renew with 503 and retryAfter (no header
// when empty), then renews. It counts the calls.
func renewOutageServer(t *testing.T, retryAfter string, calls *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*calls++
		if *calls == 1 {
			if retryAfter != "" {
				w.Header().Set("Retry-After", retryAfter)
			}
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"title":"service unavailable","status":503,"detail":"authentication temporarily unavailable"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"renewed-jwt","token_type":"bearer","expires_in":3600}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestAutoRefreshTokenRetriesAfterAnOutage503 is the client half of #1100:
// the renew route answers 503 with Retry-After when the store could not check
// the token, and the CLI waits that long and renews once more instead of
// dropping the renewal. Driven through autoRefreshToken, the path commands use.
func TestAutoRefreshTokenRetriesAfterAnOutage503(t *testing.T) {
	t.Parallel()
	now := time.Now()
	old := testJWT(t, now.Add(-55*time.Minute), now.Add(5*time.Minute))
	var calls int
	srv := renewOutageServer(t, "0", &calls)

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.PersistSession(cfgPath, srv.URL, old); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	if got := autoRefreshToken(context.Background(), cfgPath, srv.URL, old); got != "renewed-jwt" {
		t.Errorf("returned %q, want the token renewed on the retry", got)
	}
	if calls != 2 {
		t.Errorf("renew calls = %d, want 2 (the 503, then one retry)", calls)
	}
	cfg, _ := config.Load(cfgPath, nil)
	if cfg.Token != "renewed-jwt" {
		t.Errorf("persisted token = %q, want the renewed one", cfg.Token)
	}
}

// TestRenewTokenWaitsTheRetryAfter: the wait is the server's Retry-After, not
// a guess of the client's.
func TestRenewTokenWaitsTheRetryAfter(t *testing.T) {
	t.Parallel()
	var calls int
	srv := renewOutageServer(t, "3", &calls)
	var waited []time.Duration
	wait := func(_ context.Context, d time.Duration) error {
		waited = append(waited, d)
		return nil
	}

	got, err := renewTokenWaiting(context.Background(), srv.URL, "tok", wait)
	if err != nil || got != "renewed-jwt" {
		t.Fatalf("renew = %q, %v; want renewed-jwt", got, err)
	}
	if len(waited) != 1 || waited[0] != 3*time.Second {
		t.Errorf("waited %v, want exactly [3s]", waited)
	}
}

// TestRenewTokenDoesNotRetryWithoutABoundedRetryAfter: a 503 without a
// Retry-After the CLI can honor is not retried. No header means the server gave
// no wait, and a wait above the bound would stall the command the renewal is
// only helping: either way the command goes ahead with its current token, as it
// did before.
func TestRenewTokenDoesNotRetryWithoutABoundedRetryAfter(t *testing.T) {
	t.Parallel()
	for name, header := range map[string]string{
		"no Retry-After":          "",
		"above the bound":         strconv.Itoa(int(maxRenewRetryAfter/time.Second) + 1),
		"negative":                "-1",
		"not a number of seconds": "Wed, 21 Oct 2026 07:28:00 GMT",
	} {
		t.Run(name, func(t *testing.T) {
			var calls int
			srv := renewOutageServer(t, header, &calls)
			wait := func(context.Context, time.Duration) error {
				t.Error("waited on a Retry-After the client must not honor")
				return nil
			}
			if got, err := renewTokenWaiting(context.Background(), srv.URL, "tok", wait); err == nil {
				t.Errorf("renew = %q, want an error so the caller keeps its token", got)
			}
			if calls != 1 {
				t.Errorf("renew calls = %d, want 1", calls)
			}
		})
	}
}

// TestRenewTokenStopsWaitingWhenTheCommandEnds: a canceled command neither
// sits out the rest of the wait nor renews after it.
func TestRenewTokenStopsWaitingWhenTheCommandEnds(t *testing.T) {
	t.Parallel()
	var calls int
	srv := renewOutageServer(t, strconv.Itoa(int(maxRenewRetryAfter/time.Second)), &calls)
	ctx, cancel := context.WithCancel(context.Background())
	wait := func(ctx context.Context, d time.Duration) error {
		cancel()
		return waitRetryAfter(ctx, d)
	}
	start := time.Now()
	if _, err := renewTokenWaiting(ctx, srv.URL, "tok", wait); err == nil {
		t.Error("renew succeeded after the command was canceled")
	}
	if took := time.Since(start); took >= maxRenewRetryAfter/2 {
		t.Errorf("renew took %s after the command was canceled, want it to stop waiting at once", took)
	}
	if calls != 1 {
		t.Errorf("renew calls = %d, want 1", calls)
	}
}

// TestRenewTokenRetriesOnlyOnce: a second 503 ends the renewal.
func TestRenewTokenRetriesOnlyOnce(t *testing.T) {
	t.Parallel()
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	if _, err := renewTokenWaiting(context.Background(), srv.URL, "tok", waitRetryAfter); err == nil {
		t.Error("renew succeeded against a server that only answers 503")
	}
	if calls != 2 {
		t.Errorf("renew calls = %d, want 2", calls)
	}
}
