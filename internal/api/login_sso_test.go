package api

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// loginPage renders the login page the way the router does, for a server with or
// without an OIDC flow discovered.
func loginPage(t *testing.T, sso bool) string {
	t.Helper()
	// Default to a deployment WITH break-glass accounts: that is the posture the
	// chart documents, and the one the existing assertions are about.
	return loginPageWith(t, sso, sso)
}

func loginPageWith(t *testing.T, sso, breakGlass bool) string {
	t.Helper()
	return loginPageQuery(t, sso, breakGlass, "?next=/dags")
}

func loginPageQuery(t *testing.T, sso, breakGlass bool, query string) string {
	t.Helper()
	return loginPageBody(t, loginPageOpts{sso: sso, breakGlass: breakGlass}, query)
}

// loginPageRec serves the login route the way the router does and returns the
// whole response, so a test can assert on the status and Location and not only
// on the rendered body. Auto-redirect made that necessary: its correct behavior
// is a redirect, which a body-only helper cannot see.
func loginPageRec(t *testing.T, o loginPageOpts, query string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/v2/auth/login", loginPageHandler(o))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v2/auth/login"+query, http.NoBody))
	return rec
}

func loginPageBody(t *testing.T, o loginPageOpts, query string) string {
	t.Helper()
	rec := loginPageRec(t, o, query)
	if rec.Code != http.StatusOK {
		t.Fatalf("login page = %d", rec.Code)
	}
	return rec.Body.String()
}

// TestLoginPageOffersTheSSOFlowWhenItExists is #1160.
//
// The server has had complete OIDC since ADR 0057 and the chart can now configure
// it, but the login page was a fixed username and password form with no branch
// anywhere: nothing read the configured provider, and nothing linked to
// /api/v2/auth/oidc/login, which is the route that starts the flow.
//
// So a correctly configured SSO deployment showed a password form. Users typed
// their IdP credentials into it and got "Invalid credentials", which is
// indistinguishable from a misconfiguration and sends every diagnostic instinct
// at the IdP. It also produced no audit record at all, because no OIDC request
// was ever made.
func TestLoginPageOffersTheSSOFlowWhenItExists(t *testing.T) {
	body := loginPage(t, true)

	if !strings.Contains(body, "/api/v2/auth/oidc/login") {
		t.Fatal("the login page offers no way to start the OIDC flow; a user configured for SSO has no route into it but the address bar")
	}
	// The post-login destination has to survive the detour through the IdP, or SSO
	// users always land on the root while password users keep their deep link.
	// Assert the VALUE, not just the parameter's presence. An earlier version
	// escaped next here and html/template escaped it again in the href context,
	// producing next=%252Fdags; the OIDC handler then read a literal "%2Fdags",
	// sanitizeNext refused it as not absolute, and every SSO login landed on the
	// root. "?next=" alone was true throughout that bug.
	if !strings.Contains(body, "/api/v2/auth/oidc/login?next=%2fdags") &&
		!strings.Contains(body, "/api/v2/auth/oidc/login?next=%2Fdags") {
		t.Errorf("the SSO link does not carry a usable next value; an SSO login cannot return the user to the page they asked for:\n%s", body)
	}
	if strings.Contains(body, "%252F") || strings.Contains(body, "%252f") {
		t.Error("next is double-escaped, so sanitizeNext will refuse it and the user lands on the root")
	}
	// break_glass_emails exists precisely so named local logins still work when the
	// IdP is down or the tenant pin is wrong. Hiding the form hides the escape
	// hatch at the moment it is needed.
	if !strings.Contains(body, `name="password"`) {
		t.Error("the password form is gone; break-glass accounts have no way in when the IdP is unreachable")
	}
}

