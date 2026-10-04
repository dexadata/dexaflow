package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/issuer"
)

// Audit actions for the trusted-issuer handoff (#1284).
const (
	auditIssuerLoginSuccess = "issuer.login.success"
	auditIssuerLoginFailure = "issuer.login.failure"
)

// Stable codes a refused handoff carries to the external sign-in as `error`
// (see issuerSessionDeps.refuse). The token and user refusals reuse their
// audit reasons (issuerReason, resolve), so the operator's page, the audit
// trail and the server log name a refusal the same way.
const (
	issuerErrRateLimited      = "rate_limited"
	issuerErrOriginNotAllowed = "origin_not_allowed"
	issuerErrTokenMissing     = "token_missing"
	issuerErrServer           = "server_error"
)

// TrustedIssuer verifies handoff tokens from the operator's trusted issuer.
// issuer.Verifier implements it.
type TrustedIssuer interface {
	// Provider is the key the issuer's users are linked under.
	Provider() string
	// Verify checks a token and returns the identity it carries.
	Verify(ctx context.Context, raw string) (*issuer.Identity, error)
}

// TrustedIssuerUserStore resolves a linked user. storage implements it; it is
// the same lookup a returning SSO login uses.
type TrustedIssuerUserStore interface {
	FindUserByOIDCSubject(ctx context.Context, provider, subject string) (*auth.User, bool, error)
}

// issuerSessionDeps is what the handoff handler needs.
type issuerSessionDeps struct {
	issuer          TrustedIssuer
	users           TrustedIssuerUserStore
	origins         []string
	audit           AuthAuditWriter
	jwtSecret       string
	tokenTTL        time.Duration
	logger          *slog.Logger
	insecureCookies bool
	// signIn is auth.external_signin_url (#1288), parsed once. When set, a
	// refusal sends the browser back there instead of answering problem+json.
	// It is the only place a refusal redirects to: nothing from the request
	// can change it.
	signIn *url.URL
}

// issuerSignInTarget parses the operator's external sign-in URL for refusals.
// Config validation has already required an absolute http(s) URL; a value that
// still fails to parse disables the redirect rather than guessing a target.
func issuerSignInTarget(raw string) *url.URL {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	return u
}

// issuerSessionHandler implements POST /api/v2/auth/session (#1284): the
// operator's platform posts a short-lived token its trusted issuer signed,
// as the form field `token`, with the page to land on as `next`. A valid token
// for an existing, active user of the token's tenant opens the normal UI
// session (the same cookie a password or SSO login sets) and redirects to
// next, kept on this origin.
//
// It never creates a user and never takes roles from the token: the user must
// already be linked to the issuer, and its roles are the ones Dexaflow holds.
// Refusals set no cookie, answer with a status that says which side is wrong
// (401 token, 403 user or origin, 500 Dexaflow), and log the reason
// server-side only.
//
// With an external sign-in URL configured, a refusal instead redirects the
// browser there with the stable reason code (see refuse), so the person lands
// on the operator's page rather than on a raw error from this engine.
//
// The post must come from one of the operator's origins. Without that, any
// site could auto-post a valid token (its owner's own, for instance) and sign
// a visitor's browser in as someone else: login CSRF. Browsers send Origin on
// every cross-site form POST, so a missing one is refused too.
func issuerSessionHandler(d issuerSessionDeps) gin.HandlerFunc {
	return func(c *gin.Context) {
		if origin := c.GetHeader("Origin"); !slices.Contains(d.origins, origin) {
			d.logger.Warn("trusted issuer sign-in refused: origin", "origin", origin)
			d.record(c, auditIssuerLoginFailure, "", "", "", map[string]string{"reason": "origin_not_allowed"})
			d.refuse(c, http.StatusForbidden, issuerErrOriginNotAllowed, "forbidden", "sign-in posted from an origin that is not allowed")
			return
		}
		raw := c.PostForm("token")
		if raw == "" {
			d.refuse(c, http.StatusBadRequest, issuerErrTokenMissing, "bad request", "form field token is required")
			return
		}
		id, err := d.issuer.Verify(c.Request.Context(), raw)
		if err != nil {
			d.logger.Warn("trusted issuer sign-in refused: token", "reason", err)
			reason := issuerReason(err)
			d.record(c, auditIssuerLoginFailure, "", "", "", map[string]string{"reason": reason})
			d.refuse(c, http.StatusUnauthorized, reason, "unauthorized", "the sign-in token was not accepted")
			return
		}
		user, ok := d.resolve(c, id)
		if !ok {
			return
		}
		token, err := auth.MintUserToken(d.jwtSecret, d.tokenTTL, *user)
		if err != nil {
			d.logger.Error("trusted issuer sign-in: minting session token", "error", err)
			d.refuse(c, http.StatusInternalServerError, issuerErrServer, "internal error", "could not open a session")
			return
		}
		setSessionCookie(c, token, d.tokenTTL, d.insecureCookies)
		d.record(c, auditIssuerLoginSuccess, user.TenantID, user.ID, user.Email, nil)
		c.Redirect(http.StatusSeeOther, sanitizeNext(c.PostForm("next")))
	}
}

