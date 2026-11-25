package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/oidc"
)

// ssoLogin drives a successful SSO login and returns the callback response.
func ssoLogin(t *testing.T, f *fakeIDP, cfg config.OIDCSection, mutate func(jwt.MapClaims)) (*httptest.ResponseRecorder, http.Handler) {
	t.Helper()
	store := newFakeOIDCStore()
	store.seed(cfg.Issuer, "subject-123", &auth.User{ID: "user-1", TenantID: "default", Email: "alice@corp.example", Roles: []string{"editor"}}, true)
	srv := oidcServer(t, f, cfg, store, &fakeAuthAudit{}, nil)
	rec := driveCallback(t, srv, f, cfg, mutate)
	assertLoginSucceeded(t, rec, "callback")
	return rec, srv
}

func idTokenCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == oidcIDTokenCookie {
			return ck
		}
	}
	return nil
}

func logout(srv http.Handler, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v2/auth/logout", http.NoBody)
	for _, ck := range cookies {
		req.AddCookie(&http.Cookie{Name: ck.Name, Value: ck.Value})
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// TestSSOSignOutEndsTheIdPSession is #1165: a session that came from SSO signs
// out at the IdP too, with its ID token as the hint, so the sign-in button does
// not sign the user straight back in.
func TestSSOSignOutEndsTheIdPSession(t *testing.T) {
	f := newFakeIDP(t)
	f.endSession = true
	cfg := baseOIDCConfig(f)
	cfg.PostLogoutRedirectURL = "https://app.example/api/v2/auth/login?local=1"
	rec, srv := ssoLogin(t, f, cfg, nil)

	kept := idTokenCookie(rec)
	if kept == nil || kept.Value == "" {
		t.Fatal("the callback kept no ID token for sign-out")
	}
	if !kept.HttpOnly || !kept.Secure || kept.Path != oidcIDTokenCookiePath {
		t.Errorf("ID-token cookie = HttpOnly %v Secure %v Path %q; want HttpOnly, Secure, scoped to %s",
			kept.HttpOnly, kept.Secure, kept.Path, oidcIDTokenCookiePath)
	}

	out := logout(srv, sessionCookie(rec), kept)
	if out.Code != http.StatusFound {
		t.Fatalf("logout = %d, want 302", out.Code)
	}
	loc, err := url.Parse(out.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if got := loc.Scheme + "://" + loc.Host + loc.Path; got != f.issuer+"/logout" {
		t.Fatalf("logout went to %q, want the IdP's end_session_endpoint", out.Header().Get("Location"))
	}
	q := loc.Query()
	if q.Get("id_token_hint") != kept.Value || q.Get("client_id") != cfg.ClientID ||
		q.Get("post_logout_redirect_uri") != cfg.PostLogoutRedirectURL {
		t.Errorf("end-session query = %v", q)
	}
	for _, name := range []string{authTokenCookie, oidcIDTokenCookie} {
		if ck := namedCookie(out, name); ck == nil || ck.MaxAge >= 0 {
			t.Errorf("logout did not clear %s: %+v", name, ck)
		}
	}
}

// TestSSOSignOutWithoutAKeptIDToken covers a session the browser holds no ID
// token for (a break-glass password login): there is no IdP session of its own
// to end, so sign-out stays local.
func TestSSOSignOutWithoutAKeptIDToken(t *testing.T) {
	f := newFakeIDP(t)
	f.endSession = true
	_, srv := ssoLogin(t, f, baseOIDCConfig(f), nil)
	out := logout(srv)
	if loc := out.Header().Get("Location"); !strings.HasPrefix(loc, "/api/v2/auth/login?") {
		t.Errorf("logout = %q, want the local sign-in page", loc)
	}
}

// TestSSOSignOutStaysLocal covers auth.oidc.logout: local and an issuer with no
// end_session_endpoint (Google): no ID token is kept and sign-out is local.
func TestSSOSignOutStaysLocal(t *testing.T) {
	for name, setup := range map[string]func(*fakeIDP, *config.OIDCSection){
		"logout local": func(f *fakeIDP, c *config.OIDCSection) { f.endSession = true; c.Logout = "local" },
		"no endpoint":  func(*fakeIDP, *config.OIDCSection) {},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeIDP(t)
			cfg := baseOIDCConfig(f)
			setup(f, &cfg)
			rec, srv := ssoLogin(t, f, cfg, nil)
			if ck := idTokenCookie(rec); ck != nil {
				t.Errorf("kept an ID token while sign-out is local: %+v", ck)
			}
			out := logout(srv, sessionCookie(rec), &http.Cookie{Name: oidcIDTokenCookie, Value: "stale.id.token"})
			if loc := out.Header().Get("Location"); !strings.HasPrefix(loc, "/api/v2/auth/login?") {
				t.Errorf("logout = %q, want the local sign-in page", loc)
			}
		})
	}
}

// TestSSOSignOutWithAnOversizedIDToken: an ID token too large for a cookie is
// withheld, and sign-out still ends the IdP session, without the hint.
func TestSSOSignOutWithAnOversizedIDToken(t *testing.T) {
	f := newFakeIDP(t)
	f.endSession = true
	rec, srv := ssoLogin(t, f, baseOIDCConfig(f), func(c jwt.MapClaims) {
		c["filler"] = strings.Repeat("g", maxIDTokenCookieLen)
	})
	kept := idTokenCookie(rec)
	if kept == nil || kept.Value != idTokenWithheld {
		t.Fatalf("ID-token cookie = %+v, want the withheld marker", kept)
	}
	loc, _ := url.Parse(logout(srv, kept).Header().Get("Location"))
	if !strings.HasPrefix(loc.String(), f.issuer+"/logout") || loc.Query().Has("id_token_hint") {
		t.Errorf("logout = %q, want the IdP sign-out without a hint", loc)
	}
}

// TestExternalSignOutWinsOverTheIdP keeps #1288: the operator's own sign-out is
// in charge of the IdP when it is configured.
func TestExternalSignOutWinsOverTheIdP(t *testing.T) {
	f := newFakeIDP(t)
	f.endSession = true
	cfg := baseOIDCConfig(f)
	flow, err := oidc.NewFlow(context.Background(), cfg, testHS256Secret)
	if err != nil {
		t.Fatal(err)
	}
	h := logoutHandler(sessionCookieSpec{}, "https://portal.example.com/signout", idpLogoutFlow(flow, testOIDCSettings(cfg)))
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/v2/auth/logout", h)
	out := logout(r, &http.Cookie{Name: oidcIDTokenCookie, Value: "the.id.token"})
	if loc := out.Header().Get("Location"); loc != "https://portal.example.com/signout" {
		t.Errorf("logout = %q, want the external sign-out", loc)
	}
}
