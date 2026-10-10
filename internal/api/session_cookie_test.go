package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
)

// What these tests can and cannot prove.
//
// They can prove the half of the defect that lives in a response header: that
// POST /auth/token sets the session cookie itself, HttpOnly, with the same
// attributes the SSO callback uses, and that logout clears exactly that cookie.
// That is the unreported half, where a JWT-only deployment ran with a session
// token any script on the page could read.
//
// They cannot prove the reported half. "A JS write is ignored because an
// HttpOnly cookie already exists" is a browser rule, not a header, and a
// handler test that asserted it would only be asserting its own fake. That one
// is in test/e2e/sso-login-page.{sh,js}, which drives a real Chromium through a
// real SSO login and then a break-glass password login without signing out.

// cookieLoginServer serves the password path with credAuthn (password "right").
func cookieLoginServer(insecure bool) *gin.Engine {
	return NewServer(Dependencies{
		Logger:                discardLogger(),
		Authenticator:         credAuthn{},
		RateLimiter:           auth.NewRateLimiter(50, time.Minute),
		CORSOrigins:           []string{"*"},
		TokenTTLSecs:          3600,
		SessionCookieInsecure: insecure,
	})
}

const goodCreds = `{"username":"admin@leoflow.local","password":"right"}`

// namedCookie returns the Set-Cookie the response wrote for name, or nil.
func namedCookie(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == name {
			return ck
		}
	}
	return nil
}

func TestPasswordLoginSetsTheSessionCookieServerSide(t *testing.T) {
	rec := do(cookieLoginServer(false), http.MethodPost, "/auth/token", goodCreds)
	if rec.Code != http.StatusOK {
		t.Fatalf("login = %d, want 200", rec.Code)
	}
	ck := namedCookie(rec, authTokenCookie)
	if ck == nil {
		t.Fatal("POST /auth/token set no session cookie: the browser session then depends on a script write, " +
			"which cannot replace a live HttpOnly session and leaves the token readable by any script on the page")
	}
	if ck.Value == "" {
		t.Fatal("session cookie is empty")
	}
	if !ck.HttpOnly {
		t.Error("session cookie is not HttpOnly: a script on the page can read the session token")
	}
	if !ck.Secure {
		t.Error("session cookie is not Secure by default")
	}
	if ck.SameSite != http.SameSiteLaxMode {
		t.Errorf("session cookie SameSite = %v, want Lax", ck.SameSite)
	}
	if ck.Path != sessionCookiePath {
		t.Errorf("session cookie path = %q, want %q (the UI, the API and the IDE all read it)", ck.Path, sessionCookiePath)
	}
	if ck.MaxAge != 3600 {
		t.Errorf("session cookie Max-Age = %d, want the token TTL (3600)", ck.MaxAge)
	}
}

// The body is a separate contract from the cookie: the CLI, the SPA and the e2e
// scripts all read access_token out of it, and setting a cookie must not have
// quietly stopped that.
func TestPasswordLoginStillReturnsTheTokenInTheBody(t *testing.T) {
	rec := do(cookieLoginServer(false), http.MethodPost, "/auth/token", goodCreds)
	var body tokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding the login response: %v (%s)", err, rec.Body.String())
	}
	if body.AccessToken == "" {
		t.Error("login response carries no access_token; every API client reads it from the body")
	}
	if body.TokenType != "bearer" || body.ExpiresIn != 3600 {
		t.Errorf("login response = %+v, want token_type bearer and expires_in 3600", body)
	}
	if ck := namedCookie(rec, authTokenCookie); ck != nil && ck.Value != body.AccessToken {
		t.Error("the cookie and the body carry different tokens")
	}
}

// A refused login must not touch the session that is already in the browser.
// Writing the cookie before the credential check, or on the rate-limited path,
// would let a wrong password sign the current user out.
func TestRefusedPasswordLoginSetsNoSessionCookie(t *testing.T) {
	bad := `{"username":"admin@leoflow.local","password":"wrong"}`
	t.Run("bad credentials", func(t *testing.T) {
		rec := do(cookieLoginServer(false), http.MethodPost, "/auth/token", bad)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("bad password = %d, want 401", rec.Code)
		}
		if ck := namedCookie(rec, authTokenCookie); ck != nil {
			t.Errorf("a refused login wrote the session cookie (%q)", ck.String())
		}
	})
	t.Run("rate limited", func(t *testing.T) {
		srv := NewServer(Dependencies{
			Logger: discardLogger(), Authenticator: credAuthn{},
			RateLimiter: auth.NewRateLimiter(1, time.Minute),
			CORSOrigins: []string{"*"}, TokenTTLSecs: 3600,
		})
		do(srv, http.MethodPost, "/auth/token", bad) // burn the budget
		rec := do(srv, http.MethodPost, "/auth/token", goodCreds)
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("locked out login = %d, want 429", rec.Code)
		}
		if ck := namedCookie(rec, authTokenCookie); ck != nil {
			t.Errorf("a rate-limited login wrote the session cookie (%q)", ck.String())
		}
	})
}

