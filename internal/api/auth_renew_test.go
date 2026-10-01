package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
)

// fakeRenewer records the renewal request and returns a canned result, so the
// handler's status mapping can be exercised without a real signing key.
type fakeRenewer struct {
	renewed string
	ok      bool
	err     error

	gotCtx   context.Context
	gotToken string
	gotTTL   time.Duration
	gotMax   time.Duration
}

func (f *fakeRenewer) RenewUserToken(ctx context.Context, token string, ttl, maxLifetime time.Duration) (renewed string, ok bool, err error) {
	f.gotCtx, f.gotToken, f.gotTTL, f.gotMax = ctx, token, ttl, maxLifetime
	return f.renewed, f.ok, f.err
}

func renewServer(r TokenRenewer) *gin.Engine {
	return NewServer(Dependencies{
		Logger:               discardLogger(),
		Authenticator:        &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:          auth.NewRateLimiter(5, time.Minute),
		HealthChecks:         map[string]HealthChecker{},
		CORSOrigins:          []string{"*"},
		TokenTTLSecs:         3600,
		TokenRenewer:         r,
		TokenMaxLifetimeSecs: 86400,
	})
}

func postRenew(srv *gin.Engine, bearer string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v2/auth/token/renew", http.NoBody)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// TestRenewTokenReturnsFreshToken: a valid bearer is renewed into a fresh access
// token, returned in the same shape as /auth/token, and the handler passes the
// configured TTL and max_lifetime to the renewer along with the caller's bearer.
func TestRenewTokenReturnsFreshToken(t *testing.T) {
	r := &fakeRenewer{renewed: "new-token", ok: true}
	rec := postRenew(renewServer(r), "current-token")
	if rec.Code != http.StatusOK {
		t.Fatalf("renew = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp tokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.AccessToken != "new-token" || resp.TokenType != "bearer" || resp.ExpiresIn != 3600 {
		t.Errorf("response = %+v, want access_token=new-token bearer expires_in=3600", resp)
	}
	if r.gotToken != "current-token" {
		t.Errorf("renewer got token %q, want the caller's bearer", r.gotToken)
	}
	if r.gotTTL != time.Hour {
		t.Errorf("renewer TTL = %v, want 1h (TokenTTLSecs)", r.gotTTL)
	}
	if r.gotMax != 24*time.Hour {
		t.Errorf("renewer max_lifetime = %v, want 24h (TokenMaxLifetimeSecs)", r.gotMax)
	}
	// The renewer reloads the user from the store, so it must run under the
	// request's context rather than a detached one (#801).
	if r.gotCtx == nil {
		t.Error("renewer got a nil context; the store reload must be canceled with the request")
	}
}

// TestRenewTokenInvalidIsUnauthorized: when the renewer rejects the token (bad
// signature / expired), the handler answers 401 so the CLI falls back to login.
func TestRenewTokenInvalidIsUnauthorized(t *testing.T) {
	r := &fakeRenewer{err: auth.ErrInvalidToken}
	rec := postRenew(renewServer(r), "expired-token")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("renew of invalid token = %d, want 401", rec.Code)
	}
}

// TestRenewTokenPastMaxLifetimeIsUnauthorized: a token past the session
// max_lifetime (renewer returns ok=false, no error) is refused with 401 — the
// user must re-authenticate.
func TestRenewTokenPastMaxLifetimeIsUnauthorized(t *testing.T) {
	r := &fakeRenewer{ok: false}
	rec := postRenew(renewServer(r), "aged-token")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("renew past max_lifetime = %d, want 401", rec.Code)
	}
}

// TestRenewTokenDuringAnOutageIsNot401 is the renew route's half of #1087.
//
// The route re-proves the principal against the user store, so it fails for the
// same two unrelated reasons every other authenticated route does: the token was
// judged and rejected, or the store could not be reached to judge it. Collapsing
// both to 401 "log in again" during a database outage points the client and
// whoever is on call at the credential instead of the incident, and — because
// the handler used AbortProblem rather than AbortProblemCause — dropped the
// driver's error entirely, leaving the outage invisible on this route.
func TestRenewTokenDuringAnOutageIsNot401(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dbDown := errors.New("failed to connect to `user=leoflow database=leoflow`: dial tcp 10.0.0.1:5432: i/o timeout")

	var logBuf bytes.Buffer
	srv := NewServer(Dependencies{
		Logger:               slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Authenticator:        &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:          auth.NewRateLimiter(5, time.Minute),
		HealthChecks:         map[string]HealthChecker{},
		CORSOrigins:          []string{"*"},
		TokenTTLSecs:         3600,
		TokenRenewer:         &fakeRenewer{err: dbDown},
		TokenMaxLifetimeSecs: 86400,
	})
	rec := postRenew(srv, "still-valid-token")

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 — the token was never judged, the store was unreachable", rec.Code)
	}
	for _, leak := range []string{"dial tcp", "10.0.0.1", "user=leoflow", "log in again"} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Errorf("body leaks or misattributes %q: %s", leak, rec.Body.String())
		}
	}
	if !strings.Contains(logBuf.String(), "dial tcp") {
		t.Errorf("the log must carry the cause the body withheld; got %s", logBuf.String())
	}
}
