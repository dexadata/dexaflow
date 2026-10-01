package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/oidc"
)

// oidcStateCookie carries the signed state/nonce/PKCE-verifier between the login
// redirect and the callback. Scoped to the auth path and short-lived.
const oidcStateCookie = "_oidc_state"

// oidcStateCookiePath scopes the state cookie to the auth routes that use it.
// Both the write and the single-use clear name it, and a browser matches a
// deletion on it, so it is a constant rather than two literals.
const oidcStateCookiePath = "/api/v2/auth/"

// Auth audit actions (H5).
const (
	auditOIDCLoginSuccess = "oidc.login.success"
	auditOIDCLoginFailure = "oidc.login.failure"
	auditOIDCTenantReject = "oidc.tenant_pin_rejected"
	auditOIDCJITProvision = "oidc.jit_provision"
	auditBreakGlass       = "auth.break_glass"
)

// OIDCUserStore resolves and just-in-time-provisions OIDC identities. storage
// implements it. The interface lives with its consumer (the callback handler).
type OIDCUserStore interface {
	// FindUserByOIDCSubject resolves a returning identity by its (provider,
	// subject) pair, returning the user, whether it is active, and
	// auth.ErrUserNotFound when no row matches.
	FindUserByOIDCSubject(ctx context.Context, provider, subject string) (*auth.User, bool, error)
	// CreateOIDCUser JIT-provisions an OIDC-only user with the given roles.
	CreateOIDCUser(ctx context.Context, tenant, email, provider, subject string, roles []string) (*auth.User, error)
	// RoleExists reports whether a role name exists for the tenant, used to fail
	// closed on a misconfigured default_role or role mapping.
	RoleExists(ctx context.Context, tenant, role string) (bool, error)
	// ReconcileUserRoles sets the user's DB roles to EXACTLY roleNames (the IdP is
	// authoritative). It fails closed on a name that is not a role in the tenant,
	// leaving the prior grants intact.
	ReconcileUserRoles(ctx context.Context, userID string, roleNames []string) error
}

// AuthAuditWriter records authentication events to the audit sink (H5). It is
// best-effort: a write error never changes the auth outcome.
type AuthAuditWriter interface {
	RecordAuthEvent(ctx context.Context, tenant, actorUserID, action, email, outcome string, extra map[string]string) error
}

// oidcDeps bundles what the callback handler needs beyond the flow itself.
type oidcDeps struct {
	flow      *oidc.Flow
	users     OIDCUserStore
	audit     AuthAuditWriter
	cfg       config.OIDCSection
	jwtSecret string
	tokenTTL  time.Duration
	logger    *slog.Logger
	// insecureCookies drops Secure from the session and state cookies; see
	// cookieSecure. Its zero value is the safe one.
	insecureCookies bool
}

// randomURLToken returns a cryptographically random, URL-safe token used for the
// CSRF state and the ID-token nonce.
func randomURLToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// setStateCookie and clearStateCookie are the only two writers of the state
// cookie, so its attributes cannot drift the way the session cookie's did. It is
// scoped to the auth path (it has no business being sent with every API call)
// and is HttpOnly for the same reason the session cookie is: it seals the nonce
// and the PKCE verifier.
func setStateCookie(c *gin.Context, value string, insecure bool) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(oidcStateCookie, value, int(oidc.StateCookieTTL.Seconds()), oidcStateCookiePath, "", cookieSecure(insecure), true)
}

func clearStateCookie(c *gin.Context, insecure bool) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(oidcStateCookie, "", -1, oidcStateCookiePath, "", cookieSecure(insecure), true)
}

// oidcLoginHandler implements GET /api/v2/auth/oidc/login: it starts the
// Authorization Code + PKCE flow. It generates the CSRF state, the nonce, and
// the PKCE verifier, seals them (plus the sanitized post-login target) in a
// signed HttpOnly state cookie, and redirects the browser to the IdP.
func oidcLoginHandler(flow *oidc.Flow, logger *slog.Logger, insecureCookies bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		state, serr := randomURLToken()
		nonce, nerr := randomURLToken()
		if serr != nil || nerr != nil {
			abortSSOServerFailure(c, logger, "generating login tokens", errors.Join(serr, nerr))
			return
		}
		verifier := oidc.GenerateCodeVerifier()
		next := sanitizeNext(c.Query("next"))
		cookie, err := flow.Codec().Encode(oidc.StatePayload{
			State: state, Nonce: nonce, Verifier: verifier, Next: next,
		}, oidc.StateCookieTTL)
		if err != nil {
			abortSSOServerFailure(c, logger, "encoding state cookie", err)
			return
		}
		setStateCookie(c, cookie, insecureCookies)
		c.Redirect(http.StatusFound, flow.AuthCodeURL(state, nonce, verifier))
	}
}