// TestLoginPageHidesSSOWhenThereIsNone guards the other direction: a JWT-only
// deployment must not advertise a route its router never registered, which would
// 404 the user.
func TestLoginPageHidesSSOWhenThereIsNone(t *testing.T) {
	body := loginPage(t, false)

	if strings.Contains(body, "/api/v2/auth/oidc/login") {
		t.Error("a JWT-only deployment advertises an OIDC route that is not registered; following it 404s")
	}
	if !strings.Contains(body, `name="password"`) {
		t.Error("the password form must be there when it is the only way in")
	}
}

// TestLoginPageDoesNotPresentAFormNobodyCanUse covers the trap in the default SSO
// configuration.
//
// Under provider: oidc with break_glass_emails empty, newBreakGlass returns a
// gate that admits NOBODY: every password login is rejected, by design, because
// that is what SSO-only means. The page nonetheless rendered a full username and
// password form as its primary control, with the focus in it. A user who types
// there gets "Invalid credentials", which is the same answer a wrong password
// gets, so the page actively teaches the wrong diagnosis.
//
// The form stays reachable, because an operator adding a break-glass account
// wants it, but it is no longer presented as a way in, and it says why.
func TestLoginPageDoesNotPresentAFormNobodyCanUse(t *testing.T) {
	body := loginPageWith(t, true, false)

	if !strings.Contains(body, `name="password"`) {
		t.Fatal("the form is gone entirely; adding a break-glass account must not require a new release")
	}
	if !strings.Contains(body, "<details") {
		t.Error("the form nobody can use is still the page's primary control")
	}
	if strings.Contains(body, "autofocus") {
		t.Error("focus lands in a field where no value can succeed, and a password manager will autofill it")
	}
	if !strings.Contains(body, "break_glass_emails") {
		t.Error("the page does not name the setting that makes this form work, which is the one thing the operator reading it needs")
	}
}

// TestLoginPageKeepsTheFormWhenBreakGlassAccountsExist is the other direction:
// with an allowlist configured the form is a working escape hatch for exactly
// the moment SSO is broken, so it must not be buried.
func TestLoginPageKeepsTheFormWhenBreakGlassAccountsExist(t *testing.T) {
	body := loginPageWith(t, true, true)

	if strings.Contains(body, "<details") {
		t.Error("the break-glass form is collapsed on a deployment where it works, at the moment the IdP is down and it is the only way in")
	}
	if !strings.Contains(body, "/api/v2/auth/oidc/login") {
		t.Error("the SSO button is gone")
	}
}

// TestLoginPageFocusesTheFormWhenItIsTheOnlyWayIn keeps the JWT deployment's
// behavior: there the form is the login, so the focus belongs in it.
func TestLoginPageFocusesTheFormWhenItIsTheOnlyWayIn(t *testing.T) {
	if !strings.Contains(loginPageWith(t, false, false), "autofocus") {
		t.Error("a JWT-only login page no longer focuses its username field")
	}
}

// TestLoginPageExplainsARefusedSingleSignOn covers what the user sees when SSO
// is denied.
//
// Every fail-closed path answered problem+json with 403. That is the right
// answer to an API client and the wrong one to a browser, and both OIDC routes
// are reached only by a top-level browser navigation: the user clicks the
// sign-in control, or the IdP redirects them back. So a denied login rendered a
// page of raw JSON with no way back to the sign-in page.
//
// The redirect carries no reason. The cause is withheld from the browser on
// purpose, because it is the same information an attacker probing the deployment
// would be after; it goes to the audit row and the server log instead.
func TestLoginPageExplainsARefusedSingleSignOn(t *testing.T) {
	body := loginPageQuery(t, true, true, "?sso_error=1&next=/dags")

	if !strings.Contains(body, "Single sign-on did not complete") {
		t.Fatal("the page says nothing about the failed sign-on, so the button looks like it did nothing")
	}
	// The person reading this banner is locked out, and under provider: oidc with
	// an empty break_glass_emails that is everyone. So the remedy cannot be "read
	// the audit log": the audit UI is behind /ui/, which needs the session they do
	// not have, and the server log needs cluster access. The banner has to name
	// the human who can look, not just the place.
	if !strings.Contains(body, "administers this Dexaflow") {
		t.Error("the banner tells a locked-out user to consult records they cannot reach, and names nobody who can")
	}
	if !strings.Contains(body, "server log") {
		t.Error("the banner does not say where the reason is recorded, so the ask to an administrator is not actionable")
	}
	if !strings.Contains(body, "/api/v2/auth/oidc/login") {
		t.Error("the sign-in control is gone from the page the user was sent back to, so there is no way to retry")
	}
}

