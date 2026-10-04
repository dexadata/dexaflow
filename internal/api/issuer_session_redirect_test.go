package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/issuer"
)

// operatorSignIn is an external sign-in URL that already carries a query, so
// every redirect test also proves that query survives byte for byte.
const operatorSignIn = "https://portal.example.com/sign-in?lang=pt-BR&return=a%20b"

// browserAccept is what a browser sends on a form navigation.
const browserAccept = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"

type issuerSessionOpts struct {
	users     *fakeIssuerUsers
	signInURL string
	audit     AuthAuditWriter
	logger    *slog.Logger
}

func issuerSessionServerWith(t *testing.T, o issuerSessionOpts) http.Handler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	logger := o.logger
	if logger == nil {
		logger = discardLogger()
	}
	return NewServer(Dependencies{
		Logger:               logger,
		RateLimiter:          auth.NewRateLimiter(100, time.Minute),
		JWTSecret:            "test-secret",
		TokenTTLSecs:         900,
		TrustedIssuer:        fakeTrustedIssuer{issuer.Identity{Subject: "user-42", Email: "ana@acme.com", Tenant: "acme"}},
		TrustedIssuerUsers:   o.users,
		TrustedIssuerOrigins: []string{"https://portal.example.com"},
		ExternalSignInURL:    o.signInURL,
		AuthAudit:            o.audit,
	})
}

func postSessionAccept(h http.Handler, form url.Values, origin, accept string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v2/auth/session", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func setsSessionCookie(rec *httptest.ResponseRecorder) bool {
	c := rec.Header().Get("Set-Cookie")
	return strings.HasPrefix(c, authTokenCookie+"=") && !strings.HasPrefix(c, authTokenCookie+"=;")
}

// issuerRefusal is one way the handoff is refused: the request that causes it,
// the status it answers without an external sign-in URL, and the stable code
// it carries to that URL when one is configured.
type issuerRefusal struct {
	name   string
	form   url.Values
	origin string
	users  func() *fakeIssuerUsers
	status int
	code   string
}

func issuerRefusals() []issuerRefusal {
	const portal = "https://portal.example.com"
	good := url.Values{"token": {"good"}, "next": {"/dags"}}
	return []issuerRefusal{
		{"origin not allowed", good, "https://evil.example", activeAcmeUser, http.StatusForbidden, "origin_not_allowed"},
		{"missing origin", good, "", activeAcmeUser, http.StatusForbidden, "origin_not_allowed"},
		{"no token", url.Values{"next": {"/dags"}}, portal, activeAcmeUser, http.StatusBadRequest, "token_missing"},
		{"invalid token", url.Values{"token": {"forged"}}, portal, activeAcmeUser, http.StatusUnauthorized, "invalid_token"},
		{"replayed token", url.Values{"token": {"replayed"}}, portal, activeAcmeUser, http.StatusUnauthorized, "token_replayed"},
		{"token lifetime", url.Values{"token": {"lifetime"}}, portal, activeAcmeUser, http.StatusUnauthorized, "token_lifetime"},
		{"tenant not allowed", url.Values{"token": {"foreign-tenant"}}, portal, activeAcmeUser, http.StatusUnauthorized, "tenant_not_allowed"},
		{"user not linked", good, portal, func() *fakeIssuerUsers { return &fakeIssuerUsers{} }, http.StatusForbidden, "user_not_linked"},
		{"user inactive", good, portal, func() *fakeIssuerUsers {
			return &fakeIssuerUsers{user: &auth.User{ID: "u-1", TenantID: "acme"}}
		}, http.StatusForbidden, "user_inactive"},
		{"tenant mismatch", good, portal, func() *fakeIssuerUsers {
			return &fakeIssuerUsers{user: &auth.User{ID: "u-1", TenantID: "globex"}, active: true}
		}, http.StatusForbidden, "tenant_mismatch"},
		{"store failure", good, portal, func() *fakeIssuerUsers {
			return &fakeIssuerUsers{err: errors.New("db down")}
		}, http.StatusInternalServerError, "server_error"},
	}
}

// TestIssuerSessionRefusalRedirectsToExternalSignIn: with an external sign-in
// configured, a browser that is refused lands back on the operator's sign-in
// with the stable reason code, instead of on a raw problem+json page.
func TestIssuerSessionRefusalRedirectsToExternalSignIn(t *testing.T) {
	for _, tc := range issuerRefusals() {
		t.Run(tc.name, func(t *testing.T) {
			h := issuerSessionServerWith(t, issuerSessionOpts{users: tc.users(), signInURL: operatorSignIn})

			rec := postSessionAccept(h, tc.form, tc.origin, browserAccept)

			want := operatorSignIn + "&error=" + tc.code
			if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != want {
				t.Fatalf("got %d to %q, want 303 to %q (body %s)", rec.Code, rec.Header().Get("Location"), want, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); strings.Contains(ct, "json") {
				t.Errorf("redirect answered with Content-Type %q", ct)
			}
			if setsSessionCookie(rec) {
				t.Errorf("a refused handoff set a session cookie: %q", rec.Header().Get("Set-Cookie"))
			}
			if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
				t.Errorf("redirect Cache-Control = %q, want no-store", cc)
			}
		})
	}
}

