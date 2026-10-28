package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/ui"
)

// errStoreDown is a user store that could not be reached: an error that is not
// auth.ErrInvalidToken, which is how #1097 classifies an outage.
var errStoreDown = errors.New("dial tcp 10.0.0.1:5432: i/o timeout")

// outageAuthn authenticates each token by its own rule: "good" is a session,
// "down" hits an unreachable store, "invalid-but-store-error" is a rejection
// that also carries a backend error, anything else is an invalid token.
type outageAuthn struct{}

func (outageAuthn) IssueToken(context.Context, auth.Credentials) (string, error) { return "", nil }
func (outageAuthn) Authenticate(_ context.Context, token string) (*auth.User, error) {
	switch token {
	case "good":
		return &auth.User{ID: "u1", Email: "a@b.c", TenantID: "default", Roles: []string{"admin"}}, nil
	case "tenantless":
		return &auth.User{ID: "u1"}, nil
	case "down":
		return nil, errStoreDown
	case "invalid-but-store-error":
		return nil, errors.Join(auth.ErrInvalidToken, errStoreDown)
	}
	return nil, auth.ErrInvalidToken
}

func shellOutageServer() *gin.Engine {
	return newTestServer(Dependencies{
		Observability: ObservabilityDeps{
			Logger: discardLogger(),
		},
		Server: ServerDeps{
			CORSOrigins: []string{"*"},
		},
		Auth: AuthDeps{
			Authenticator: outageAuthn{},
			RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		},
		UI: UIDeps{
			SPA: ui.New(),
		},
	})
}

// shellGet requests a SPA route with the given bearer and session cookie
// (either may be empty).
func shellGet(srv http.Handler, bearer, cookie string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/dags/etl/grid", http.NoBody)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: authTokenCookie, Value: cookie})
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// servedShell reports whether the response carries markup only the SPA shell has.
func servedShell(rec *httptest.ResponseRecorder) bool {
	body := rec.Body.String()
	return strings.Contains(body, `<div id="root"`) || strings.Contains(body, "<base href=")
}

// TestShellGateDuringAStoreOutage is the regression for #1099.
//
// The shell gate could only say "valid session" or not, so a session it could
// not check because the store was unreachable was sent to the login page, which
// cannot work during the outage either, with nothing saying why. Now the gate
// tells the two apart with the rule JWTAuth uses (#1097: an error that is not
// auth.ErrInvalidToken), and serves a static outage page for the second. It
// stays fail-closed: the shell is served only for a session that was verified.
func TestShellGateDuringAStoreOutage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := shellOutageServer()

	t.Run("no session still redirects to login", func(t *testing.T) {
		rec := shellGet(srv, "", "")
		if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/api/v2/auth/login") {
			t.Errorf("got %d to %q, want 302 to the login page", rec.Code, rec.Header().Get("Location"))
		}
	})

	for name, cookie := range map[string]string{
		"an invalid session":                 "nope",
		"a tenantless session":               "tenantless",
		"a rejection carrying a store error": "invalid-but-store-error",
	} {
		t.Run(name+" still redirects to login", func(t *testing.T) {
			rec := shellGet(srv, "", cookie)
			if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/api/v2/auth/login") {
				t.Errorf("got %d to %q, want 302 to the login page", rec.Code, rec.Header().Get("Location"))
			}
			if servedShell(rec) {
				t.Error("served the shell")
			}
		})
	}

	for name, creds := range map[string][2]string{
		"a cookie the store could not check":           {"", "down"},
		"a bearer the store could not check":           {"down", ""},
		"an invalid bearer and an unchecked cookie":    {"nope", "down"},
		"an unchecked bearer and an invalid cookie":    {"down", "nope"},
		"an unchecked bearer and a tenantless session": {"down", "tenantless"},
	} {
		t.Run(name+" gets the outage page", func(t *testing.T) {
			rec := shellGet(srv, creds[0], creds[1])
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503", rec.Code)
			}
			if servedShell(rec) {
				t.Fatal("served the authenticated shell for a session that was never verified")
			}
			if got := rec.Header().Get("Retry-After"); got != strconv.Itoa(outageRetryAfterSeconds) {
				t.Errorf("Retry-After = %q, want %d", got, outageRetryAfterSeconds)
			}
			if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
				t.Errorf("Cache-Control = %q, want no-store so the outage page is never served from a cache", cc)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Errorf("Content-Type = %q, want text/html", ct)
			}
			body := rec.Body.String()
			if !strings.Contains(body, "temporarily unavailable") {
				t.Errorf("the page does not say the service is temporarily unavailable: %s", body)
			}
			for _, leak := range []string{"<script", "dial tcp", "10.0.0.1", "/static/", "/dags/etl/grid"} {
				if strings.Contains(body, leak) {
					t.Errorf("the outage page carries %q", leak)
				}
			}
			if c := rec.Header().Get("Set-Cookie"); c != "" {
				t.Errorf("Set-Cookie = %q: an outage must not sign the browser out of a session it could not check", c)
			}
		})
	}

	for name, creds := range map[string][2]string{
		"a valid cookie":                         {"", "good"},
		"a valid bearer":                         {"good", ""},
		"an unchecked bearer and a valid cookie": {"down", "good"},
	} {
		t.Run(name+" gets the shell", func(t *testing.T) {
			rec := shellGet(srv, creds[0], creds[1])
			if rec.Code != http.StatusOK || !servedShell(rec) {
				t.Errorf("got %d, want 200 with the SPA shell", rec.Code)
			}
		})
	}
}