// oidcCallbackHandler implements GET /api/v2/auth/oidc/callback: it validates the
// CSRF state, exchanges the code (PKCE + client secret), verifies the ID token
// (issuer pin, audience, nonce, azp, clock skew, email_verified, tenant pin,
// email-domain allowlist), resolves or JIT-provisions the user, mints the app's
// _token session cookie, and redirects. Every terminal path is audited; a
// verification failure fails closed (see denyWithCause) and never falls back to
// a default identity.
func oidcCallbackHandler(deps oidcDeps) gin.HandlerFunc {
	return func(c *gin.Context) {
		payload, ok := deps.readState(c)
		if !ok {
			return
		}
		// CSRF: the state query param must match the signed cookie.
		if subtle.ConstantTimeCompare([]byte(c.Query("state")), []byte(payload.State)) != 1 {
			deps.deny(c, auditOIDCLoginFailure, "", "", "", "state_mismatch")
			return
		}
		// RFC 9207 issuer response param, when the IdP provides it, must be the
		// pinned issuer (H4/H1).
		if iss := c.Query("iss"); iss != "" && iss != deps.cfg.Issuer {
			deps.deny(c, auditOIDCLoginFailure, "", "", "", "iss_param_mismatch")
			return
		}
		// The IdP may redirect back with an error instead of a code.
		if e := c.Query("error"); e != "" {
			deps.deny(c, auditOIDCLoginFailure, "", "", "", "idp_error:"+e)
			return
		}
		code := c.Query("code")
		if code == "" {
			deps.deny(c, auditOIDCLoginFailure, "", "", "", "missing_code")
			return
		}
		rawIDToken, err := deps.flow.Exchange(c.Request.Context(), code, payload.Verifier)
		if err != nil {
			deps.logger.Warn("oidc: code exchange failed", "error", err)
			deps.deny(c, auditOIDCLoginFailure, "", "", "", "exchange_failed")
			return
		}
		identity, err := deps.flow.Verifier().Verify(c.Request.Context(), rawIDToken, payload.Nonce)
		if err != nil {
			deps.rejectVerify(c, err)
			return
		}
		user, err := deps.resolveUser(c, identity)
		if err != nil {
			return // resolveUser already audited + answered the rejection
		}
		token, terr := auth.MintUserToken(deps.jwtSecret, deps.tokenTTL, *user)
		if terr != nil {
			abortSSOServerFailure(c, deps.logger, "minting session token", terr)
			return
		}
		setSessionCookie(c, token, deps.tokenTTL, deps.insecureCookies)
		deps.record(c, auditOIDCLoginSuccess, identity.Tenant, user.ID, identity.Email, "success",
			map[string]string{"subject": identity.Subject, "roles": strings.Join(user.Roles, ",")})
		c.Redirect(http.StatusFound, sanitizeNext(payload.Next))
	}
}

// readState reads and verifies the signed state cookie. A missing or invalid
// cookie is a rejection (CSRF/replay), audited.
func (d oidcDeps) readState(c *gin.Context) (oidc.StatePayload, bool) {
	cookie, err := c.Request.Cookie(oidcStateCookie)
	if err != nil || cookie.Value == "" {
		d.deny(c, auditOIDCLoginFailure, "", "", "", "missing_state")
		return oidc.StatePayload{}, false
	}
	payload, err := d.flow.Codec().Decode(cookie.Value)
	if err != nil {
		d.deny(c, auditOIDCLoginFailure, "", "", "", "invalid_state")
		return oidc.StatePayload{}, false
	}
	// The state cookie is single-use for this attempt; clear it regardless of
	// outcome so it cannot be replayed.
	clearStateCookie(c, d.insecureCookies)
	return payload, true
}