// The login page must not try to set the cookie itself. A script cannot
// overwrite the HttpOnly cookie the server now sends, so a leftover
// document.cookie write would be dead code on a fresh login and an active
// hazard on top of a live session.
func TestLoginPageNeverWritesTheSessionCookieFromScript(t *testing.T) {
	body := anonGet(loginServer(), "/api/v2/auth/login").Body.String()
	if strings.Contains(body, "document.cookie") {
		t.Error("the login page writes document.cookie: a script cannot replace an HttpOnly session cookie, " +
			"so a password login over a live SSO session would be silently dropped")
	}
	if strings.Contains(body, authTokenCookie+"=") {
		t.Errorf("the login page still names the %s cookie; the response's Set-Cookie is the login now", authTokenCookie)
	}
	// It still has to post the credentials somewhere.
	if !strings.Contains(body, "/auth/token") {
		t.Error("the login page no longer posts to /auth/token")
	}
}

// Logout has to clear the cookie login sets, not a cookie that looks like it.
// A browser matches a deletion on name, domain and path, and refuses a Secure
// Set-Cookie over plain http, so any disagreement leaves the session alive
// behind a redirect that says it ended.
func TestLogoutClearsExactlyTheCookieLoginSets(t *testing.T) {
	for _, insecure := range []bool{false, true} {
		srv := cookieLoginServer(insecure)
		set := namedCookie(do(srv, http.MethodPost, "/auth/token", goodCreds), authTokenCookie)
		if set == nil {
			t.Fatal("login set no session cookie")
		}
		cleared := namedCookie(do(srv, http.MethodGet, "/api/v2/auth/logout", ""), authTokenCookie)
		if cleared == nil {
			t.Fatal("logout cleared no session cookie")
		}
		if cleared.Value != "" || cleared.MaxAge >= 0 {
			t.Errorf("logout cookie = %q, want an empty value and a negative Max-Age", cleared.String())
		}
		if cleared.Path != set.Path || cleared.Domain != set.Domain ||
			cleared.Secure != set.Secure || cleared.SameSite != set.SameSite {
			t.Errorf("insecure=%v: logout clears %q but login sets %q; the browser will not match them",
				insecure, cleared.String(), set.String())
		}
	}
}

// Both login paths mint the same session, so they must protect it the same way.
// This is the assertion that would have caught the defect: the two cookies were
// the same name with different postures, decided by which button the user
// pressed.
func TestBothLoginPathsSetTheSameSessionCookie(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	store := newFakeOIDCStore()
	store.seed(cfg.Issuer, "subject-123", &auth.User{ID: "user-1", TenantID: "default", Email: "alice@corp.example", Roles: []string{"editor"}}, true)
	// credAuthn serves the password path on the same server the SSO flow runs on.
	srv := oidcServer(t, f, cfg, store, &fakeAuthAudit{}, credAuthn{})

	ssoRec := driveCallback(t, srv, f, cfg, nil)
	assertLoginSucceeded(t, ssoRec, "sso login")
	sso := sessionCookie(ssoRec)
	if sso == nil {
		t.Fatal("the SSO callback set no session cookie")
	}
	pw := namedCookie(do(srv, http.MethodPost, "/auth/token", goodCreds), authTokenCookie)
	if pw == nil {
		t.Fatal("the password path set no session cookie")
	}
	if pw.HttpOnly != sso.HttpOnly || pw.Secure != sso.Secure || pw.SameSite != sso.SameSite ||
		pw.Path != sso.Path || pw.Domain != sso.Domain || pw.MaxAge != sso.MaxAge {
		t.Errorf("the two login paths protect the same session differently:\n  sso:      %s\n  password: %s",
			sso.String(), pw.String())
	}
}

// The escape hatch drops Secure and nothing else. It exists for a plain-http
// deployment that is not on loopback, where a browser refuses a Secure cookie
// outright and login would fail with no visible error; it must not quietly also
// hand the token to scripts.
func TestSessionCookieInsecureDropsSecureAndNothingElse(t *testing.T) {
	hardened := namedCookie(do(cookieLoginServer(false), http.MethodPost, "/auth/token", goodCreds), authTokenCookie)
	relaxed := namedCookie(do(cookieLoginServer(true), http.MethodPost, "/auth/token", goodCreds), authTokenCookie)
	if hardened == nil || relaxed == nil {
		t.Fatal("login set no session cookie")
	}
	if relaxed.Secure {
		t.Error("auth.session_cookie_insecure did not drop Secure, so a plain-http deployment still cannot log in")
	}
	if !relaxed.HttpOnly {
		t.Error("auth.session_cookie_insecure also dropped HttpOnly: it is about the transport, not about scripts")
	}
	if relaxed.SameSite != hardened.SameSite || relaxed.Path != hardened.Path || relaxed.MaxAge != hardened.MaxAge {
		t.Errorf("auth.session_cookie_insecure changed more than Secure:\n  default: %s\n  relaxed: %s",
			hardened.String(), relaxed.String())
	}
}

