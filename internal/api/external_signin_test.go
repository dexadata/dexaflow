package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
)

// TestExternalSignInReceivesTheRequestedPage covers #1288: with
// auth.external_signin_url set, the sign-in route hands the user to the
// operator's sign-in with the page they asked for, so a deep link survives the
// detour. The operator's own query string is kept.
func TestExternalSignInReceivesTheRequestedPage(t *testing.T) {
	o := loginPageOpts{externalSignIn: "https://portal.example.com/engine?org=acme"}

	rec := loginPageRec(t, o, "?next=/dags/etl/grid")

	if rec.Code != http.StatusFound {
		t.Fatalf("login page = %d, want a %d redirect to the external sign-in", rec.Code, http.StatusFound)
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if loc.Scheme != "https" || loc.Host != "portal.example.com" || loc.Path != "/engine" {
		t.Errorf("redirected to %s, want the configured sign-in URL", loc)
	}
	if loc.Query().Get("org") != "acme" || loc.Query().Get("next") != "/dags/etl/grid" {
		t.Errorf("query = %v, want the operator's org=acme and next=/dags/etl/grid", loc.Query())
	}
}

// TestExternalSignInNeverForwardsAnOffOriginNext locks that the path handed to
// the operator is one sanitizeNext accepted: an attacker-built link cannot use
// the operator's sign-in to bounce users to another site.
func TestExternalSignInNeverForwardsAnOffOriginNext(t *testing.T) {
	o := loginPageOpts{externalSignIn: "https://portal.example.com/engine"}

	rec := loginPageRec(t, o, "?next="+url.QueryEscape("//evil.example/x"))

	loc, _ := url.Parse(rec.Header().Get("Location"))
	if got := loc.Query().Get("next"); got != "/" {
		t.Errorf("next = %q, want / for an off-origin target", got)
	}
}

// TestExternalSignInKeepsTheEscapeHatches keeps the local form reachable: the
// break-glass marker and a refused SSO both render the page instead of leaving
// for the operator's sign-in, for the same reasons auto-redirect yields to them.
func TestExternalSignInKeepsTheEscapeHatches(t *testing.T) {
	o := loginPageOpts{externalSignIn: "https://portal.example.com/engine"}
	for _, query := range []string{"?" + loginLocalParam + "=1", "?" + ssoErrorParam + "=" + ssoErrorRefused} {
		t.Run(query, func(t *testing.T) {
			rec := loginPageRec(t, o, query)

			if rec.Code != http.StatusOK {
				t.Errorf("%s redirected (%d) to %q, want the local sign-in page", query, rec.Code, rec.Header().Get("Location"))
			}
		})
	}
}

// TestExternalSignOutEndsOnTheOperatorsPage covers the other half of #1288:
// signing out clears the session cookie as before, then goes to the operator's
// sign-out so their session ends too.
func TestExternalSignOutEndsOnTheOperatorsPage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/v2/auth/logout", logoutHandler(false, "https://portal.example.com/signout"))
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v2/auth/logout", http.NoBody))

	if got := rec.Header().Get("Location"); got != "https://portal.example.com/signout" {
		t.Errorf("logout redirected to %q, want the external sign-out", got)
	}
	if c := rec.Header().Get("Set-Cookie"); !strings.HasPrefix(c, authTokenCookie+"=;") || !strings.Contains(c, "Max-Age=0") {
		t.Errorf("logout no longer clears the session cookie: %q", c)
	}
}

// TestSignOutWithoutExternalURLIsTodaysBehavior pins the default.
func TestSignOutWithoutExternalURLIsTodaysBehavior(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/v2/auth/logout", logoutHandler(false, ""))
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v2/auth/logout", http.NoBody))

	if got := rec.Header().Get("Location"); got != "/api/v2/auth/login?"+loginLocalParam+"=1" {
		t.Errorf("logout redirected to %q, want the local sign-in page", got)
	}
}

// TestNewServerWiresExternalAuthURLs locks the wiring from Dependencies to the
// routes, so the settings cannot be accepted and then dropped.
func TestNewServerWiresExternalAuthURLs(t *testing.T) {
	h := NewServer(Dependencies{
		Logger:             discardLogger(),
		RateLimiter:        auth.NewRateLimiter(100, time.Minute),
		ExternalSignInURL:  "https://portal.example.com/engine",
		ExternalSignOutURL: "https://portal.example.com/signout",
	})
	login := httptest.NewRecorder()
	logout := httptest.NewRecorder()

	h.ServeHTTP(login, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v2/auth/login?next=/dags", http.NoBody))
	h.ServeHTTP(logout, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v2/auth/logout", http.NoBody))

	if !strings.HasPrefix(login.Header().Get("Location"), "https://portal.example.com/engine?") {
		t.Errorf("sign-in went to %q, want the external sign-in", login.Header().Get("Location"))
	}
	if logout.Header().Get("Location") != "https://portal.example.com/signout" {
		t.Errorf("sign-out went to %q, want the external sign-out", logout.Header().Get("Location"))
	}
}