// resolve finds the active user the identity is linked to, in the identity's
// tenant. On refusal it has already answered the request.
func (d issuerSessionDeps) resolve(c *gin.Context, id *issuer.Identity) (*auth.User, bool) {
	user, active, err := d.users.FindUserByOIDCSubject(c.Request.Context(), d.issuer.Provider(), id.Subject)
	refuse := func(reason string) (*auth.User, bool) {
		d.logger.Warn("trusted issuer sign-in refused: user", "reason", reason, "tenant", id.Tenant, "email", id.Email)
		d.record(c, auditIssuerLoginFailure, id.Tenant, "", id.Email, map[string]string{"reason": reason})
		d.refuse(c, http.StatusForbidden, reason, "forbidden", "no active account for this sign-in")
		return nil, false
	}
	switch {
	case errors.Is(err, auth.ErrUserNotFound):
		return refuse("user_not_linked")
	case err != nil:
		d.logger.Error("trusted issuer sign-in: looking up user", "error", err)
		d.refuse(c, http.StatusInternalServerError, issuerErrServer, "internal error", "could not look up the account")
		return nil, false
	case !active:
		return refuse("user_inactive")
	case user.TenantID != id.Tenant:
		// The subject is linked in another tenant than the token names. Opening a
		// session in either would let the issuer's claim and Dexaflow's record
		// disagree about where this person works, so neither wins.
		return refuse("tenant_mismatch")
	}
	return user, true
}

// refuse answers a refused handoff and stops the chain. Callers have already
// logged and audited the refusal; refuse only chooses the response.
//
// Without an external sign-in URL it is the problem+json it always was. With
// one, the browser is sent there with 303 See Other and `error=<code>`
// appended to the URL's own query. The code is one of a fixed set of stable,
// non-secret strings; nothing else from the refusal (token, subject, email,
// tenant, origin) goes into the URL. Every refusal redirects, including 429
// and 500: the route is a top-level form navigation, so the alternative is a
// raw JSON page the person cannot act on, the code carries no detail of the
// failure, and the target is fixed by the operator, so no answer here can be
// steered by the request.
//
// A caller that asks for JSON and not HTML (Accept naming application/json or
// application/problem+json without text/html) is a script or a test rather
// than a browser navigation, and keeps the problem it can parse. Browsers send
// text/html on a form navigation, and a client that sends no Accept or */*
// gets the redirect, which still carries the code.
//
// The access log keeps the refusal's status as refusal_status and logs at its
// level, so a redirected refusal is as visible there as a 4xx or 5xx.
func (d issuerSessionDeps) refuse(c *gin.Context, status int, code, title, detail string) {
	if d.signIn == nil || wantsProblemJSON(c.GetHeader("Accept")) {
		AbortProblem(c, status, title, detail)
		return
	}
	c.Set(contextKeyProblemDetail, detail)
	c.Set(contextKeyRefusalStatus, status)
	c.Redirect(http.StatusSeeOther, withError(d.signIn, code))
	c.Abort()
}

// refuseRateLimited is the handoff's answer to a caller over its per-IP limit,
// the same refusal rateLimitByIP gives, routed through refuse.
func (d issuerSessionDeps) refuseRateLimited(c *gin.Context) {
	d.refuse(c, http.StatusTooManyRequests, issuerErrRateLimited, "rate limited", rateLimitedDetail)
}

// withError returns target with `error=<code>` appended to its query. The
// operator's query is kept byte for byte (not re-encoded or reordered), and a
// fragment stays after the query.
func withError(target *url.URL, code string) string {
	u := *target
	param := "error=" + url.QueryEscape(code)
	// Keep the configured query byte for byte, minus any error parameter of its
	// own, so the page never sees two and reads the stale one.
	kept := make([]string, 0, 4)
	for _, p := range strings.Split(u.RawQuery, "&") {
		if p == "" || p == "error" || strings.HasPrefix(p, "error=") {
			continue
		}
		kept = append(kept, p)
	}
	u.RawQuery = strings.Join(append(kept, param), "&")
	u.ForceQuery = false
	return u.String()
}

// wantsProblemJSON reports whether an Accept header asks for JSON and not
// HTML. Quality values are not weighed: a browser navigation always names
// text/html, and that alone is what keeps the redirect.
func wantsProblemJSON(accept string) bool {
	a := strings.ToLower(accept)
	if strings.Contains(a, "text/html") {
		return false
	}
	return strings.Contains(a, "application/json") || strings.Contains(a, "application/problem+json")
}

func (d issuerSessionDeps) record(c *gin.Context, action, tenant, userID, email string, extra map[string]string) {
	if d.audit == nil {
		return
	}
	outcome := "success"
	if action == auditIssuerLoginFailure {
		outcome = "failure"
	}
	if err := d.audit.RecordAuthEvent(c.Request.Context(), tenant, userID, action, email, outcome, extra); err != nil {
		d.logger.Warn("auth audit write failed", "action", action, "error", err)
	}
}

// issuerReason is a stable, non-secret audit reason for a verification error.
func issuerReason(err error) string {
	switch {
	case errors.Is(err, issuer.ErrLifetime):
		return "token_lifetime"
	case errors.Is(err, issuer.ErrTenantNotAllowed):
		return "tenant_not_allowed"
	case errors.Is(err, issuer.ErrReplayed):
		return "token_replayed"
	default:
		return "invalid_token"
	}
}