// resolveUser turns a verified identity into a Leoflow user, applying the
// group→role mapping (with the default_role fallback), the JIT policy, and
// fail-closed role validation. On any rejection it audits and answers through
// denyWithCause, returning errRejected so the caller stops.
func (d oidcDeps) resolveUser(c *gin.Context, id *oidc.VerifiedIdentity) (*auth.User, error) {
	ctx := c.Request.Context()
	loginRoles := oidc.ApplyDefaultRole(oidc.MapRoles(id.Groups, d.cfg.RoleMappings), d.cfg.DefaultRole)
	// Fail closed on a role name (mapped or default_role) that does not exist for
	// the tenant, rather than minting a token referencing an unknown role.
	for _, role := range loginRoles {
		exists, err := d.users.RoleExists(ctx, id.Tenant, role)
		if err != nil {
			// RoleExists resolves the tenant before the role, so ErrNotFound here is
			// a tenant that does not exist and not a lookup that failed. They used to
			// share role_check_failed, which left the audit row unable to tell a
			// one-character typo in tenant_claims from a database outage. Boot names
			// the typo precisely now; login has to agree with it.
			if errors.Is(err, domain.ErrNotFound) {
				d.denyWithCause(c, auditOIDCLoginFailure, id.Tenant, "", id.Email, "unknown_tenant", err)
				return nil, errRejected
			}
			d.logger.Error("oidc: checking role", "error", err)
			d.deny(c, auditOIDCLoginFailure, id.Tenant, "", id.Email, "role_check_failed")
			return nil, errRejected
		}
		if !exists {
			d.deny(c, auditOIDCLoginFailure, id.Tenant, "", id.Email, "unknown_role:"+role)
			return nil, errRejected
		}
	}

	user, active, err := d.users.FindUserByOIDCSubject(ctx, id.Provider, id.Subject)
	var resolved *auth.User
	switch {
	case err == nil:
		if !active {
			d.deny(c, auditOIDCLoginFailure, id.Tenant, user.ID, id.Email, "inactive")
			return nil, errRejected
		}
		// Defense-in-depth (H1 residual): never mint a session against a tenant the
		// stored user does not belong to. Unreachable under a pinned single-tenant
		// issuer (the attacker cannot alter tid/hd), but trusting the claim-derived
		// tenant while discarding the stored one is a latent confused-deputy path;
		// reject a mismatch explicitly and bind the session to the stored tenant.
		if user.TenantID != id.Tenant {
			d.deny(c, auditOIDCLoginFailure, id.Tenant, user.ID, id.Email, "tenant_mismatch")
			return nil, errRejected
		}
		resolved = &auth.User{ID: user.ID, TenantID: user.TenantID, Email: id.Email, Roles: loginRoles}
	case errors.Is(err, auth.ErrUserNotFound):
		resolved, err = d.jitProvision(c, id, loginRoles)
		if err != nil {
			return nil, err // jitProvision already audited + answered the rejection
		}
	default:
		d.logger.Error("oidc: resolving user", "error", err)
		d.deny(c, auditOIDCLoginFailure, id.Tenant, "", id.Email, "user_lookup_failed")
		return nil, errRejected
	}

	// IdP-authoritative role reconciliation (D5): set the DB roles to EXACTLY the
	// group-mapped set for BOTH new and returning users, so the per-request authz
	// reload reflects the IdP's current groups — an IdP demotion or deprovisioning
	// takes effect on the next login instead of stale DB grants winning. A failure
	// here rejects the login rather than minting a token the DB does not back.
	if rerr := d.users.ReconcileUserRoles(ctx, resolved.ID, loginRoles); rerr != nil {
		d.logger.Error("oidc: reconciling roles", "error", rerr)
		d.deny(c, auditOIDCLoginFailure, id.Tenant, resolved.ID, id.Email, "role_reconcile_failed")
		return nil, errRejected
	}
	return resolved, nil
}