// TestLoginPageSaysNothingAboutSSOWithoutTheMarker keeps the banner off a normal
// visit: a login page that always says sign-on failed is a page that says
// nothing.
func TestLoginPageSaysNothingAboutSSOWithoutTheMarker(t *testing.T) {
	if strings.Contains(loginPage(t, true), "Single sign-on did not complete") {
		t.Error("the failure banner renders on a plain visit to the login page")
	}
	// A JWT-only deployment has no SSO at all, so the marker must mean nothing
	// there: otherwise anyone can make the page claim a sign-on failed.
	if strings.Contains(loginPageQuery(t, false, false, "?sso_error=1"), "Single sign-on did not complete") {
		t.Error("a jwt-only deployment renders an SSO failure banner for anyone who appends the parameter")
	}
}

// TestRefusedSingleSignOnLandsOnAPageThatSaysSo walks the two ends of the fix
// across the real router, because each end is otherwise tested against its own
// idea of the contract.
//
// oidc_flow_test.go asserts that a denial redirects to loginPageWithSSOError.
// The tests above assert that the login page renders a banner when it sees
// sso_error. Nothing asserted that the target of the first is served by the
// second: renaming the parameter on one side only would leave every test green
// and every refused user back on a bare form that looks like the button did
// nothing.
func TestRefusedSingleSignOnLandsOnAPageThatSaysSo(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	srv := oidcServer(t, f, cfg, newFakeOIDCStore(), &fakeAuthAudit{}, nil)

	// A callback with no state cookie is a real rejection through the real
	// fail-closed path (missing_state), not a hand-built response.
	denial := httptest.NewRecorder()
	srv.ServeHTTP(denial, httptest.NewRequestWithContext(t.Context(),
		http.MethodGet, "/api/v2/auth/oidc/callback?code=x&state=y", http.NoBody))
	assertLoginDenied(t, denial, "callback with no state cookie")

	landing := httptest.NewRecorder()
	srv.ServeHTTP(landing, httptest.NewRequestWithContext(t.Context(),
		http.MethodGet, denial.Header().Get("Location"), http.NoBody))
	if landing.Code != http.StatusOK {
		t.Fatalf("the page a refused login is sent to answered %d", landing.Code)
	}
	if !strings.Contains(landing.Body.String(), "Single sign-on did not complete") {
		t.Errorf("a refused login lands on a page that says nothing about it:\n%s", landing.Body.String())
	}
	if !strings.Contains(landing.Body.String(), "/api/v2/auth/oidc/login") {
		t.Error("the page a refused login lands on offers no way to retry")
	}
}

// TestServerSideSSOFailureIsNotShownAsARefusal covers the half of the browser
// problem the first pass left behind.
//
// Three paths on these two browser-only routes answer 500 rather than deny:
// token generation failing at the start of the flow, the state cookie failing to
// seal, and the session failing to mint. The last is the worst of them, because
// it is the tail of a completely successful round trip through the IdP: the user
// authenticated, was accepted, and then got a page of raw JSON.
//
// It needs its own marker and its own words. "We refused you" and "we broke"
// send the user to different places: the first to an administrator who has to
// change a setting, the second to a retry that may well work.
func TestServerSideSSOFailureIsNotShownAsARefusal(t *testing.T) {
	refused := loginPageQuery(t, true, true, "?sso_error=1")
	broke := loginPageQuery(t, true, true, "?sso_error=server")

	if !strings.Contains(broke, "on its side") {
		t.Fatalf("a server-side failure is described as a refusal, which sends the user to an administrator for a problem no setting fixes:\n%s", broke)
	}
	if refused == broke {
		t.Error("both markers render the same page, so the distinction exists only in the URL")
	}
	if !strings.Contains(broke, "/api/v2/auth/oidc/login") {
		t.Error("no way to retry, which is the one thing that may work for a transient failure")
	}
}

