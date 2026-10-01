package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
