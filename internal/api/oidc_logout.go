package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/oidc"
)

// RP-Initiated Logout (#1165, ADR 0109).
//
// Sign-out under SSO used to clear only this server's session. The IdP session
// was untouched, so the sign-in button signed the user straight back in and the
// button appeared to do nothing. Ending the IdP session means sending the
// browser to the issuer's end_session_endpoint, and most issuers want the ID
// token of the session to end as id_token_hint: Okta refuses without it and
// Keycloak asks the user to confirm.
//
// So the browser keeps the ID token from the login, in its own cookie:
//
//   - HttpOnly and Secure like the session cookie, so a script cannot read it.
//   - Scoped to the sign-out route alone, so it is sent on exactly one request
//     and never with an API call, where it would be one more credential on the
//     wire for nothing.
//   - Living as long as the longest session a renewal can reach, so it is still
//     there when the user signs out.
//
// The server stores nothing. The ID token is an assertion addressed to this
// server (its audience is our client id), not an access token for any API, and
// the hint only names the session to end.
//
// Its presence is also what marks a session as one that came from SSO: a
// break-glass password login has none and signs out locally, since there is no
// IdP session of its own to end.

// oidcIDTokenCookie holds the login's ID token for the sign-out redirect.
const oidcIDTokenCookie = "_oidc_idt"

// oidcIDTokenCookiePath scopes the ID-token cookie to the sign-out route.
const oidcIDTokenCookiePath = "/api/v2/auth/logout"

// maxIDTokenCookieLen keeps the cookie under the 4096-byte limit browsers apply
// to a cookie with its attributes. A larger ID token (an IdP that puts a long
// group list in it) is withheld: the cookie then only marks the session as SSO,
// and sign-out still ends the IdP session, without the hint.
const maxIDTokenCookieLen = 3800

// idTokenWithheld is the cookie value that marks an SSO session whose ID token
// was too large to keep.
const idTokenWithheld = "-"

// idpLogoutFlow returns the flow sign-out sends the browser through, or nil
// when sign-out stays local: no SSO, auth.oidc.logout is local, or the issuer
// publishes no end_session_endpoint.
func idpLogoutFlow(flow *oidc.Flow, settings OIDCSettings) *oidc.Flow {
	if flow == nil || settings.Logout == oidcLogoutLocal || flow.EndSessionEndpoint() == "" {
		return nil
	}
	return flow
}

// oidcLogoutLocal is auth.oidc.logout's local-only value (config.OIDCLogoutLocal;
// internal/api does not import internal/config).
const oidcLogoutLocal = "local"

// setIDTokenCookie keeps the login's ID token for the sign-out redirect.
func setIDTokenCookie(c *gin.Context, rawIDToken string, ttl time.Duration, insecure bool, logger *slog.Logger) {
	value := rawIDToken
	if len(value) > maxIDTokenCookieLen {
		logger.Warn("oidc: ID token too large to keep for sign-out; the IdP sign-out will be sent without id_token_hint",
			"bytes", len(value), "limit", maxIDTokenCookieLen)
		value = idTokenWithheld
	}
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(oidcIDTokenCookie, value, int(ttl.Seconds()), oidcIDTokenCookiePath, "", cookieSecure(insecure), true)
}

// takeIDTokenCookie reads and clears the ID-token cookie. sso is false when
// there is none (a password login, or a login before IdP sign-out was on).
func takeIDTokenCookie(c *gin.Context, insecure bool) (hint string, sso bool) {
	ck, err := c.Request.Cookie(oidcIDTokenCookie)
	if err != nil || ck.Value == "" {
		return "", false
	}
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(oidcIDTokenCookie, "", -1, oidcIDTokenCookiePath, "", cookieSecure(insecure), true)
	if ck.Value == idTokenWithheld {
		return "", true
	}
	return ck.Value, true
}

// idTokenCookieTTL is the longest a session can live: its TTL, or the renewal
// ceiling when that is longer.
func idTokenCookieTTL(tokenTTL time.Duration, maxLifetimeSecs int) time.Duration {
	if limit := time.Duration(maxLifetimeSecs) * time.Second; limit > tokenTTL {
		return limit
	}
	return tokenTTL
}