// TestIssuerSessionRefusalKeepsProblemJSONWithoutExternalSignIn: with no
// external sign-in URL, every refusal answers exactly as before.
func TestIssuerSessionRefusalKeepsProblemJSONWithoutExternalSignIn(t *testing.T) {
	for _, tc := range issuerRefusals() {
		t.Run(tc.name, func(t *testing.T) {
			h := issuerSessionServerWith(t, issuerSessionOpts{users: tc.users()})

			rec := postSessionAccept(h, tc.form, tc.origin, browserAccept)

			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.status, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
				t.Errorf("Content-Type = %q, want application/problem+json", ct)
			}
			if loc := rec.Header().Get("Location"); loc != "" {
				t.Errorf("Location = %q, want none", loc)
			}
			var p Problem
			if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.Status != tc.status {
				t.Errorf("body %s is not a problem with status %d (%v)", rec.Body.String(), tc.status, err)
			}
			if strings.Contains(rec.Body.String(), tc.code) {
				t.Errorf("problem body %s names the internal reason %q", rec.Body.String(), tc.code)
			}
		})
	}
}

// TestIssuerSessionRefusalRedirectBuildsTheURL locks how the code is added to
// the operator's URL: appended to whatever query it has, URL-encoded, before
// any fragment.
func TestIssuerSessionRefusalRedirectBuildsTheURL(t *testing.T) {
	cases := []struct{ signIn, want string }{
		{"https://portal.example.com/login", "https://portal.example.com/login?error=origin_not_allowed"},
		{"https://portal.example.com/login?", "https://portal.example.com/login?error=origin_not_allowed"},
		{"https://portal.example.com/login?z=1&a=2", "https://portal.example.com/login?z=1&a=2&error=origin_not_allowed"},
		{"https://portal.example.com/login?next=%2Fhome%3Fx%3D1", "https://portal.example.com/login?next=%2Fhome%3Fx%3D1&error=origin_not_allowed"},
		{"https://portal.example.com/login#top", "https://portal.example.com/login?error=origin_not_allowed#top"},
		{"https://portal.example.com/login?a=1#top", "https://portal.example.com/login?a=1&error=origin_not_allowed#top"},
		{"https://portal.example.com/login?error=old&a=1", "https://portal.example.com/login?a=1&error=origin_not_allowed"},
		{"https://portal.example.com/login?error&a=1&error=x", "https://portal.example.com/login?a=1&error=origin_not_allowed"},
	}
	for _, tc := range cases {
		t.Run(tc.signIn, func(t *testing.T) {
			h := issuerSessionServerWith(t, issuerSessionOpts{users: activeAcmeUser(), signInURL: tc.signIn})

			rec := postSessionAccept(h, url.Values{"token": {"good"}}, "https://evil.example", browserAccept)

			if got := rec.Header().Get("Location"); rec.Code != http.StatusSeeOther || got != tc.want {
				t.Errorf("got %d to %q, want 303 to %q", rec.Code, got, tc.want)
			}
		})
	}
}

// TestIssuerSessionRedirectTargetIsOnlyTheConfiguredURL: nothing the request
// carries (next, Referer, a form field) can steer where a refusal redirects,
// so the refusal path cannot become an open redirect.
func TestIssuerSessionRedirectTargetIsOnlyTheConfiguredURL(t *testing.T) {
	h := issuerSessionServerWith(t, issuerSessionOpts{users: &fakeIssuerUsers{}, signInURL: operatorSignIn})
	form := url.Values{"token": {"good"}, "next": {"https://evil.example/x"}, "error": {"https://evil.example"}, "redirect": {"https://evil.example"}}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v2/auth/session", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://portal.example.com")
	req.Header.Set("Referer", "https://evil.example/page")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if want := operatorSignIn + "&error=user_not_linked"; rec.Header().Get("Location") != want {
		t.Errorf("Location = %q, want %q", rec.Header().Get("Location"), want)
	}
}

// TestIssuerSessionJSONClientsKeepProblemJSON: a caller that asks for JSON and
// not HTML is a script, not a browser navigation, and gets the problem it can
// parse even when an external sign-in is configured.
func TestIssuerSessionJSONClientsKeepProblemJSON(t *testing.T) {
	cases := []struct {
		accept   string
		redirect bool
	}{
		{"application/json", false},
		{"application/problem+json", false},
		{"application/problem+json, application/json;q=0.9", false},
		{"", true},
		{"*/*", true},
		{browserAccept, true},
		{"text/html, application/json", true},
	}
	for _, tc := range cases {
		t.Run(tc.accept, func(t *testing.T) {
			h := issuerSessionServerWith(t, issuerSessionOpts{users: &fakeIssuerUsers{}, signInURL: operatorSignIn})

			rec := postSessionAccept(h, url.Values{"token": {"good"}}, "https://portal.example.com", tc.accept)

			if tc.redirect {
				if rec.Code != http.StatusSeeOther {
					t.Errorf("Accept %q: status = %d, want 303", tc.accept, rec.Code)
				}
				return
			}
			if rec.Code != http.StatusForbidden || rec.Header().Get("Content-Type") != "application/problem+json" {
				t.Errorf("Accept %q: got %d %q, want 403 application/problem+json", tc.accept, rec.Code, rec.Header().Get("Content-Type"))
			}
		})
	}
}

