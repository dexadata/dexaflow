package api

import (
	"context"
	"errors"
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

// fakeTrustedIssuer accepts the token "good" as the configured identity.
type fakeTrustedIssuer struct{ id issuer.Identity }

func (fakeTrustedIssuer) Provider() string { return "issuer:portal" }

func (f fakeTrustedIssuer) Verify(_ context.Context, raw string) (*issuer.Identity, error) {
	if raw != "good" {
		return nil, issuer.ErrInvalidToken
	}
	id := f.id
	return &id, nil
}

// fakeIssuerUsers resolves exactly one linked user.
type fakeIssuerUsers struct {
	user     *auth.User
	active   bool
	err      error
	provider string
	subject  string
}

func (f *fakeIssuerUsers) FindUserByOIDCSubject(_ context.Context, provider, subject string) (*auth.User, bool, error) {
	f.provider, f.subject = provider, subject
	if f.err != nil {
		return nil, false, f.err
	}
	if f.user == nil {
		return nil, false, auth.ErrUserNotFound
	}
	return f.user, f.active, nil
}

func issuerSessionServer(t *testing.T, users *fakeIssuerUsers) http.Handler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	return NewServer(Dependencies{
		Logger:               discardLogger(),
		RateLimiter:          auth.NewRateLimiter(100, time.Minute),
		JWTSecret:            "test-secret",
		TokenTTLSecs:         900,
		TrustedIssuer:        fakeTrustedIssuer{issuer.Identity{Subject: "user-42", Email: "ana@acme.com", Tenant: "acme"}},
		TrustedIssuerUsers:   users,
		TrustedIssuerOrigins: []string{"https://portal.example.com"},
	})
}

func postSession(h http.Handler, form url.Values) *httptest.ResponseRecorder {
	return postSessionFrom(h, form, "https://portal.example.com")
}

func postSessionFrom(h http.Handler, form url.Values, origin string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v2/auth/session", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func activeAcmeUser() *fakeIssuerUsers {
	return &fakeIssuerUsers{user: &auth.User{ID: "u-1", TenantID: "acme", Email: "ana@acme.com", Roles: []string{"operator"}}, active: true}
}

// TestIssuerSessionOpensTheUISessionAndLandsOnNext covers #1284's happy path:
// a token from the trusted issuer becomes the normal session cookie, minted
// for the linked user, and the browser lands on the page it asked for.
func TestIssuerSessionOpensTheUISessionAndLandsOnNext(t *testing.T) {
	users := activeAcmeUser()
	h := issuerSessionServer(t, users)

	rec := postSession(h, url.Values{"token": {"good"}, "next": {"/dags/etl/grid"}})

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/dags/etl/grid" {
		t.Fatalf("got %d to %q, want 303 to /dags/etl/grid (body %s)", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	cookie := rec.Header().Get("Set-Cookie")
	if !strings.HasPrefix(cookie, authTokenCookie+"=") || strings.HasPrefix(cookie, authTokenCookie+"=;") || !strings.Contains(cookie, "HttpOnly") {
		t.Fatalf("no session cookie set: %q", cookie)
	}
	if users.provider != "issuer:portal" || users.subject != "user-42" {
		t.Errorf("looked up (%q, %q), want the issuer's provider key and subject", users.provider, users.subject)
	}
	token := strings.TrimPrefix(strings.SplitN(cookie, ";", 2)[0], authTokenCookie+"=")
	got, err := auth.NewJWTAuthenticator(nil, "test-secret", time.Minute).Authenticate(context.Background(), token)
	if err != nil || got.ID != "u-1" || got.TenantID != "acme" {
		t.Errorf("session token authenticates as %+v (%v), want user u-1 in acme", got, err)
	}
}

// TestIssuerSessionKeepsNextOnThisOrigin locks that the handoff cannot be used
// as an open redirect.
func TestIssuerSessionKeepsNextOnThisOrigin(t *testing.T) {
	h := issuerSessionServer(t, activeAcmeUser())

	rec := postSession(h, url.Values{"token": {"good"}, "next": {"https://evil.example/x"}})

	if rec.Header().Get("Location") != "/" {
		t.Errorf("redirected to %q, want / for an off-origin next", rec.Header().Get("Location"))
	}
}

// TestIssuerSessionRejections: every refusal sets no cookie, and the status
// tells the operator's flow which side is wrong without saying why in detail.
func TestIssuerSessionRejections(t *testing.T) {
	cases := []struct {
		name  string
		form  url.Values
		users *fakeIssuerUsers
		want  int
	}{
		{"no token", url.Values{}, activeAcmeUser(), http.StatusBadRequest},
		{"invalid token", url.Values{"token": {"forged"}}, activeAcmeUser(), http.StatusUnauthorized},
		{"no linked user", url.Values{"token": {"good"}}, &fakeIssuerUsers{}, http.StatusForbidden},
		{"inactive user", url.Values{"token": {"good"}}, &fakeIssuerUsers{user: &auth.User{ID: "u-1", TenantID: "acme"}}, http.StatusForbidden},
		{"user in another tenant", url.Values{"token": {"good"}}, &fakeIssuerUsers{user: &auth.User{ID: "u-1", TenantID: "globex"}, active: true}, http.StatusForbidden},
		{"store failure", url.Values{"token": {"good"}}, &fakeIssuerUsers{err: errors.New("db down")}, http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := issuerSessionServer(t, tc.users)

			rec := postSession(h, tc.form)

			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d (body %s)", rec.Code, tc.want, rec.Body.String())
			}
			if c := rec.Header().Get("Set-Cookie"); strings.HasPrefix(c, authTokenCookie+"=") && !strings.HasPrefix(c, authTokenCookie+"=;") {
				t.Errorf("a refused handoff set a session cookie: %q", c)
			}
		})
	}
}

// TestIssuerSessionRouteExistsOnlyWhenConfigured keeps the default surface:
// without a trusted issuer the endpoint does not exist.
func TestIssuerSessionRouteExistsOnlyWhenConfigured(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewServer(Dependencies{Logger: discardLogger(), RateLimiter: auth.NewRateLimiter(100, time.Minute)})

	rec := postSession(h, url.Values{"token": {"good"}})

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d without a trusted issuer, want 404", rec.Code)
	}
}

// TestIssuerSessionOnlyFromTheOperatorsOrigin closes login CSRF: a page on
// another site that auto-posts a valid token (the attacker's own, say) would
// otherwise sign the victim's browser in as the attacker. Browsers send Origin
// on every cross-site form POST, so the handoff accepts only the operator's.
func TestIssuerSessionOnlyFromTheOperatorsOrigin(t *testing.T) {
	for _, origin := range []string{"", "https://evil.example", "null", "https://portal.example.com.evil.example"} {
		t.Run(origin, func(t *testing.T) {
			h := issuerSessionServer(t, activeAcmeUser())

			rec := postSessionFrom(h, url.Values{"token": {"good"}}, origin)

			if rec.Code != http.StatusForbidden {
				t.Errorf("Origin %q: status = %d, want 403", origin, rec.Code)
			}
			if c := rec.Header().Get("Set-Cookie"); strings.HasPrefix(c, authTokenCookie+"=") && !strings.HasPrefix(c, authTokenCookie+"=;") {
				t.Errorf("Origin %q set a session cookie", origin)
			}
		})
	}
}
