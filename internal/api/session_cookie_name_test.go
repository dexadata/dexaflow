package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/issuer"
	"github.com/dexadata/dexaflow/internal/ui"
)

// hostCookie is a __Host- session cookie name (#1405). A browser accepts such
// a cookie only when it is Secure, has Path=/ and carries no Domain, so a page
// on a sibling host cannot plant one.
const hostCookie = "__Host-dexaflow"

// namedCookieServer serves the password login, the data plane and the SPA
// shell with the session cookie renamed. tokenAuthn accepts "good" only, and
// credAuthn's password login mints "jwt", so the data plane is checked with
// "good" and the login with its own cookie.
func namedCookieServer(name string, authn auth.Authenticator) *gin.Engine {
	return newTestServer(Dependencies{
		Observability: ObservabilityDeps{
			Logger: discardLogger(),
		},
		Server: ServerDeps{
			CORSOrigins: []string{"*"},
		},
		Auth: AuthDeps{
			Authenticator:     authn,
			RateLimiter:       auth.NewRateLimiter(50, time.Minute),
			TokenTTLSecs:      3600,
			SessionCookieName: name,
		},
		UI: UIDeps{
			SPA: ui.New(),
		},
	})
}

func getWithCookie(srv http.Handler, path string, ck *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, http.NoBody)
	if ck != nil {
		req.AddCookie(ck)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestPasswordLoginSetsTheConfiguredCookieName(t *testing.T) {
	rec := do(namedCookieServer(hostCookie, credAuthn{}), http.MethodPost, "/auth/token", goodCreds)
	if rec.Code != http.StatusOK {
		t.Fatalf("login = %d, want 200", rec.Code)
	}
	if ck := namedCookie(rec, authTokenCookie); ck != nil {
		t.Errorf("login still set %s although the session cookie is named %s", authTokenCookie, hostCookie)
	}
	ck := namedCookie(rec, hostCookie)
	if ck == nil || ck.Value == "" {
		t.Fatalf("login set no %s cookie", hostCookie)
	}
	// The browser drops a __Host- cookie that misses any of these.
	if !ck.Secure || ck.Path != "/" || ck.Domain != "" {
		t.Errorf("%s cookie = %q, want Secure, Path=/ and no Domain", hostCookie, ck.String())
	}
	if !ck.HttpOnly || ck.SameSite != http.SameSiteLaxMode {
		t.Errorf("%s cookie = %q, want the same HttpOnly and SameSite=Lax as the default", hostCookie, ck.String())
	}
}

func TestDataPlaneReadsOnlyTheConfiguredCookie(t *testing.T) {
	srv := namedCookieServer(hostCookie, tokenAuthn{valid: "good"})
	if rec := getWithCookie(srv, "/api/v2/version", &http.Cookie{Name: hostCookie, Value: "good"}); rec.Code != http.StatusOK {
		t.Errorf("request with the %s cookie = %d, want 200", hostCookie, rec.Code)
	}
	// A _token planted from a sibling host is exactly what the rename is for:
	// it must not authenticate once the session cookie has another name.
	if rec := getWithCookie(srv, "/api/v2/version", &http.Cookie{Name: authTokenCookie, Value: "good"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("request with a %s cookie = %d, want 401 while the session cookie is %s", authTokenCookie, rec.Code, hostCookie)
	}
}

func TestShellGateReadsOnlyTheConfiguredCookie(t *testing.T) {
	srv := namedCookieServer(hostCookie, tokenAuthn{valid: "good"})
	if rec := getWithCookie(srv, "/dags/etl/grid", &http.Cookie{Name: hostCookie, Value: "good"}); rec.Code != http.StatusOK {
		t.Errorf("shell with the %s cookie = %d, want 200", hostCookie, rec.Code)
	}
	if rec := getWithCookie(srv, "/dags/etl/grid", &http.Cookie{Name: authTokenCookie, Value: "good"}); rec.Code != http.StatusFound {
		t.Errorf("shell with a %s cookie = %d, want 302 to login", authTokenCookie, rec.Code)
	}
}

func TestLogoutClearsTheConfiguredCookie(t *testing.T) {
	rec := do(namedCookieServer(hostCookie, credAuthn{}), http.MethodGet, "/api/v2/auth/logout", "")
	ck := namedCookie(rec, hostCookie)
	if ck == nil || ck.Value != "" || ck.MaxAge >= 0 {
		t.Fatalf("logout did not clear %s: %v", hostCookie, rec.Header().Values("Set-Cookie"))
	}
	if !ck.Secure || ck.Path != "/" || ck.Domain != "" {
		t.Errorf("logout cookie = %q, want the attributes login set, or the browser keeps the session", ck.String())
	}
}

func TestEmptyCookieNameKeepsToken(t *testing.T) {
	rec := do(namedCookieServer("", credAuthn{}), http.MethodPost, "/auth/token", goodCreds)
	if namedCookie(rec, authTokenCookie) == nil {
		t.Errorf("login with no cookie name set no %s cookie: an existing install would lose its sessions", authTokenCookie)
	}
}

func TestIssuerHandoffSetsTheConfiguredCookieName(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := newTestServer(Dependencies{
		Observability: ObservabilityDeps{
			Logger: discardLogger(),
		},
		Auth: AuthDeps{
			RateLimiter:       auth.NewRateLimiter(100, time.Minute),
			JWTSecret:         "test-secret",
			TokenTTLSecs:      900,
			SessionCookieName: hostCookie,
		},
		Issuers: IssuerDeps{
			TrustedIssuer:        fakeTrustedIssuer{issuer.Identity{Subject: "user-42", Email: "ana@acme.com", Tenant: "acme"}},
			TrustedIssuerUsers:   activeAcmeUser(),
			TrustedIssuerOrigins: []string{"https://portal.example.com"},
		},
	})
	rec := postSession(srv, url.Values{"token": {"good"}, "next": {"/"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("handoff = %d, want 303 (body %s)", rec.Code, rec.Body.String())
	}
	if ck := namedCookie(rec, hostCookie); ck == nil || ck.Value == "" {
		t.Errorf("handoff set no %s cookie: %v", hostCookie, rec.Header().Values("Set-Cookie"))
	}
}
