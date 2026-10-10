package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
)

// credAuthn issues a token only for password "right"; anything else is invalid
// credentials. It lets the rate-limit tests distinguish a successful login from
// a failed one (fakeAuthn ignores the password).
type credAuthn struct{}

func (credAuthn) IssueToken(_ context.Context, c auth.Credentials) (string, error) {
	if c.Password == "right" {
		return "jwt", nil
	}
	return "", auth.ErrInvalidCredentials
}

func (credAuthn) Authenticate(context.Context, string) (*auth.User, error) {
	return &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}, nil
}

func rateLimitedServer(limit int) *gin.Engine {
	return NewServer(Dependencies{
		Logger: discardLogger(), Authenticator: credAuthn{},
		RateLimiter: auth.NewRateLimiter(limit, time.Minute),
		CORSOrigins: []string{"*"}, TokenTTLSecs: 3600,
	})
}

func TestLoginRateLimitCountsOnlyFailures(t *testing.T) {
	good := `{"username":"admin@leoflow.local","password":"right"}`
	bad := `{"username":"admin@leoflow.local","password":"wrong"}`

	t.Run("successful logins never consume the budget", func(t *testing.T) {
		srv := rateLimitedServer(2) // tiny budget; successes must still all pass
		for i := 0; i < 6; i++ {
			if code := do(srv, http.MethodPost, "/auth/token", good).Code; code != http.StatusOK {
				t.Fatalf("successful login %d = %d, want 200 (a success must not count)", i, code)
			}
		}
	})

	t.Run("mistype a few times then get it right is NOT locked", func(t *testing.T) {
		srv := rateLimitedServer(3)
		for i := 0; i < 2; i++ {
			if code := do(srv, http.MethodPost, "/auth/token", bad).Code; code != http.StatusUnauthorized {
				t.Fatalf("wrong attempt %d = %d, want 401", i, code)
			}
		}
		if code := do(srv, http.MethodPost, "/auth/token", good).Code; code != http.StatusOK {
			t.Fatalf("correct password after mistypes = %d, want 200 (must not be locked)", code)
		}
	})

	t.Run("repeated failures still lock (anti-brute-force intact)", func(t *testing.T) {
		srv := rateLimitedServer(3)
		for i := 0; i < 3; i++ {
			do(srv, http.MethodPost, "/auth/token", bad)
		}
		if code := do(srv, http.MethodPost, "/auth/token", good).Code; code != http.StatusTooManyRequests {
			t.Fatalf("after exhausting the budget with failures, login = %d, want 429", code)
		}
	})
}