// TestIssuerSessionRateLimitRedirects: the per-IP limit on the handoff is a
// refusal like the others; a browser over it lands on the operator's sign-in
// with rate_limited, and keeps the 429 problem without the URL.
func TestIssuerSessionRateLimitRedirects(t *testing.T) {
	for _, signIn := range []string{"", operatorSignIn} {
		t.Run("signin="+signIn, func(t *testing.T) {
			h := issuerSessionServerWith(t, issuerSessionOpts{users: activeAcmeUser(), signInURL: signIn})
			var rec *httptest.ResponseRecorder
			for range 31 {
				rec = postSessionAccept(h, url.Values{"token": {"forged"}}, "https://portal.example.com", browserAccept)
			}
			if signIn == "" {
				if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Content-Type") != "application/problem+json" {
					t.Errorf("got %d %q, want 429 application/problem+json", rec.Code, rec.Header().Get("Content-Type"))
				}
				return
			}
			if want := signIn + "&error=rate_limited"; rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != want {
				t.Errorf("got %d to %q, want 303 to %q", rec.Code, rec.Header().Get("Location"), want)
			}
		})
	}
}

// TestIssuerSessionSuccessUnchangedWithExternalSignIn: configuring the URL
// changes nothing for an accepted handoff.
func TestIssuerSessionSuccessUnchangedWithExternalSignIn(t *testing.T) {
	audit := &fakeAuthAudit{}
	h := issuerSessionServerWith(t, issuerSessionOpts{users: activeAcmeUser(), signInURL: operatorSignIn, audit: audit})

	rec := postSessionAccept(h, url.Values{"token": {"good"}, "next": {"/dags/etl/grid"}}, "https://portal.example.com", browserAccept)

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/dags/etl/grid" {
		t.Fatalf("got %d to %q, want 303 to /dags/etl/grid", rec.Code, rec.Header().Get("Location"))
	}
	if !setsSessionCookie(rec) {
		t.Errorf("no session cookie set: %q", rec.Header().Get("Set-Cookie"))
	}
	if !audit.has(auditIssuerLoginSuccess, "success") {
		t.Errorf("success not audited: %+v", audit.events)
	}
}

// TestIssuerSessionRedirectKeepsDiagnostics: redirecting must not lose what a
// refusal records. The audit event is the same with or without the URL, and
// the access log line keeps the refusal's level, status and detail.
func TestIssuerSessionRedirectKeepsDiagnostics(t *testing.T) {
	for _, tc := range issuerRefusals() {
		t.Run(tc.name, func(t *testing.T) {
			var events [2][]authEvent
			var lines [2]map[string]any
			for i, signIn := range []string{"", operatorSignIn} {
				audit := &fakeAuthAudit{}
				var buf bytes.Buffer
				logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
				h := issuerSessionServerWith(t, issuerSessionOpts{users: tc.users(), signInURL: signIn, audit: audit, logger: logger})

				postSessionAccept(h, tc.form, tc.origin, browserAccept)

				events[i] = audit.events
				lines[i] = accessLogLine(t, buf.String())
			}
			if len(events[0]) != len(events[1]) {
				t.Fatalf("audit events differ: %+v without the URL, %+v with it", events[0], events[1])
			}
			for i := range events[0] {
				a, b := events[0][i], events[1][i]
				if a.action != b.action || a.outcome != b.outcome || a.tenant != b.tenant || a.email != b.email || a.extra["reason"] != b.extra["reason"] {
					t.Errorf("audit event %d differs: %+v without the URL, %+v with it", i, a, b)
				}
			}
			plain, redirected := lines[0], lines[1]
			if plain["level"] != redirected["level"] || plain["detail"] != redirected["detail"] {
				t.Errorf("access log differs: %v without the URL, %v with it", plain, redirected)
			}
			if got, ok := redirected["refusal_status"].(float64); !ok || int(got) != tc.status {
				t.Errorf("redirected access log refusal_status = %v, want %d", redirected["refusal_status"], tc.status)
			}
		})
	}
}

// accessLogLine returns the StructuredLogger "http request" record from a JSON
// log.
func accessLogLine(t *testing.T, log string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err == nil && rec["msg"] == "http request" {
			return rec
		}
	}
	t.Fatalf("no access log line in %q", log)
	return nil
}
