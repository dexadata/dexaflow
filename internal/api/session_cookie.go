package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// One place decides the session cookie's attributes, because two places decided
// them differently and nobody noticed for a release.
//
// The OIDC callback set _token server-side, HttpOnly. The password login page
// set the same cookie from a script (document.cookie), which has two
// consequences a reader of either path alone would never see.
//
// A script cannot overwrite an HttpOnly cookie. So a password login on top of a
// live SSO session was dropped by the browser: the server minted a token and
// answered 200, the page navigated away, and the browser kept sending the old
// SSO _token. The UI went on showing the previous identity while every log said
// the login succeeded. That is the worst possible moment for it, because the
// password path under SSO is break-glass: it is used when SSO is already broken
// and the operator is already unsure what works.
//
// And a cookie a script can write is a cookie a script can read. A deployment
// that never turned SSO on never got the HttpOnly one at all, so its session
// token was readable by anything running on the page. The two paths mint the
// same session; they now protect it identically.

// sessionCookiePath scopes the session cookie to the whole origin. The UI, the
// API and the IDE all read it, so a narrower path would authenticate some of
// them and not others. It is equally the path logout must clear on: a browser
// matches a deletion by name, domain and path, so a mismatch there leaves the
// session alive behind a redirect that claims it ended.
const sessionCookiePath = "/"

// secFetchSiteHeader is the browser's own statement of where a request came
// from. A script cannot set it and a proxy has no reason to rewrite it, which
// is why the check below reads it rather than comparing Origin against Host:
// behind an ingress that rewrites Host, an Origin comparison would refuse the
// sign-in page's own login and land it back on itself with no error, which is
// the exact failure mode this file exists to remove.
const secFetchSiteHeader = "Sec-Fetch-Site"

// browserMaySetSession reports whether a credential POST may establish this
// browser's session, as opposed to only answering with a token in the body.
//
// Setting the cookie server-side is what makes the password path work at all,
// and it also turns /auth/token into a login-CSRF target it was not while it
// only returned a body. The handler binds JSON without looking at Content-Type,
// so a page on another origin can POST here with a CORS-safelisted type and no
// preflight, or with a plain form and enctype=text/plain. It cannot read the
// answer and does not need to: the Set-Cookie lands in the victim's jar and the
// victim is signed in as the attacker, entering connection credentials into an
// account somebody else owns.
//
// So the cookie is written only for a request the browser calls same-origin.
// An absent header is allowed, and that covers two callers. One is every
// non-browser client (the CLI, curl, the typed client), none of which keeps a
// cookie jar, and refusing there would break the credential contract for the
// callers that never had this risk. The other is a browser on a plain-http
// origin that is not loopback: fetch metadata is only sent to a potentially
// trustworthy URL, so exactly the deployment auth.session_cookie_insecure
// exists for sends no Sec-Fetch-Site and therefore keeps no protection here.
// That is the right trade rather than a gap worth closing with an Origin/Host
// comparison: such a deployment already hands the session token to anyone on
// the path, and behind an ingress that rewrites Host an Origin comparison would
// refuse the sign-in page's own login and land it back on itself with no error.
// same-site is refused with cross-site, because a sibling subdomain is exactly
// the origin an attacker gets to control first.
//
// The OIDC callback is deliberately not subject to this. It is a top-level
// cross-site navigation from the IdP by construction, and what binds it to a
// flow this browser actually started is the signed single-use state cookie, not
// a fetch-metadata header.
func browserMaySetSession(c *gin.Context) bool {
	switch c.GetHeader(secFetchSiteHeader) {
	case "", "same-origin", "none":
		return true
	default:
		return false
	}
}

// sessionCookieSpec is what decides the session cookie: its name
// (auth.session_cookie_name, #1405) and whether it drops Secure
// (auth.session_cookie_insecure, see cookieSecure). Every place that sets,
// clears or reads the session cookie takes the same spec, so they cannot
// disagree on the name.
type sessionCookieSpec struct {
	name     string
	insecure bool
}

// cookieName returns the session cookie's name: authTokenCookie, the name
// every earlier release used, when none is configured.
func (s sessionCookieSpec) cookieName() string {
	if s.name == "" {
		return authTokenCookie
	}
	return s.name
}

// setSessionCookie writes the session cookie server-side. Both login paths
// call it, so both get HttpOnly, SameSite=Lax and the same path and lifetime,
// and clearSessionCookie deletes exactly what it wrote. It never sets a Domain
// and always sets Path=/, which together with Secure is what a __Host- name
// requires; config validation refuses a __Host- name without Secure.
func setSessionCookie(c *gin.Context, token string, ttl time.Duration, spec sessionCookieSpec) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(spec.cookieName(), token, int(ttl.Seconds()), sessionCookiePath, "", cookieSecure(spec.insecure), true)
}

// clearSessionCookie expires the session cookie. Every attribute a browser
// matches on is setSessionCookie's, including Secure: a Set-Cookie carrying
// Secure is itself refused over plain http, so a deletion that disagreed with
// the cookie it is deleting would be silently dropped on exactly the deployment
// the disagreement came from.
func clearSessionCookie(c *gin.Context, spec sessionCookieSpec) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(spec.cookieName(), "", -1, sessionCookiePath, "", cookieSecure(spec.insecure), true)
}

// cookieSecure reports whether the auth cookies carry Secure. It is true unless
// the operator set auth.session_cookie_insecure.
//
// The server cannot derive this from the request. Behind a TLS-terminating
// ingress it sees plain http while the browser sees https, so deriving Secure
// from the request scheme would strip it from the majority deployment, which is
// the one that most needs it. Deriving it from X-Forwarded-Proto moves the
// decision to a header that is only trustworthy when trusted_proxies is
// configured, and the default is to trust none. So it is a setting, defaulting
// to the safe value, with one documented reason to change it: a deployment
// served over plain http to something that is not a loopback address, where a
// browser refuses a Secure cookie outright and login would otherwise fail with
// no visible error.
func cookieSecure(insecure bool) bool { return !insecure }
