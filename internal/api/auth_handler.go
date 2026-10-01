package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
)

// TokenRenewer re-mints a still-valid user bearer with a fresh short TTL, bounded
// by max_lifetime since first login. *auth.JWTAuthenticator implements it via
// RenewUserToken; the handler depends on this narrow interface so the renew route
// can be tested without a real signing key. It takes a context because renewal
// re-proves the principal against the user store, so it must be canceled with
// the request.
type TokenRenewer interface {
	RenewUserToken(ctx context.Context, token string, ttl, maxLifetime time.Duration) (renewed string, ok bool, err error)
}

// breakGlass gates the credential path when the provider is OIDC (D8): only the
// designated local admin email(s) may log in with a password; every other
// password login is rejected, and each attempt is audited (H5). It is nil in
// JWT mode, where the credential path is ungated and unchanged.
type breakGlass struct {
	emails map[string]bool
	audit  AuthAuditWriter
}

// newBreakGlass builds the gate from the allowlist. In JWT mode (oidcEnabled
// false) the credential path is the primary auth, so an empty allowlist returns
// nil (ungated) — break-glass is an OIDC concept. Under OIDC it ALWAYS gates: an
// empty allowlist yields a gate that admits no one, i.e. SSO-only (every password
// login rejected). Returning nil for OIDC + empty was the bypass — POST
// /auth/token stayed fully open for every password user (e.g. the seeded admin).
func newBreakGlass(emails []string, audit AuthAuditWriter, oidcEnabled bool) *breakGlass {
	if len(emails) == 0 && !oidcEnabled {
		return nil
	}
	set := make(map[string]bool, len(emails))
	for _, e := range emails {
		set[strings.ToLower(strings.TrimSpace(e))] = true
	}
	return &breakGlass{emails: set, audit: audit}
}

// allows reports whether the email may use the credential path.
func (b *breakGlass) allows(email string) bool {
	return b.emails[strings.ToLower(strings.TrimSpace(email))]
}

// rateLimitByIP is a gin middleware that caps requests per client IP against its
// own limiter. It backs the OIDC login/callback endpoints (which otherwise had no
// rate limit), on a limiter separate from the /auth/token one so OIDC traffic and
// password-login lockouts never contaminate each other's budget.
func rateLimitByIP(limiter *auth.RateLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		ip := c.ClientIP()
		if limiter.Blocked(ip) {
			AbortProblem(c, http.StatusTooManyRequests, "rate limited",
				"too many requests; wait about a minute and try again")
			return
		}
		limiter.Allow(ip)
		c.Next()
	}
}

type tokenRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
	Tenant   string `json:"tenant"`
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

// authTokenHandler issues a JWT for valid credentials, rate-limited per client IP.
// When bg is non-nil (OIDC mode, D8) only the break-glass allowlist may use the
// credential path; every other password login is rejected and audited, so
// enabling SSO does not silently leave a full password bypass open.
//
// On success it does two things with the token, for two different callers. It
// returns it in the body, which is the contract the CLI, the SPA and every API
// client read. And, for a request the browser reports as same-origin, it sets
// it as that browser's session cookie, server-side, with the attributes
// setSessionCookie describes: the login page used to write that cookie itself
// from JavaScript, which could neither replace an existing HttpOnly session nor
// keep a script from reading the new one.
//
// The cookie on a CLI response is inert: nothing there keeps a cookie jar, and
// the body is unchanged. A cross-origin caller gets the body and no cookie at
// all; see browserMaySetSession for why setting one there would be a login-CSRF
// hole that this endpoint did not have while it answered with a body only.
func authTokenHandler(authn auth.Authenticator, limiter *auth.RateLimiter, ttlSeconds int, bg *breakGlass, insecureCookies bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Peek the limiter up front but DON'T count this attempt yet: only failed
		// logins consume the budget (recorded below). This is what keeps a user who
		// mistypes a couple times — or whose SPA re-fetches a token — from getting
		// locked out the instant they finally send the right password.
		if limiter.Blocked(c.ClientIP()) {
			AbortProblem(c, http.StatusTooManyRequests, "rate limited",
				"too many failed login attempts; wait about a minute and try again")
			return
		}
		var req tokenRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", err.Error())
			return
		}
		tenant := strings.TrimSpace(req.Tenant)
		if tenant == "" {
			tenant = "default"
		}
		username := strings.TrimSpace(req.Username)
		// Break-glass gate (D8): under OIDC, reject any password login that is not
		// on the designated local-admin allowlist, before touching the credential
		// store. Audited either way.
		if bg != nil && !bg.allows(username) {
			recordBreakGlass(c, bg, tenant, username, "denied")
			limiter.Allow(c.ClientIP())
			AbortProblem(c, http.StatusUnauthorized, "unauthorized", "invalid credentials")
			return
		}
		// Trim the username/tenant (emails carry no meaningful surrounding space and
		// autofill/paste often add one), but NEVER the password — trailing/leading
		// spaces are valid password characters, and silently trimming them is unsafe.
		token, err := authn.IssueToken(c.Request.Context(), auth.Credentials{
			Tenant:   tenant,
			Username: username,
			Password: req.Password,
		})
		if err != nil {
			if errors.Is(err, auth.ErrInvalidCredentials) {
				limiter.Allow(c.ClientIP()) // count the failure toward the lockout budget
				if bg != nil {
					recordBreakGlass(c, bg, tenant, username, "bad_credentials")
				}
				AbortProblem(c, http.StatusUnauthorized, "unauthorized", "invalid credentials")
				return
			}
			// A non-credential error means the backend (DB) could not be reached, not
			// that the credentials are wrong. Answer 503 so operators/monitoring see an
			// outage rather than a wall of "invalid credentials" (#843). It leaks
			// nothing: it is returned regardless of whether the account exists.
			AbortProblem(c, http.StatusServiceUnavailable, "service unavailable", "authentication temporarily unavailable")
			return
		}
		if bg != nil {
			recordBreakGlass(c, bg, tenant, username, "success")
		}
		// Only a login that actually issued a token writes the cookie: every
		// rejection above returns before this point, so a refused attempt can never
		// disturb a session that is already there. And only a request the browser
		// calls same-origin writes it, so a page on another origin cannot sign this
		// browser in as an account it holds credentials for; see
		// browserMaySetSession. The body is unchanged either way.
		if browserMaySetSession(c) {
			setSessionCookie(c, token, time.Duration(ttlSeconds)*time.Second, insecureCookies)
		}
		c.JSON(http.StatusOK, tokenResponse{AccessToken: token, TokenType: "bearer", ExpiresIn: ttlSeconds})
	}
}

