package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/issuer"
	"github.com/dexadata/dexaflow/internal/ui"
)

// The host binding of #1463: with auth.trusted_issuer.host_pattern set, a
// browser session works only on its tenant's host.

const tenantHostPattern = "{tenant}.example.com"

// tenantAuthn authenticates "acme-session" as a user of acme and
// "globex-session" as a user of globex.
type tenantAuthn struct{}

func (tenantAuthn) IssueToken(context.Context, auth.Credentials) (string, error) { return "", nil }
func (tenantAuthn) Authenticate(_ context.Context, token string) (*auth.User, error) {
	switch token {
	case "acme-session":
		return &auth.User{ID: "u-acme", TenantID: "acme", Roles: []string{"admin"}}, nil
	case "globex-session":
		return &auth.User{ID: "u-globex", TenantID: "globex", Roles: []string{"admin"}}, nil
	}
	return nil, auth.ErrInvalidToken
}

func hostBoundServer(t *testing.T, pattern string, trustedProxies []string, audit AuthAuditWriter) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	return newTestServer(Dependencies{
		Observability: ObservabilityDeps{
			Logger: discardLogger(),
		},
		Server: ServerDeps{
			TrustedProxies: trustedProxies,
		},
		Auth: AuthDeps{
			Authenticator: tenantAuthn{},
			RateLimiter:   auth.NewRateLimiter(100, time.Minute),
			JWTSecret:     "test-secret",
			TokenTTLSecs:  900,
			AuthAudit:     audit,
		},
		Issuers: IssuerDeps{
			TrustedIssuer:            fakeTrustedIssuer{issuer.Identity{Subject: "user-42", Email: "ana@acme.com", Tenant: "acme"}},
			TrustedIssuerUsers:       activeAcmeUser(),
			TrustedIssuerOrigins:     []string{"https://portal.example.com"},
			TrustedIssuerHostPattern: pattern,
		},
		UI: UIDeps{
			SPA: ui.New(),
		},
	})
}

func postSessionOnHost(h http.Handler, host string) *httptest.ResponseRecorder {
	form := url.Values{"token": {"good"}, "next": {"/"}}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v2/auth/session", strings.NewReader(form.Encode()))
	req.Host = host
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://portal.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

type hostRequest struct {
	path, host, cookie, bearer, remoteAddr, forwardedHost string
}

func serveOnHost(h http.Handler, r hostRequest) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, r.path, http.NoBody)
	req.Host = r.host
	if r.cookie != "" {
		req.AddCookie(&http.Cookie{Name: authTokenCookie, Value: r.cookie})
	}
	if r.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+r.bearer)
	}
	if r.remoteAddr != "" {
		req.RemoteAddr = r.remoteAddr
	}
	if r.forwardedHost != "" {
		req.Header.Set("X-Forwarded-Host", r.forwardedHost)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func setsSessionCookie(rec *httptest.ResponseRecorder) bool {
	ck := namedCookie(rec, authTokenCookie)
	return ck != nil && ck.Value != ""
}

func clearsSessionCookie(rec *httptest.ResponseRecorder) bool {
	ck := namedCookie(rec, authTokenCookie)
	return ck != nil && ck.Value == "" && ck.MaxAge < 0
}

func TestHandoffOnTheTenantsHostIsAccepted(t *testing.T) {
	srv := hostBoundServer(t, tenantHostPattern, nil, nil)
	for _, host := range []string{"acme.example.com", "ACME.example.com", "acme.example.com:443"} {
		rec := postSessionOnHost(srv, host)
		if rec.Code != http.StatusSeeOther || !setsSessionCookie(rec) {
			t.Errorf("handoff for acme on %s = %d (cookie %v), want 303 with a session", host, rec.Code, setsSessionCookie(rec))
		}
	}
}

func TestHandoffOnAnotherTenantsHostIsRefused(t *testing.T) {
	audit := &fakeAuthAudit{}
	srv := hostBoundServer(t, tenantHostPattern, nil, audit)
	rec := postSessionOnHost(srv, "globex.example.com")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("handoff for acme on globex's host = %d, want 403", rec.Code)
	}
	if setsSessionCookie(rec) {
		t.Error("a refused handoff set a session cookie")
	}
	if !strings.Contains(rec.Body.String(), "host") {
		t.Errorf("refusal detail = %s, want one that names the host", rec.Body.String())
	}
	var found bool
	for _, e := range audit.events {
		if e.action == auditIssuerLoginFailure && e.extra["reason"] == "host_tenant_mismatch" && e.tenant == "acme" {
			found = true
		}
	}
	if !found {
		t.Errorf("audit = %+v, want issuer_login_failure with reason host_tenant_mismatch", audit.events)
	}
}

