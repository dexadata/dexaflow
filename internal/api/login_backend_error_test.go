package api

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
)

// dbDownAuthn simulates the control-plane DB being unreachable: IssueToken returns
// a non-credential backend error (as the store now propagates, #843).
type dbDownAuthn struct{}

func (dbDownAuthn) IssueToken(context.Context, auth.Credentials) (string, error) {
	return "", errors.New("looking up user for login: dial tcp: connection refused")
}

func (dbDownAuthn) Authenticate(context.Context, string) (*auth.User, error) {
	return nil, errors.New("db down")
}

func authnServer(a auth.Authenticator) *gin.Engine {
	return NewServer(Dependencies{
		Logger: discardLogger(), Authenticator: a,
		RateLimiter: auth.NewRateLimiter(100, time.Minute),
		CORSOrigins: []string{"*"}, TokenTTLSecs: 3600,
	})
}

// A DB outage during login must be 503 (backend unavailable), NOT 401 "invalid
// credentials" — the latter misleads operators and hides the incident (#843).
func TestLoginBackendDownReturns503(t *testing.T) {
	srv := authnServer(dbDownAuthn{})
	body := `{"username":"admin@leoflow.local","password":"whatever"}`
	if code := do(srv, http.MethodPost, "/auth/token", body).Code; code != http.StatusServiceUnavailable {
		t.Errorf("DB-down login = %d, want 503", code)
	}
}

// A genuine wrong credential stays 401 (generic, no enumeration).
func TestLoginBadCredentialsReturns401(t *testing.T) {
	srv := authnServer(credAuthn{})
	body := `{"username":"admin@leoflow.local","password":"wrong"}`
	if code := do(srv, http.MethodPost, "/auth/token", body).Code; code != http.StatusUnauthorized {
		t.Errorf("bad-credential login = %d, want 401", code)
	}
}

// A DB outage (503) must NOT consume the per-IP lockout budget — otherwise an
// outage locks every user out once the budget drains. With a tiny limit, repeated
// DB-down logins must all stay 503, never flip to 429 (#843 / review invariant).
func TestLoginBackendDownDoesNotConsumeRateLimit(t *testing.T) {
	srv := NewServer(Dependencies{
		Logger: discardLogger(), Authenticator: dbDownAuthn{},
		RateLimiter: auth.NewRateLimiter(2, time.Minute), // tiny budget
		CORSOrigins: []string{"*"}, TokenTTLSecs: 3600,
	})
	body := `{"username":"admin@leoflow.local","password":"x"}`
	for i := 0; i < 5; i++ {
		if code := do(srv, http.MethodPost, "/auth/token", body).Code; code != http.StatusServiceUnavailable {
			t.Fatalf("request %d = %d, want 503 every time (a 503 must not consume the lockout budget)", i, code)
		}
	}
}
