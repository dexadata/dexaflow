package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
)

func loginServer() *gin.Engine {
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Email: "admin@leoflow.local", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		TokenTTLSecs:  3600,
	})
}

func anonGet(srv *gin.Engine, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, http.NoBody)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestLoginPageIsPublicHTML(t *testing.T) {
	rec := anonGet(loginServer(), "/api/v2/auth/login") // no token
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/v2/auth/login = %d, want 200 (must be public)", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type = %q, want text/html", ct)
	}
	// The page posts the credentials; the response's Set-Cookie is the session.
	// It must NOT name the cookie itself: see
	// TestLoginPageNeverWritesTheSessionCookieFromScript.
	if body := rec.Body.String(); !strings.Contains(body, "/auth/token") {
		t.Errorf("login page should post /auth/token")
	}
}

func TestLoginPageLetsBrowserSaveCredentials(t *testing.T) {
	body := anonGet(loginServer(), "/api/v2/auth/login").Body.String()
	// Autofill on load needs the standard autocomplete tokens.
	for _, want := range []string{`autocomplete="username"`, `autocomplete="current-password"`} {
		if !strings.Contains(body, want) {
			t.Errorf("login form missing %s", want)
		}
	}
	// A fetch login does not trigger the browser "save password?" prompt by
	// itself; the Credential Management API does. Without this the user has to
	// retype the password every session.
	if !strings.Contains(body, "navigator.credentials.store") || !strings.Contains(body, "PasswordCredential") {
		t.Error("login page should store credentials via the Credential Management API so the browser can save them")
	}
}

func TestLoginPageDistinguishesRateLimit(t *testing.T) {
	body := anonGet(loginServer(), "/api/v2/auth/login").Body.String()
	// A 429 must NOT show "Invalid credentials" — that makes a rate-limited user
	// retry and dig the hole deeper. The page must tell them to wait.
	if !strings.Contains(body, "429") || !strings.Contains(body, "wait") {
		t.Errorf("login page should handle 429 with a 'wait' message, not 'Invalid credentials':\n%s", body)
	}
}

func TestLoginPageSanitizesNext(t *testing.T) {
	// Open-redirect targets collapse to "/".
	for _, bad := range []string{"//evil.com", "https://evil.com", "javascript:alert(1)"} {
		rec := anonGet(loginServer(), "/api/v2/auth/login?next="+bad)
		if strings.Contains(rec.Body.String(), "evil.com") || strings.Contains(rec.Body.String(), "alert(1)") {
			t.Errorf("next=%q leaked into the page", bad)
		}
	}
	// A safe same-origin path is preserved.
	rec := anonGet(loginServer(), "/api/v2/auth/login?next=/dags/etl/grid")
	if !strings.Contains(rec.Body.String(), "/dags/etl/grid") {
		t.Errorf("safe next path was not preserved")
	}
}

func TestTokenCookieAuthenticatesDataPlane(t *testing.T) {
	srv := viewsServer(nil, nil) // has DagRuns + /api/v2/version
	// Bearer header path still works.
	if rec := authGet(srv, http.MethodGet, "/api/v2/version", ""); rec.Code != http.StatusOK {
		t.Fatalf("bearer header = %d, want 200", rec.Code)
	}
	// _token cookie alone authenticates (no Authorization header).
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v2/version", http.NoBody)
	req.AddCookie(&http.Cookie{Name: "_token", Value: "cookie-jwt"})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("_token cookie auth = %d, want 200", rec.Code)
	}
}

func TestLogoutClearsCookieAndRedirects(t *testing.T) {
	rec := anonGet(loginServer(), "/api/v2/auth/logout")
	if rec.Code != http.StatusFound {
		t.Fatalf("logout = %d, want 302", rec.Code)
	}
	// The marker is required, not incidental. With auto_redirect on, the bare
	// sign-in URL is itself a redirect to the IdP, whose session our sign-out
	// does not touch, so landing there would sign the user straight back in.
	if loc := rec.Header().Get("Location"); loc != "/api/v2/auth/login?local=1" {
		t.Errorf("logout redirect = %q, want the sign-in PAGE rather than a URL that can redirect back into the flow", loc)
	}
	if sc := rec.Header().Get("Set-Cookie"); !strings.Contains(sc, "_token=") || !strings.Contains(sc, "Max-Age=0") {
		t.Errorf("logout should expire _token cookie, got %q", sc)
	}
}