// TestServerFailureLandsOnThePageThatDescribesIt ties the two ends together, the
// way TestRefusedSingleSignOnLandsOnAPageThatSaysSo does for a refusal.
//
// The three 500 paths are hard to drive from outside: HMAC signing does not fail
// on any input the handler can produce, and neither does sealing the state
// cookie. So this drives the exit itself and then serves its target on the real
// route. Without it, renaming one side of the marker leaves every test green,
// which is exactly the gap this file already found once.
func TestServerFailureLandsOnThePageThatDescribesIt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var buf bytes.Buffer
	r := gin.New()
	r.GET("/boom", func(c *gin.Context) {
		abortSSOServerFailure(c, slog.New(slog.NewTextHandler(&buf, nil)), "minting session token", errors.New("hsm unreachable at 10.0.0.9"))
	})
	r.GET("/api/v2/auth/login", loginPageHandler(loginPageOpts{sso: true, breakGlass: true}))

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/boom", http.NoBody))

	if rec.Code != http.StatusFound {
		t.Fatalf("server failure = %d, want a redirect; problem+json renders as raw JSON on a browser-only route", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "hsm unreachable") || strings.Contains(rec.Header().Get("Location"), "hsm") {
		t.Errorf("the cause reached the browser:\n%s\n%s", rec.Header().Get("Location"), rec.Body.String())
	}
	if !strings.Contains(buf.String(), "hsm unreachable at 10.0.0.9") {
		t.Errorf("the cause reached nobody at all, which is worse than showing it:\n%s", buf.String())
	}

	landed := httptest.NewRecorder()
	r.ServeHTTP(landed, httptest.NewRequestWithContext(t.Context(), http.MethodGet, rec.Header().Get("Location"), http.NoBody))
	if !strings.Contains(landed.Body.String(), "on its side") {
		t.Errorf("the page the failure redirects to does not describe a failure:\n%s", landed.Body.String())
	}
}

// TestAutoRedirectSendsTheUserStraightToTheIdP covers the click a comparable
// tool does not ask for.
//
// Against the same pool, OpenMetadata starts the flow on the first
// unauthenticated request and the user lands inside with no visible login step.
// Dexaflow rendered its sign-in page and waited. Where an edge proxy has already
// authenticated the session, that page is a screen to acknowledge for nothing.
func TestAutoRedirectSendsTheUserStraightToTheIdP(t *testing.T) {
	rec := loginPageRec(t, loginPageOpts{sso: true, breakGlass: true, autoRedirect: true}, "?next=/dags")

	if rec.Code != http.StatusFound {
		t.Fatalf("login page = %d, want a %d redirect to the IdP", rec.Code, http.StatusFound)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/api/v2/auth/oidc/login") {
		t.Fatalf("redirected to %q, want the route that starts the flow", loc)
	}
	// The destination has to survive the detour, or auto-redirect trades a click
	// for always landing on the root.
	if !strings.Contains(loc, "next=%2Fdags") && !strings.Contains(loc, "next=%2fdags") {
		t.Errorf("the redirect dropped the post-login destination: %q", loc)
	}
}