// renewTokenHandler re-mints the caller's still-valid user bearer into a fresh
// access token, so a long CLI/dev session never has to `leoflow auth login` again
// on the hour (EKS validation aresta #5). It is the server half of transparent
// renewal: the short access-token TTL is unchanged (a stolen token still lapses
// quickly), while max_lifetime bounds how long a session may keep renewing before
// the user must re-authenticate.
//
// The route sits under the public /api/v2/auth/ prefix (like login/logout), so the
// handler is self-gating: it re-mints ONLY from a cryptographically valid,
// unexpired, correct-audience bearer whose user is still active in the store (the
// renewer enforces all of that), and answers 401 when the token is
// invalid/expired, when its user has been deactivated or deleted, and when it is
// past max_lifetime, so the CLI falls back to login in every case. You cannot
// renew without already holding a valid token for a live account. The response
// mirrors /auth/token so the same client decoder handles both.
func renewTokenHandler(renewer TokenRenewer, ttlSeconds, maxLifetimeSeconds int) gin.HandlerFunc {
	ttl := time.Duration(ttlSeconds) * time.Second
	maxLifetime := time.Duration(maxLifetimeSeconds) * time.Second
	return func(c *gin.Context) {
		token := bearerToken(c.GetHeader("Authorization"))
		if token == "" {
			AbortProblem(c, http.StatusUnauthorized, "unauthorized", "missing bearer token")
			return
		}
		renewed, ok, err := renewer.RenewUserToken(c.Request.Context(), token, ttl, maxLifetime)
		if err != nil {
			// Renewal re-proves the principal against the user store, so it fails for
			// two unrelated reasons: the token was judged and rejected, or the store
			// could not be reached to judge it. Only the first is a statement about
			// the caller. Answering 401 for the second sends the client to log in
			// against the database that is already down and reads to whoever is on
			// call as an auth incident — the same conflation JWTAuth carried (#1087).
			// Either way the renewal is refused; the cause goes to the log, never to
			// the body.
			if !errors.Is(err, auth.ErrInvalidToken) {
				AbortProblemCause(c, http.StatusServiceUnavailable, "service unavailable",
					"authentication temporarily unavailable", err)
				return
			}
			AbortProblemCause(c, http.StatusUnauthorized, "unauthorized", "token cannot be renewed; log in again", err)
			return
		}
		if !ok {
			AbortProblem(c, http.StatusUnauthorized, "unauthorized", "session lifetime exceeded; log in again")
			return
		}
		c.JSON(http.StatusOK, tokenResponse{AccessToken: renewed, TokenType: "bearer", ExpiresIn: ttlSeconds})
	}
}

// recordBreakGlass audits a break-glass credential-path attempt (H5),
// best-effort so a flaky audit sink never changes the login outcome.
func recordBreakGlass(c *gin.Context, bg *breakGlass, tenant, email, outcome string) {
	if bg == nil || bg.audit == nil {
		return
	}
	//nolint:errcheck // best-effort audit; a sink error must never fail the login
	_ = bg.audit.RecordAuthEvent(c.Request.Context(), tenant, "", auditBreakGlass, email, outcome, nil)
}
