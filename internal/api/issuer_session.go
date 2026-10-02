package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
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
// The post must come from one of the operator's origins. Without that, any
// site could auto-post a valid token (its owner's own, for instance) and sign
// a visitor's browser in as someone else: login CSRF. Browsers send Origin on
// every cross-site form POST, so a missing one is refused too.
func issuerSessionHandler(d issuerSessionDeps) gin.HandlerFunc {
	return func(c *gin.Context) {
		if origin := c.GetHeader("Origin"); !slices.Contains(d.origins, origin) {
			d.logger.Warn("trusted issuer sign-in refused: origin", "origin", origin)
			d.record(c, auditIssuerLoginFailure, "", "", "", map[string]string{"reason": "origin_not_allowed"})
			AbortProblem(c, http.StatusForbidden, "forbidden", "sign-in posted from an origin that is not allowed")
			return
		}
		raw := c.PostForm("token")
		if raw == "" {
			AbortProblem(c, http.StatusBadRequest, "bad request", "form field token is required")
			return
		}
		id, err := d.issuer.Verify(c.Request.Context(), raw)
		if err != nil {
			d.logger.Warn("trusted issuer sign-in refused: token", "reason", err)
			d.record(c, auditIssuerLoginFailure, "", "", "", map[string]string{"reason": issuerReason(err)})
			AbortProblem(c, http.StatusUnauthorized, "unauthorized", "the sign-in token was not accepted")
			return
		}
		user, ok := d.resolve(c, id)
		if !ok {
			return
		}
		token, err := auth.MintUserToken(d.jwtSecret, d.tokenTTL, *user)
		if err != nil {
			d.logger.Error("trusted issuer sign-in: minting session token", "error", err)
			AbortProblem(c, http.StatusInternalServerError, "internal error", "could not open a session")
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
		AbortProblem(c, http.StatusForbidden, "forbidden", "no active account for this sign-in")
		return nil, false
	}
	switch {
	case errors.Is(err, auth.ErrUserNotFound):
		return refuse("user_not_linked")
	case err != nil:
		d.logger.Error("trusted issuer sign-in: looking up user", "error", err)
		AbortProblem(c, http.StatusInternalServerError, "internal error", "could not look up the account")
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