// jitProvision handles the no-matching-row case per D4: reject when JIT is off,
// create the user with the mapped roles when it is on.
func (d oidcDeps) jitProvision(c *gin.Context, id *oidc.VerifiedIdentity, loginRoles []string) (*auth.User, error) {
	ctx := c.Request.Context()
	if !d.cfg.JITProvisioning {
		d.deny(c, auditOIDCLoginFailure, id.Tenant, "", id.Email, "no_user_jit_off")
		return nil, errRejected
	}
	created, err := d.users.CreateOIDCUser(ctx, id.Tenant, id.Email, id.Provider, id.Subject, loginRoles)
	if err != nil {
		if errors.Is(err, domain.ErrValidation) {
			d.deny(c, auditOIDCLoginFailure, id.Tenant, "", id.Email, "unknown_role")
			return nil, errRejected
		}
		// A concurrent login may have provisioned the same identity first.
		if errors.Is(err, domain.ErrConflict) {
			if u, active, ferr := d.users.FindUserByOIDCSubject(ctx, id.Provider, id.Subject); ferr == nil && active {
				return &auth.User{ID: u.ID, TenantID: id.Tenant, Email: id.Email, Roles: loginRoles}, nil
			}
		}
		d.logger.Error("oidc: jit provisioning", "error", err)
		d.deny(c, auditOIDCLoginFailure, id.Tenant, "", id.Email, "jit_failed")
		return nil, errRejected
	}
	d.recordCtx(ctx, auditOIDCJITProvision, id.Tenant, created.ID, id.Email, "success",
		map[string]string{"subject": id.Subject, "roles": strings.Join(loginRoles, ",")})
	return &auth.User{ID: created.ID, TenantID: id.Tenant, Email: id.Email, Roles: loginRoles}, nil
}

// errRejected is a sentinel signaling that a helper already answered the
// rejection and audited it; the caller must simply stop.
var errRejected = errors.New("oidc: request already rejected")

// rejectVerify maps a verification error to a rejection, choosing the audit
// action so tenant-pin rejections are distinguishable from other verification
// failures.
func (d oidcDeps) rejectVerify(c *gin.Context, err error) {
	action := auditOIDCLoginFailure
	if errors.Is(err, oidc.ErrTenantNotAllowed) || errors.Is(err, oidc.ErrEmailNotVerified) || errors.Is(err, oidc.ErrEmailDomainNotAllowed) {
		action = auditOIDCTenantReject
	}
	reason := verifyReason(err)
	// token_invalid is the catch-all arm, and it swallowed the only thing that
	// tells a JWKS fetch failure apart from a wrong client_id: the error itself.
	// Attach the cause there, and on the one recognized reason whose error text
	// is the remedy rather than a restatement of the reason.
	//
	// Everything else verifyReason recognizes is a bare package-level sentinel
	// with fixed text, returned unwrapped, so logging it beside its own reason
	// adds a second copy of the same words. Withholding it there is about noise,
	// not about secrecy: none of those sentinels is formatted from a claim.
	if reason == reasonTokenInvalid || errors.Is(err, oidc.ErrGroupOverage) {
		d.denyWithCause(c, action, "", "", "", reason, err)
		return
	}
	d.deny(c, action, "", "", "", reason)
}

// reasonTokenInvalid is the arm of verifyReason that means "we did not recognize
// this", which is exactly the case where the operator needs the real error.
const reasonTokenInvalid = "token_invalid"

// verifyReason returns a stable, non-secret audit reason for a verification
// error.
func verifyReason(err error) string {
	switch {
	case errors.Is(err, oidc.ErrIssuerMismatch):
		return "issuer_mismatch"
	case errors.Is(err, oidc.ErrNonceMismatch):
		return "nonce_mismatch"
	case errors.Is(err, oidc.ErrAzpMismatch):
		return "azp_mismatch"
	case errors.Is(err, oidc.ErrTokenExpired):
		return "token_expired"
	case errors.Is(err, oidc.ErrEmailNotVerified):
		return "email_not_verified"
	case errors.Is(err, oidc.ErrTenantNotAllowed):
		return "tenant_not_allowed"
	case errors.Is(err, oidc.ErrTenantAmbiguous):
		return "tenant_ambiguous"
	case errors.Is(err, oidc.ErrTenantClaimShape):
		return "tenant_claim_shape"
	case errors.Is(err, oidc.ErrEmailDomainNotAllowed):
		return "email_domain_not_allowed"
	case errors.Is(err, oidc.ErrMissingExpiry):
		return "token_missing_expiry"
	case errors.Is(err, oidc.ErrNoSubject):
		return "token_no_subject"
	case errors.Is(err, oidc.ErrGroupOverage):
		return "group_claim_overage"
	default:
		return reasonTokenInvalid
	}
}

// deny audits a failed login (outcome "denied", with a non-secret reason) and
// answers it. Every fail-closed path funnels through here so a rejection is
// always both recorded and answered, never a silent fall-through.
func (d oidcDeps) deny(c *gin.Context, action, tenant, userID, email, reason string) {
	d.denyWithCause(c, action, tenant, userID, email, reason, nil)
}