func TestCookieForAnotherTenantsHostIsRefused(t *testing.T) {
	srv := hostBoundServer(t, tenantHostPattern, nil, nil)
	if rec := serveOnHost(srv, hostRequest{path: "/api/v2/version", host: "acme.example.com", cookie: "acme-session"}); rec.Code != http.StatusOK {
		t.Errorf("acme cookie on acme's host = %d, want 200", rec.Code)
	}
	rec := serveOnHost(srv, hostRequest{path: "/api/v2/version", host: "globex.example.com", cookie: "acme-session"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("acme cookie on globex's host = %d, want 401", rec.Code)
	}
	if !clearsSessionCookie(rec) {
		t.Errorf("the refused session cookie was not cleared: %v", rec.Header().Values("Set-Cookie"))
	}
}

func TestShellGateRefusesACookieForAnotherTenantsHost(t *testing.T) {
	srv := hostBoundServer(t, tenantHostPattern, nil, nil)
	if rec := serveOnHost(srv, hostRequest{path: "/dags", host: "acme.example.com", cookie: "acme-session"}); rec.Code != http.StatusOK {
		t.Errorf("shell with acme cookie on acme's host = %d, want 200", rec.Code)
	}
	rec := serveOnHost(srv, hostRequest{path: "/dags", host: "globex.example.com", cookie: "acme-session"})
	if rec.Code != http.StatusFound {
		t.Fatalf("shell with acme cookie on globex's host = %d, want 302 to sign-in", rec.Code)
	}
	if !clearsSessionCookie(rec) {
		t.Errorf("the refused session cookie was not cleared: %v", rec.Header().Values("Set-Cookie"))
	}
}

// A token in the Authorization header is not bound: an API client sends it
// on purpose, and nothing can plant one in it the way a sibling host can set a
// cookie.
func TestBearerHeaderIsNotHostBound(t *testing.T) {
	srv := hostBoundServer(t, tenantHostPattern, nil, nil)
	if rec := serveOnHost(srv, hostRequest{path: "/api/v2/version", host: "globex.example.com", bearer: "acme-session"}); rec.Code != http.StatusOK {
		t.Errorf("acme bearer on globex's host = %d, want 200", rec.Code)
	}
}

func TestEmptyHostPatternKeepsTodaysBehavior(t *testing.T) {
	srv := hostBoundServer(t, "", nil, nil)
	if rec := postSessionOnHost(srv, "globex.example.com"); rec.Code != http.StatusSeeOther || !setsSessionCookie(rec) {
		t.Errorf("handoff on any host without a pattern = %d, want 303 with a session", rec.Code)
	}
	if rec := serveOnHost(srv, hostRequest{path: "/api/v2/version", host: "globex.example.com", cookie: "acme-session"}); rec.Code != http.StatusOK {
		t.Errorf("cookie on any host without a pattern = %d, want 200", rec.Code)
	}
}

// X-Forwarded-Host decides the host only when the peer is a trusted proxy;
// from anyone else it is a header the client chose.
func TestForwardedHostOnlyFromATrustedProxy(t *testing.T) {
	srv := hostBoundServer(t, tenantHostPattern, []string{"10.0.0.0/8"}, nil)
	viaProxy := hostRequest{path: "/api/v2/version", host: "engine.internal", cookie: "acme-session", remoteAddr: "10.1.2.3:4000", forwardedHost: "acme.example.com"}
	if rec := serveOnHost(srv, viaProxy); rec.Code != http.StatusOK {
		t.Errorf("acme cookie with X-Forwarded-Host acme from a trusted proxy = %d, want 200", rec.Code)
	}
	spoofed := hostRequest{path: "/api/v2/version", host: "globex.example.com", cookie: "acme-session", remoteAddr: "192.0.2.9:4000", forwardedHost: "acme.example.com"}
	if rec := serveOnHost(srv, spoofed); rec.Code != http.StatusUnauthorized {
		t.Errorf("acme cookie on globex's host with a spoofed X-Forwarded-Host = %d, want 401", rec.Code)
	}
}

// With a configured session cookie name (#1405), the binding reads and clears
// that cookie, not _token: a refusal that cleared _token would leave the
// configured cookie in the jar and the browser would retry it forever.
func TestHostBindingClearsTheConfiguredSessionCookie(t *testing.T) {
	const name = "__Host-dexaflow"
	gin.SetMode(gin.TestMode)
	srv := newTestServer(Dependencies{
		Observability: ObservabilityDeps{
			Logger: discardLogger(),
		},
		Auth: AuthDeps{
			Authenticator:     tenantAuthn{},
			RateLimiter:       auth.NewRateLimiter(100, time.Minute),
			JWTSecret:         "test-secret",
			TokenTTLSecs:      900,
			SessionCookieName: name,
		},
		Issuers: IssuerDeps{
			TrustedIssuerHostPattern: tenantHostPattern,
		},
		UI: UIDeps{
			SPA: ui.New(),
		},
	})
	serve := func(path, host string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, http.NoBody)
		req.Host = host
		req.AddCookie(&http.Cookie{Name: name, Value: "acme-session"})
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}
	if rec := serve("/api/v2/version", "acme.example.com"); rec.Code != http.StatusOK {
		t.Errorf("acme %s cookie on acme's host = %d, want 200", name, rec.Code)
	}
	for _, tc := range []struct {
		path string
		want int
	}{{"/api/v2/version", http.StatusUnauthorized}, {"/dags", http.StatusFound}} {
		rec := serve(tc.path, "globex.example.com")
		if rec.Code != tc.want {
			t.Fatalf("%s with acme %s cookie on globex's host = %d, want %d", tc.path, name, rec.Code, tc.want)
		}
		ck := namedCookie(rec, name)
		if ck == nil || ck.Value != "" || ck.MaxAge >= 0 {
			t.Errorf("%s: the refused %s cookie was not cleared: %v", tc.path, name, rec.Header().Values("Set-Cookie"))
		}
		if namedCookie(rec, authTokenCookie) != nil {
			t.Errorf("%s: a refusal touched %s instead of only %s: %v", tc.path, authTokenCookie, name, rec.Header().Values("Set-Cookie"))
		}
	}
}

