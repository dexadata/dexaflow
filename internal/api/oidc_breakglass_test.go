package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
)

// emailPassAuthn issues a token only for one specific email+password, so the
// break-glass tests can distinguish an allowlisted success from a rejection.
type emailPassAuthn struct{ email, pass string }

func (a emailPassAuthn) IssueToken(_ context.Context, c auth.Credentials) (string, error) {
	if c.Username == a.email && c.Password == a.pass {
		return "session-token", nil
	}
	return "", auth.ErrInvalidCredentials
}

func (a emailPassAuthn) Authenticate(context.Context, string) (*auth.User, error) {
	return &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}, nil
}

// breakGlassServer builds a server in OIDC break-glass mode: the credential path
// admits only the given allowlisted email (with the given password).
func breakGlassServer(allow []string, authn auth.Authenticator, audit AuthAuditWriter) *gin.Engine {
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: authn,
		RateLimiter:   auth.NewRateLimiter(50, time.Minute),
		HealthChecks:  map[string]HealthChecker{},
		CORSOrigins:   []string{"*"},
		TokenTTLSecs:  3600,
		AuthAudit:     audit,
		OIDCEnabled:   true,
		OIDCSettings:  config.OIDCSection{BreakGlassEmails: allow},
	})
}

// Under OIDC, an EMPTY break-glass allowlist must mean SSO-only: every password
// login is rejected. The bug (ADR 0060 sibling / #826 companion): newBreakGlass
// returned nil for an empty list, leaving POST /auth/token fully open for every
// password user (e.g. the seeded admin) — the exact bypass SSO exists to close.
func TestBreakGlassOIDCEmptyAllowlistDeniesAllPasswordLogins(t *testing.T) {
	authn := emailPassAuthn{email: "admin@corp.example", pass: "right"}
	audit := &fakeAuthAudit{}
	srv := breakGlassServer(nil, authn, audit) // OIDC mode, empty allowlist

	rec := do(srv, http.MethodPost, "/auth/token", `{"username":"admin@corp.example","password":"right"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("password login under OIDC with empty allowlist = %d, want 401 (SSO-only)", rec.Code)
	}
	if !audit.has(auditBreakGlass, "denied") {
		t.Error("SSO-only denial was not audited")
	}
}

func TestBreakGlassAllowsOnlyAllowlistedEmail(t *testing.T) {
	authn := emailPassAuthn{email: "admin@corp.example", pass: "right"}
	audit := &fakeAuthAudit{}
	srv := breakGlassServer([]string{"admin@corp.example"}, authn, audit)

	t.Run("allowlisted email + right password → 200, audited success", func(t *testing.T) {
		rec := do(srv, http.MethodPost, "/auth/token", `{"username":"admin@corp.example","password":"right"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("break-glass login = %d, want 200", rec.Code)
		}
		if !audit.has(auditBreakGlass, "success") {
			t.Error("break-glass success was not audited")
		}
	})

	t.Run("non-allowlisted email → 401, audited denied", func(t *testing.T) {
		rec := do(srv, http.MethodPost, "/auth/token", `{"username":"someone@corp.example","password":"right"}`)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("non-break-glass login = %d, want 401", rec.Code)
		}
		if !audit.has(auditBreakGlass, "denied") {
			t.Error("break-glass denial was not audited")
		}
	})

	t.Run("allowlisted email + wrong password → 401, audited", func(t *testing.T) {
		rec := do(srv, http.MethodPost, "/auth/token", `{"username":"admin@corp.example","password":"wrong"}`)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("wrong password = %d, want 401", rec.Code)
		}
		if !audit.has(auditBreakGlass, "bad_credentials") {
			t.Error("break-glass bad-credential attempt was not audited")
		}
	})
}

// TestJWTModeCredentialPathUngated locks that with no OIDC configured (the
// default), /auth/token is exactly as before: any credential the authenticator
// accepts works, with no break-glass gating and no auth-event audit.
func TestJWTModeCredentialPathUngated(t *testing.T) {
	audit := &fakeAuthAudit{}
	// No BreakGlassEmails, no OIDCFlow → bg is nil, path ungated.
	srv := NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: credAuthn{}, // issues for password "right", any username
		RateLimiter:   auth.NewRateLimiter(50, time.Minute),
		HealthChecks:  map[string]HealthChecker{},
		CORSOrigins:   []string{"*"},
		TokenTTLSecs:  3600,
		AuthAudit:     audit,
	})

	rec := do(srv, http.MethodPost, "/auth/token", `{"username":"anyone@corp.example","password":"right"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("jwt-mode login = %d, want 200 (ungated)", rec.Code)
	}
	if len(audit.events) != 0 {
		t.Errorf("jwt mode must not emit auth-event audit; got %d events", len(audit.events))
	}
}

// TestJWTModeOIDCRoutesAbsent locks that the OIDC endpoints are not registered
// unless a flow was discovered at boot (provider: jwt is the default).
func TestJWTModeOIDCRoutesAbsent(t *testing.T) {
	srv := testServer(&fakeAuthn{})
	for _, path := range []string{"/api/v2/auth/oidc/login", "/api/v2/auth/oidc/callback"} {
		rec := do(srv, http.MethodGet, path, "")
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s in jwt mode = %d, want 404 (route absent)", path, rec.Code)
		}
	}
}