// denyWithCause is deny plus the underlying error, for the arms where the reason
// alone does not identify the failure.
//
// The log line is the point. The audit row is the record of truth, but it lives
// in a Postgres table reachable only through an API that needs a working session,
// and under provider: oidc with an empty break_glass_emails there is no session
// to be had. So an operator whose SSO rejects every login had exactly one channel
// they could read, and it said nothing: a deployment failing 100% of logins
// looked identical to one nobody was using.
//
// WARN rather than INFO because a rejected login is an operational event, and
// rather than ERROR because fail-closed is this path working as designed.
func (d oidcDeps) denyWithCause(c *gin.Context, action, tenant, userID, email, reason string, cause error) {
	d.recordCtx(c.Request.Context(), action, tenant, userID, email, "denied", map[string]string{"reason": reason})
	attrs := []any{"reason", reason, "action", action}
	if email != "" {
		attrs = append(attrs, "email", email)
	}
	if userID != "" {
		attrs = append(attrs, "user", userID)
	}
	if tenant != "" {
		attrs = append(attrs, "tenant", tenant)
	}
	if cause != nil {
		attrs = append(attrs, "error", cause.Error())
	}
	if d.logger != nil {
		d.logger.Warn("oidc: login denied", attrs...)
	}
	// Both OIDC routes are reached only by a top-level browser navigation: the
	// user clicks the sign-in control, or the IdP redirects them back. problem+json
	// is the right answer to an API client and the wrong one to a browser, which
	// renders it as a page of raw JSON with no way back to the sign-in page.
	//
	// The target carries no reason, only that one attempt failed. Withholding the
	// cause from the browser is the whole posture of this path (it is the same
	// information an attacker probing the deployment is after), and the cause is
	// already in the audit row and the WARN above, which is where an operator
	// reads it.
	c.Redirect(http.StatusFound, loginPageWithSSOError)
	c.Abort()
}

// loginPageWithSSOError is where a refused single sign-on sends the browser. The
// marker carries no reason: it exists so the login page can say that sign-on
// failed, rather than showing the same bare form the user was just redirected
// away from, which reads as the click having done nothing.
const loginPageWithSSOError = "/api/v2/auth/login?" + ssoErrorParam + "=" + ssoErrorRefused

// The two sso_error markers. They carry no reason, only which of the two things
// happened, because the words the page needs are different: a refusal is the
// deployment saying no and needs an administrator, a failure is Leoflow breaking
// and needs a retry. Telling a user to go find an administrator for a transient
// mint error sends them somewhere no setting will help.
const (
	ssoErrorParam   = "sso_error"
	ssoErrorRefused = "1"
	ssoErrorServer  = "server"
)

// loginPageWithSSOServerFailure is where the three 500 paths on these routes send
// the browser. They are browser-only routes, so problem+json reached the user as
// a page of raw JSON; the mint failure is the worst of the three, being the tail
// of a completely successful round trip through the IdP.
const loginPageWithSSOServerFailure = "/api/v2/auth/login?" + ssoErrorParam + "=" + ssoErrorServer

// abortSSOServerFailure logs the cause and returns the browser to the login page
// saying Leoflow broke. The cause never reaches the browser, for the same reason
// a denial's does not, and the log line is what an operator reads.
func abortSSOServerFailure(c *gin.Context, logger *slog.Logger, what string, cause error) {
	if logger != nil {
		logger.Error("oidc: "+what, "error", cause)
	}
	c.Redirect(http.StatusFound, loginPageWithSSOServerFailure)
	c.Abort()
}

// record audits an event using the request context.
func (d oidcDeps) record(c *gin.Context, action, tenant, userID, email, outcome string, extra map[string]string) {
	d.recordCtx(c.Request.Context(), action, tenant, userID, email, outcome, extra)
}

// recordCtx writes one audit event, best-effort: a sink error is logged and
// dropped so it never changes the auth outcome.
func (d oidcDeps) recordCtx(ctx context.Context, action, tenant, userID, email, outcome string, extra map[string]string) {
	if d.audit == nil {
		return
	}
	if err := d.audit.RecordAuthEvent(ctx, tenant, userID, action, email, outcome, extra); err != nil {
		d.logger.Error("oidc: recording audit event", "action", action, "error", err)
	}
}
