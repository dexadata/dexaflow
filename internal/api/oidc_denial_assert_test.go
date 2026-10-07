package api

import (
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// This file holds the two assertions every OIDC login test funnels through, and
// the tests OF those assertions.
//
// They are tested rather than trusted because a refused login and a successful
// one are now the same status code. `rec.Code == http.StatusFound` used to mean
// the login worked; it no longer does, so the difference between the two lives
// entirely inside these helpers. A helper that cannot fail would make every
// caller a test that cannot fail, silently, all at once.

// bareRedirectBody is the body net/http writes for a redirect answered to a GET:
// one anchor naming the target, and nothing else. Deriving it here, instead of
// probing the body for a handful of suspicious words, is what makes the body
// assertion a detector: ANY reason smuggled into the body, in any wording, is a
// byte that is not in this string.
func bareRedirectBody(location string) string {
	return "<a href=\"" + html.EscapeString(location) + "\">Found</a>.\n\n"
}

// denialResponseHeaders are the headers a refused login is allowed to carry.
// Everything here is written by the middleware chain or by the redirect itself
// and is independent of WHY the login failed. A header outside this set is a new
// channel out of the handler, and the point of this path is that the reason
// takes no channel to the browser at all.
var denialResponseHeaders = map[string]bool{
	"Location":                     true,
	"Content-Type":                 true,
	"Cache-Control":                true,
	"Set-Cookie":                   true,
	"Vary":                         true,
	"X-Request-Id":                 true,
	"Access-Control-Allow-Origin":  true,
	"Access-Control-Allow-Methods": true,
	"Access-Control-Allow-Headers": true,
	// Which response headers a cross-origin page may read; a fixed list,
	// independent of the request.
	"Access-Control-Expose-Headers": true,
}

// loginDenialProblems reports everything wrong with a response that is supposed
// to be a refused single sign-on. It returns findings instead of failing so the
// rules themselves can be exercised: see TestLoginDenialProblems.
//
// The shape is a redirect to the login page, not problem+json. Both OIDC routes
// are reached only by a top-level browser navigation, so JSON reached the user
// as a page of raw text with no way back to the sign-in page. What has to hold
// in every case:
//
//   - no session is minted. This is the security property; the rest is UX.
//   - the browser is sent to the login page, and to this origin. A deny that
//     could be steered off-origin would be an open redirect on the one path that
//     runs before anything is authenticated.
//   - the response carries no reason, in the target, in the body, or in a
//     header. The cause goes to the audit row and the server log; telling the
//     browser why a login was refused tells whoever is probing the deployment
//     the same thing.
func loginDenialProblems(rec *httptest.ResponseRecorder) []string {
	if rec.Code != http.StatusFound {
		return []string{fmt.Sprintf("status = %d, want a %d redirect back to the login page", rec.Code, http.StatusFound)}
	}
	var problems []string
	loc := rec.Header().Get("Location")
	if loc != loginPageWithSSOError {
		problems = append(problems, fmt.Sprintf("redirected to %q, want exactly %q: anything appended to the target is a reason the browser did not have before", loc, loginPageWithSSOError))
	}
	if !strings.HasPrefix(loc, "/") || strings.HasPrefix(loc, "//") {
		problems = append(problems, fmt.Sprintf("redirected off-origin (%q): a deny runs before anything is authenticated", loc))
	}
	if sessionCookie(rec) != nil {
		problems = append(problems, "minted a session cookie")
	}
	if body := rec.Body.String(); body != "" && body != bareRedirectBody(loc) {
		problems = append(problems, fmt.Sprintf("body = %q, want nothing beyond the bare redirect to %q", body, loc))
	}
	for name := range rec.Header() {
		if !denialResponseHeaders[http.CanonicalHeaderKey(name)] {
			problems = append(problems, fmt.Sprintf("carries header %s: %q, which is a channel the reason could leave by", name, rec.Header().Get(name)))
		}
	}
	return problems
}

// assertLoginDenied is the one place that states what a refused single sign-on
// looks like on the wire, so every fail-closed case asserts the same shape
// instead of each checking a status code and nothing else.
func assertLoginDenied(t *testing.T, rec *httptest.ResponseRecorder, what string) {
	t.Helper()
	for _, p := range loginDenialProblems(rec) {
		t.Errorf("%s: %s", what, p)
	}
}

// loginSuccessProblems is the counterpart to loginDenialProblems, and it exists
// because of it.
//
// A denied single sign-on answers 302 as well, so `rec.Code ==
// http.StatusFound` no longer means the login worked: every assertion that read
// a 302 as success would pass on a rejection. This states what success actually
// is, in the same place, so the two can never drift back together: a session
// cookie was minted, and the browser was sent somewhere other than back to the
// login page.
func loginSuccessProblems(rec *httptest.ResponseRecorder) []string {
	if rec.Code != http.StatusFound {
		return []string{fmt.Sprintf("status = %d, want a %d redirect", rec.Code, http.StatusFound)}
	}
	var problems []string
	if loc := rec.Header().Get("Location"); strings.Contains(loc, "sso_error") {
		problems = append(problems, fmt.Sprintf("was refused and sent back to the login page (%q); a 302 alone no longer distinguishes the two", loc))
	}
	if sessionCookie(rec) == nil {
		problems = append(problems, "set no session cookie, so nothing was actually signed in")
	}
	return problems
}

func assertLoginSucceeded(t *testing.T, rec *httptest.ResponseRecorder, what string) {
	t.Helper()
	for _, p := range loginSuccessProblems(rec) {
		t.Fatalf("%s: %s", what, p)
	}
}

// deniedResponse builds the response a refused login actually produces, so the
// tests below start from the real thing and mutate one property at a time.
func deniedResponse(mutate func(*httptest.ResponseRecorder)) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	rec.Code = http.StatusFound
	rec.Header().Set("Location", loginPageWithSSOError)
	rec.Header().Set("Content-Type", "text/html; charset=utf-8")
	rec.Header().Set("Cache-Control", "no-store, must-revalidate")
	rec.Header().Set("X-Request-Id", "ffb51210a101288cbd5a8e2db67784bc")
	rec.Body.WriteString(bareRedirectBody(loginPageWithSSOError))
	if mutate != nil {
		mutate(rec)
	}
	return rec
}