// The OIDC state cookie seals the nonce and the PKCE verifier, so it answers to
// the same posture as the session cookie, and the single-use clear in the
// callback has to match the write in the login redirect or the state can be
// replayed.
func TestStateCookieIsHardenedAndClearedOnTheSamePath(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	store := newFakeOIDCStore()
	store.seed(cfg.Issuer, "subject-123", &auth.User{ID: "user-1", TenantID: "default", Email: "alice@corp.example", Roles: []string{"editor"}}, true)
	srv := oidcServer(t, f, cfg, store, &fakeAuthAudit{}, nil)

	st := startLogin(t, srv)
	if !st.cookie.Secure || !st.cookie.HttpOnly || st.cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("state cookie = %q, want Secure, HttpOnly and SameSite=Lax", st.cookie.String())
	}
	if st.cookie.Path != oidcStateCookiePath {
		t.Errorf("state cookie path = %q, want %q", st.cookie.Path, oidcStateCookiePath)
	}
	rec := driveCallback(t, srv, f, cfg, nil)
	cleared := namedCookie(rec, oidcStateCookie)
	if cleared == nil {
		t.Fatal("the callback did not clear the state cookie; it is single-use")
	}
	if cleared.Path != st.cookie.Path || cleared.Secure != st.cookie.Secure || cleared.MaxAge >= 0 {
		t.Errorf("the callback clears %q but the login sets %q; the browser will not match them",
			cleared.String(), st.cookie.String())
	}
}

// doFrom is do() with the browser's own statement of where the request came
// from. A non-browser client sends no Sec-Fetch-Site at all, which is site "".
func doFrom(srv *gin.Engine, site, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/token", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if site != "" {
		req.Header.Set("Sec-Fetch-Site", site)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// Making /auth/token a cookie-setting endpoint made it a login-CSRF target, a
// hazard it did not have while it only returned a body. A page on another
// origin can POST here without a preflight (the handler binds JSON regardless
// of Content-Type, so text/plain, a CORS-safelisted type, is enough, and a form
// with enctype=text/plain needs no fetch at all). It cannot read the answer, and
// it does not need to: the Set-Cookie lands in the victim's jar and the victim
// is now signed in as the attacker, typing connection credentials into an
// account somebody else owns.
//
// The OIDC callback is the same kind of cross-site arrival and is NOT covered by
// this: its signed single-use state cookie is what binds it to a flow this
// browser started, and it has to keep working, since it is a top-level
// navigation from the IdP.
func TestCrossSiteLoginDoesNotPlantASessionCookie(t *testing.T) {
	for _, site := range []string{"cross-site", "same-site"} {
		t.Run(site, func(t *testing.T) {
			rec := doFrom(cookieLoginServer(false), site, goodCreds)
			if rec.Code != http.StatusOK {
				t.Fatalf("login = %d, want 200: the credential contract is unchanged for API clients", rec.Code)
			}
			var body tokenResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.AccessToken == "" {
				t.Fatalf("the body no longer carries access_token (%v, %s)", err, rec.Body.String())
			}
			if ck := namedCookie(rec, authTokenCookie); ck != nil {
				t.Errorf("a %s login planted the session cookie (%q): a page on another origin can sign this "+
					"browser in as whoever it has credentials for", site, ck.String())
			}
		})
	}
}

// The other side of the same guard: the sign-in page's own fetch, and every
// non-browser client, must still be signed in by the response. The empty case
// is load-bearing twice over. It is the CLI, and it is also a browser on a
// plain-http origin that is not loopback, which sends no fetch metadata at all
// (the headers go only to potentially trustworthy URLs). Refusing there would
// break the sign-in page on exactly the deployment
// auth.session_cookie_insecure exists for, in exactly the silent way this file
// exists to remove.
func TestSameOriginAndNonBrowserLoginsStillSetTheCookie(t *testing.T) {
	for _, site := range []string{"", "same-origin", "none"} {
		name := site
		if name == "" {
			name = "no Sec-Fetch-Site (cli)"
		}
		t.Run(name, func(t *testing.T) {
			rec := doFrom(cookieLoginServer(false), site, goodCreds)
			if rec.Code != http.StatusOK {
				t.Fatalf("login = %d, want 200", rec.Code)
			}
			ck := namedCookie(rec, authTokenCookie)
			if ck == nil {
				t.Fatal("the login set no session cookie, so the sign-in page lands back on itself with no error")
			}
			if !ck.HttpOnly || !ck.Secure {
				t.Errorf("session cookie = %q, want HttpOnly and Secure", ck.String())
			}
		})
	}
}