// TestShellOutagePageWithHostBinding: under a host binding (#1463) an
// outage page still clears no cookie, since the gate never learned which
// tenant the session belongs to.
func TestShellOutagePageWithHostBinding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := newTestServer(Dependencies{
		Observability: ObservabilityDeps{
			Logger: discardLogger(),
		},
		Server: ServerDeps{
			CORSOrigins: []string{"*"},
		},
		Auth: AuthDeps{
			Authenticator: outageAuthn{},
			RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		},
		Issuers: IssuerDeps{
			TrustedIssuerHostPattern: tenantHostPattern,
		},
		UI: UIDeps{
			SPA: ui.New(),
		},
	})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/dags", http.NoBody)
	req.Host = "acme.example.com"
	req.AddCookie(&http.Cookie{Name: authTokenCookie, Value: "down"})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || servedShell(rec) {
		t.Fatalf("got %d (shell=%v), want the 503 outage page", rec.Code, servedShell(rec))
	}
	if c := rec.Header().Get("Set-Cookie"); c != "" {
		t.Errorf("Set-Cookie = %q, want the session cookie left alone", c)
	}
}

// TestAPITokensPageDuringAStoreOutage: the API tokens screen (#1886) has its
// own session gate, which must tell an unreachable store from a missing
// session the way the shell gate does (#1099): the outage page for a session
// that could not be checked, the sign-in redirect for none.
func TestAPITokensPageDuringAStoreOutage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := newTestServer(Dependencies{
		Observability: ObservabilityDeps{
			Logger: discardLogger(),
		},
		Auth: AuthDeps{
			Authenticator: outageAuthn{},
			RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		},
		Stores: StoreDeps{
			APITokens: &fakeAPITokens{},
		},
	})

	rec := pageGet(t, srv, apiTokensPagePath, "down")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unchecked session: status = %d to %q, want 503", rec.Code, rec.Header().Get("Location"))
	}
	if !strings.Contains(rec.Body.String(), "temporarily unavailable") {
		t.Errorf("unchecked session: not the outage page: %s", rec.Body.String())
	}

	rec = pageGet(t, srv, apiTokensPagePath, "nope")
	if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/api/v2/auth/login") {
		t.Errorf("invalid session: got %d to %q, want 302 to the login page", rec.Code, rec.Header().Get("Location"))
	}
}