// TestLoginDenialProblems exercises the denial assertion itself.
//
// The first version of it probed the body for a fixed list of words ("reason",
// "tenant", "token", ...). A 302 body is the redirect anchor and nothing else,
// so that loop could only ever fire on a reason that happened to contain one of
// five English words, and the two reasons most likely to leak, "state_mismatch"
// and "email_not_verified", contain none of them. Each case below is a leak the
// helper has to catch.
func TestLoginDenialProblems(t *testing.T) {
	cases := []struct {
		name   string
		rec    *httptest.ResponseRecorder
		reason string // must be flagged; empty means the response is clean
	}{
		{
			name: "the response a refused login actually produces",
			rec:  deniedResponse(nil),
		},
		{
			name: "an empty body, which is what a redirect answered to a HEAD writes",
			rec:  deniedResponse(func(r *httptest.ResponseRecorder) { r.Body.Reset() }),
		},
		{
			name: "the reason spelled out in the body, in words the old probe list did not contain",
			rec: deniedResponse(func(r *httptest.ResponseRecorder) {
				r.Body.Reset()
				r.Body.WriteString("single sign-on failed: state_mismatch\n")
			}),
			reason: "body",
		},
		{
			name: "the reason appended to the redirect target",
			rec: deniedResponse(func(r *httptest.ResponseRecorder) {
				loc := loginPageWithSSOError + "&reason=email_not_verified"
				r.Header().Set("Location", loc)
				r.Body.Reset()
				r.Body.WriteString(bareRedirectBody(loc))
			}),
			reason: "redirected to",
		},
		{
			name: "the reason moved into a header",
			rec: deniedResponse(func(r *httptest.ResponseRecorder) {
				r.Header().Set("X-SSO-Error", "tenant_not_allowed")
			}),
			reason: "carries header",
		},
		{
			name: "the old problem+json answer, which a browser renders as raw text",
			rec: deniedResponse(func(r *httptest.ResponseRecorder) {
				r.Code = http.StatusForbidden
				r.Header().Set("Content-Type", "application/problem+json")
				r.Body.Reset()
				r.Body.WriteString(`{"title":"forbidden","detail":"single sign-on was rejected"}`)
			}),
			reason: "status = 403",
		},
		{
			name: "a session minted on the way out",
			rec: deniedResponse(func(r *httptest.ResponseRecorder) {
				http.SetCookie(r, &http.Cookie{Name: authTokenCookie, Value: "minted"})
			}),
			reason: "minted a session cookie",
		},
		{
			name: "a target steered off-origin",
			rec: deniedResponse(func(r *httptest.ResponseRecorder) {
				r.Header().Set("Location", "//evil.example/api/v2/auth/login?sso_error=1")
			}),
			reason: "off-origin",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			problems := loginDenialProblems(tc.rec)
			if tc.reason == "" {
				if len(problems) != 0 {
					t.Fatalf("a clean denial was flagged: %v", problems)
				}
				return
			}
			if len(problems) == 0 {
				t.Fatalf("the assertion did not fire, so every denial test would pass on this response")
			}
			if !strings.Contains(strings.Join(problems, "; "), tc.reason) {
				t.Errorf("fired, but on the wrong thing: %v, want something about %q", problems, tc.reason)
			}
		})
	}
}

// TestLoginDeniedAndSucceededAreMutuallyExclusive is the property that makes the
// pair safe to use: the same response must never satisfy both. Without it, a
// rename or a loosened check could let the success helper accept the very
// response the denial helper was written to recognize.
func TestLoginDeniedAndSucceededAreMutuallyExclusive(t *testing.T) {
	denied := deniedResponse(nil)
	if problems := loginSuccessProblems(denied); len(problems) == 0 {
		t.Error("the success assertion accepts a refused login, so a rejection would read as a login")
	}

	succeeded := httptest.NewRecorder()
	succeeded.Code = http.StatusFound
	succeeded.Header().Set("Location", "/dags")
	http.SetCookie(succeeded, &http.Cookie{Name: authTokenCookie, Value: "a.minted.jwt"})
	succeeded.Body.WriteString(bareRedirectBody("/dags"))
	if problems := loginSuccessProblems(succeeded); len(problems) != 0 {
		t.Fatalf("a real successful login was flagged: %v", problems)
	}
	if problems := loginDenialProblems(succeeded); len(problems) == 0 {
		t.Error("the denial assertion accepts a successful login, so a minted session would read as a rejection")
	}
}