// TestAutoRedirectStopsOnARefusedSignOn is the reason this feature is not one
// line, and it is the assertion that must exist.
//
// Since #1169 a refused sign-on answers 302 to the login page carrying
// sso_error. Auto-redirecting that page without a guard turns every denial into
// an infinite bounce between this server and the IdP, with no surface left to
// read the error on. A test that only asserts "auto-redirect redirects" passes
// on exactly that implementation.
func TestAutoRedirectStopsOnARefusedSignOn(t *testing.T) {
	rec := loginPageRec(t, loginPageOpts{sso: true, breakGlass: true, autoRedirect: true}, "?sso_error=1")

	if rec.Code == http.StatusFound {
		t.Fatalf("a refused sign-on bounced straight back to the IdP (%q): the user can never read why they were refused, and the loop has no exit", rec.Header().Get("Location"))
	}
	if !strings.Contains(rec.Body.String(), "Single sign-on did not complete") {
		t.Error("the page the denial lands on no longer explains it")
	}
}

// TestAutoRedirectYieldsToAnExplicitOptOut keeps break-glass reachable without a
// config change. With auto-redirect on, the password form is behind a redirect,
// and the account that needs it is the one used when the IdP is the thing that
// is broken: requiring an operator to edit values and roll out, to reach the
// escape hatch, would defeat the hatch.
func TestAutoRedirectYieldsToAnExplicitOptOut(t *testing.T) {
	rec := loginPageRec(t, loginPageOpts{sso: true, breakGlass: true, autoRedirect: true}, "?"+loginLocalParam+"=1")

	if rec.Code != http.StatusOK {
		t.Fatalf("the opt-out still redirected (%d); there is then no way to reach the form when the IdP is down", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `name="password"`) {
		t.Error("the opt-out did not render the password form")
	}
}

// TestAutoRedirectOffIsTodaysBehavior pins the default. Turning this on for
// everyone would remove the sign-in page from deployments that rely on it.
func TestAutoRedirectOffIsTodaysBehavior(t *testing.T) {
	rec := loginPageRec(t, loginPageOpts{sso: true, breakGlass: true}, "?next=/dags")
	if rec.Code != http.StatusOK {
		t.Fatalf("auto-redirect off = %d, want the sign-in page", rec.Code)
	}
}

// TestAutoRedirectNeedsAFlow guards the combination that would 404 every user:
// auto-redirect on with no OIDC flow discovered sends them to a route the router
// never registered.
func TestAutoRedirectNeedsAFlow(t *testing.T) {
	rec := loginPageRec(t, loginPageOpts{autoRedirect: true}, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("a jwt-only deployment redirected (%d) to a route it does not serve", rec.Code)
	}
}

// TestLogoutDoesNotBounceStraightBackIntoTheIdP covers the interaction between
// sign-out and auto-redirect, which turns a working logout into a no-op.
//
// logoutHandler clears the session and redirects to the sign-in page. With
// auto-redirect on, that page is itself a redirect to the IdP, and the IdP
// session is untouched by our sign-out, so the user is signed straight back in.
// From their side the button did nothing, and the more reliable the SSO setup
// is, the more completely it fails.
//
// Sign-out therefore has to reach the PAGE rather than the flow.
func TestLogoutDoesNotBounceStraightBackIntoTheIdP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/v2/auth/logout", logoutHandler(false))
	r.GET("/api/v2/auth/login", loginPageHandler(loginPageOpts{sso: true, breakGlass: true, autoRedirect: true}))

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v2/auth/logout", http.NoBody))
	loc := rec.Header().Get("Location")

	landed := httptest.NewRecorder()
	r.ServeHTTP(landed, httptest.NewRequestWithContext(t.Context(), http.MethodGet, loc, http.NoBody))

	if landed.Code == http.StatusFound && strings.Contains(landed.Header().Get("Location"), "/oidc/login") {
		t.Fatalf("signing out landed on %q, which redirects back to the IdP; the IdP session is still live, so the user is signed straight back in and the button appears to do nothing", loc)
	}
	if landed.Code != http.StatusOK {
		t.Fatalf("signing out reached %q, which answered %d instead of the sign-in page", loc, landed.Code)
	}
}