// TestHandoffOnAnotherTenantsHostRedirectsToExternalSignIn locks that the host
// refusal is a handoff refusal like the others: with an external sign-in URL
// (#1288) the browser goes back there with error=host_tenant_mismatch.
func TestHandoffOnAnotherTenantsHostRedirectsToExternalSignIn(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := newTestServer(Dependencies{
		Observability: ObservabilityDeps{
			Logger: discardLogger(),
		},
		Auth: AuthDeps{
			Authenticator:     tenantAuthn{},
			RateLimiter:       auth.NewRateLimiter(100, time.Minute),
			JWTSecret:         "test-secret",
			TokenTTLSecs:      900,
			ExternalSignInURL: operatorSignIn,
		},
		Issuers: IssuerDeps{
			TrustedIssuer:            fakeTrustedIssuer{issuer.Identity{Subject: "user-42", Email: "ana@acme.com", Tenant: "acme"}},
			TrustedIssuerUsers:       activeAcmeUser(),
			TrustedIssuerOrigins:     []string{"https://portal.example.com"},
			TrustedIssuerHostPattern: tenantHostPattern,
		},
	})

	rec := postSessionOnHost(srv, "globex.example.com")

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 to the external sign-in (body %s)", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); !strings.HasSuffix(loc, "&error=host_tenant_mismatch") || !strings.HasPrefix(loc, "https://portal.example.com/sign-in?") {
		t.Errorf("Location = %q, want the external sign-in with error=host_tenant_mismatch", loc)
	}
	if setsSessionCookie(rec) {
		t.Error("a refused handoff set a session cookie")
	}
}
