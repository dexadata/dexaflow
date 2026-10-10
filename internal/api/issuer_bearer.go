package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/issuer"
)

// auditIssuerBearerFailure records a trusted-issuer bearer that verified but
// named no active user linked in its tenant (#1468). Successes are not
// audited: a bearer authenticates every request of a client, and the
// requests that change something are audited on their own.
const auditIssuerBearerFailure = "issuer.bearer.failure"

// TrustedIssuerBearer verifies bearer tokens from the operator's trusted
// issuer (#1468, ADR 0050 D9). issuer.Verifier implements it.
type TrustedIssuerBearer interface {
	// Provider is the key the issuer's users are linked under.
	Provider() string
	// VerifyBearer checks a token sent as a request bearer and returns the
	// identity it carries.
	VerifyBearer(ctx context.Context, raw string) (*issuer.Identity, error)
}

// issuerBearerAuth authenticates a request whose bearer is not an engine
// token but one the trusted issuer signed for a bearer audience.
type issuerBearerAuth struct {
	issuer TrustedIssuerBearer
	users  TrustedIssuerUserStore
	audit  AuthAuditWriter
	logger *slog.Logger
}

// authenticate verifies raw and resolves the user it names, on every request,
// so deactivating or unlinking the user ends its access at once whatever the
// token's expiry. A refusal is auth.ErrInvalidToken (401); a failed lookup or
// an issuer whose keys cannot be downloaded is returned as is, so the caller
// answers 503 rather than "sign in again".
func (a *issuerBearerAuth) authenticate(ctx context.Context, raw string) (*auth.User, error) {
	id, err := a.issuer.VerifyBearer(ctx, raw)
	if errors.Is(err, issuer.ErrKeysUnavailable) {
		// The issuer's keys could not be downloaded: the token was not checked,
		// so this is an outage (503), not a refusal.
		return nil, fmt.Errorf("verifying trusted issuer bearer: %w", err)
	}
	if err != nil {
		a.logger.Warn("trusted issuer bearer refused: token", "reason", err)
		return nil, errors.Join(auth.ErrInvalidToken, err)
	}
	user, reason, err := linkedUser(ctx, a.users, a.issuer.Provider(), id)
	if err != nil {
		return nil, err
	}
	if reason != "" {
		a.logger.Warn("trusted issuer bearer refused: user", "reason", reason, "tenant", id.Tenant, "email", id.Email)
		if a.audit != nil {
			if err := a.audit.RecordAuthEvent(ctx, id.Tenant, "", auditIssuerBearerFailure, id.Email, "failure", map[string]string{"reason": reason}); err != nil {
				a.logger.Warn("auth audit write failed", "action", auditIssuerBearerFailure, "error", err)
			}
		}
		return nil, fmt.Errorf("%w: %s", auth.ErrInvalidToken, reason)
	}
	// The token's scopes narrow this request only (ADR 0067). Copy the row so
	// they never ride back into whatever the store hands out next.
	scoped := *user
	scoped.Scoped = true
	scoped.Scopes = id.Scopes
	return &scoped, nil
}

// linkedUser finds the active user an issuer identity is linked to, in the
// identity's tenant. A refusal comes back as a stable audit reason with a nil
// error; err is a lookup failure. The handoff and the bearer share it, so both
// apply the same rule.
func linkedUser(ctx context.Context, users TrustedIssuerUserStore, provider string, id *issuer.Identity) (user *auth.User, reason string, err error) {
	user, active, err := users.FindUserByOIDCSubject(ctx, provider, id.Subject)
	switch {
	case errors.Is(err, auth.ErrUserNotFound):
		return nil, "user_not_linked", nil
	case err != nil:
		return nil, "", fmt.Errorf("looking up trusted issuer user: %w", err)
	case !active:
		return nil, "user_inactive", nil
	case user.TenantID != id.Tenant:
		// The subject is linked in another tenant than the token names. Acting
		// in either would let the issuer's claim and Dexaflow's record disagree
		// about where this person works, so neither wins.
		return nil, "tenant_mismatch", nil
	}
	return user, "", nil
}
