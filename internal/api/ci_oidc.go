package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/cioidc"
	"github.com/dexadata/dexaflow/internal/domain"
)

// ciTokenPath is the CI OIDC exchange (ADR 0092). It sits under the public
// /api/v2/auth/ prefix: it authenticates its caller with the provider's
// token in the body.
const ciTokenPath = "/api/v2/auth/ciToken"

// ciTokenMaxBody bounds the exchange body; a CI token is a few KiB.
const ciTokenMaxBody = 16 << 10

// ciTokenFailuresPerMinute is the refused exchanges one client address may
// make in a minute. Issued sessions do not count, so a busy runner pool
// behind one address is not throttled by its own deploys.
const ciTokenFailuresPerMinute = 30

// auditCIOIDCExchange is the audit action of an exchange, recorded in the
// tenant of the issuer entry.
const auditCIOIDCExchange = "ci_oidc.exchange"

// The outcomes of an exchange, the outcome label of
// dexaflow_ci_oidc_exchange_total.
const (
	ciOutcomeIssued          = "issued"
	ciOutcomeInvalidToken    = "invalid_token"
	ciOutcomeUnknownIssuer   = "unknown_issuer"
	ciOutcomeNoRule          = "no_rule"
	ciOutcomeRoleMissing     = "role_missing"
	ciOutcomeKeysUnavailable = "keys_unavailable"
	ciOutcomeError           = "error"
)

// CIOIDCVerifier checks CI provider tokens against the configured issuers
// and lists them per tenant. cioidc.Verifier implements it.
type CIOIDCVerifier interface {
	// Verify returns what the token may act as, or a *cioidc.Error.
	Verify(ctx context.Context, raw string) (*cioidc.Identity, error)
	// Advertised returns the entries of tenant for GET /api/v2/deployDefaults.
	Advertised(tenant string) []domain.CIOIDCIssuer
}

// CIOIDCExchangeRecorder counts exchanges by issuer entry and outcome.
// observability.Metrics implements it.
type CIOIDCExchangeRecorder interface {
	RecordCIOIDCExchange(issuer, outcome string)
}

// ciTokenRequest is the POST /api/v2/auth/ciToken body.
type ciTokenRequest struct {
	Token string `json:"token"`
}

// ciTokenResponse is a session issued by the exchange.
type ciTokenResponse struct {
	AccessToken string    `json:"access_token"`
	TokenType   string    `json:"token_type"`
	ExpiresIn   int       `json:"expires_in"`
	ExpiresAt   time.Time `json:"expires_at"`
	Issuer      string    `json:"issuer"`
	Tenant      string    `json:"tenant"`
	Role        string    `json:"role"`
	Scopes      []string  `json:"scopes"`
}

// ciTokenDeps is what the exchange needs.
type ciTokenDeps struct {
	verifier  CIOIDCVerifier
	roles     auth.RolePermissionStore
	secret    string
	ttl       time.Duration
	exchanges CIOIDCExchangeRecorder
	audit     AuthAuditWriter
	logger    *slog.Logger
	limiter   *auth.RateLimiter
	now       func() time.Time
}

// newCITokenDeps builds the exchange from deps, nil when CI OIDC is off.
func newCITokenDeps(deps Dependencies) *ciTokenDeps {
	if deps.Issuers.CIOIDC == nil || deps.Issuers.CIOIDCRoles == nil {
		return nil
	}
	ttl := deps.Issuers.CIOIDCSessionTTL
	if ttl <= 0 {
		ttl = time.Hour
	}
	return &ciTokenDeps{
		verifier: deps.Issuers.CIOIDC, roles: deps.Issuers.CIOIDCRoles, secret: deps.Auth.JWTSecret, ttl: ttl,
		exchanges: deps.Issuers.CIOIDCExchanges, audit: deps.Auth.AuthAudit, logger: deps.Observability.Logger,
		limiter: auth.NewRateLimiter(ciTokenFailuresPerMinute, time.Minute), now: time.Now,
	}
}

// ciTokenHandler implements POST /api/v2/auth/ciToken: a CI provider's
// token in, a session for the role its issuer's first matching rule names
// out (ADR 0092).
func ciTokenHandler(d *ciTokenDeps) gin.HandlerFunc {
	return func(c *gin.Context) {
		ip := c.ClientIP()
		if d.limiter.Blocked(ip) {
			AbortProblem(c, http.StatusTooManyRequests, "rate limited", rateLimitedDetail)
			return
		}
		raw, ok := readCITokenBody(c)
		if !ok {
			return
		}
		ctx := c.Request.Context()
		id, err := d.verifier.Verify(ctx, raw)
		if err == nil {
			err = d.checkRole(ctx, id)
		}
		if err != nil {
			d.refuse(c, id, err)
			d.limiter.Allow(ip)
			return
		}
		d.issue(c, id)
	}
}

// readCITokenBody reads the exchange body, answering 400 for one that is
// not a JSON object with a non-empty token and nothing else.
func readCITokenBody(c *gin.Context) (string, bool) {
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, ciTokenMaxBody+1))
	if err != nil || len(body) > ciTokenMaxBody {
		AbortProblem(c, http.StatusBadRequest, "bad request", "the body must be a JSON object of at most 16 KiB")
		return "", false
	}
	var req ciTokenRequest
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.Token == "" {
		AbortProblem(c, http.StatusBadRequest, "bad request", `the body must be {"token": "<the CI provider's OIDC token>"}`)
		return "", false
	}
	return req.Token, true
}

// errCIRoleMissing is a rule naming a role its tenant does not have.
var errCIRoleMissing = errors.New("the rule's role is not a role of the issuer's tenant")

// checkRole makes sure the role the rule names exists in the entry's
// tenant before a session is issued for it.
func (d *ciTokenDeps) checkRole(ctx context.Context, id *cioidc.Identity) error {
	_, err := d.roles.RolePermissions(ctx, id.Tenant, id.Role)
	if errors.Is(err, auth.ErrRoleNotFound) {
		return errCIRoleMissing
	}
	return err
}

// classify maps a refusal to its status, metric outcome and client detail.
func classifyCIRefusal(err error) (status int, outcome, detail string) {
	switch {
	case errors.Is(err, cioidc.ErrUnknownIssuer):
		return http.StatusUnauthorized, ciOutcomeUnknownIssuer, "no trusted CI issuer accepts this token"
	case errors.Is(err, cioidc.ErrInvalidToken):
		return http.StatusUnauthorized, ciOutcomeInvalidToken, "invalid token"
	case errors.Is(err, cioidc.ErrNoMatchingRule):
		return http.StatusForbidden, ciOutcomeNoRule, "no rule of the CI issuer admits this job"
	case errors.Is(err, errCIRoleMissing):
		return http.StatusForbidden, ciOutcomeRoleMissing, "the role the CI issuer's rule names does not exist in its tenant"
	case errors.Is(err, cioidc.ErrKeysUnavailable):
		return http.StatusServiceUnavailable, ciOutcomeKeysUnavailable, "the CI provider's signing keys cannot be read; try again"
	}
	return http.StatusServiceUnavailable, ciOutcomeError, "the exchange is temporarily unavailable"
}

// refuse answers a refused exchange, counts it, logs it with the entry and
// the job's subject (never the token), and audits it in the entry's tenant
// when the token named an entry.
func (d *ciTokenDeps) refuse(c *gin.Context, id *cioidc.Identity, err error) {
	status, outcome, detail := classifyCIRefusal(err)
	var e *cioidc.Error
	issuerName, tenant, subject, role := "", "", "", ""
	if errors.As(err, &e) {
		issuerName, tenant, subject = e.Issuer, e.Tenant, e.Subject
	}
	if id != nil {
		issuerName, tenant, subject, role = id.Issuer, id.Tenant, id.Subject, id.Role
	}
	d.count(issuerName, outcome)
	d.logger.WarnContext(c.Request.Context(), "ci oidc exchange refused", "issuer", issuerName, "tenant", tenant,
		"subject", subject, "role", role, "outcome", outcome, "reason", err.Error())
	if tenant != "" {
		d.record(c.Request.Context(), tenant, issuerName, subject, role, "failure", outcome)
	}
	if status == http.StatusServiceUnavailable {
		AbortProblemCause(c, status, "service unavailable", detail, err)
		return
	}
	AbortProblem(c, status, http.StatusText(status), detail)
}

// issue mints the session for id and answers it.
func (d *ciTokenDeps) issue(c *gin.Context, id *cioidc.Identity) {
	ctx := c.Request.Context()
	tok, exp, err := auth.MintCISession(d.secret, d.ttl, d.now(), auth.CISession{
		Issuer: id.Issuer, Fingerprint: id.Fingerprint, Tenant: id.Tenant, Subject: id.Subject, Role: id.Role, Scopes: id.Scopes,
	})
	if err != nil {
		d.refuse(c, id, err)
		return
	}
	d.count(id.Issuer, ciOutcomeIssued)
	d.logger.InfoContext(ctx, "ci oidc session issued", "issuer", id.Issuer, "tenant", id.Tenant, "subject", id.Subject,
		"role", id.Role, "scopes", id.Scopes, "expires_at", exp)
	d.record(ctx, id.Tenant, id.Issuer, id.Subject, id.Role, "success", ciOutcomeIssued)
	c.JSON(http.StatusOK, ciTokenResponse{
		AccessToken: tok, TokenType: "Bearer", ExpiresIn: int(d.ttl / time.Second), ExpiresAt: exp,
		Issuer: id.Issuer, Tenant: id.Tenant, Role: id.Role, Scopes: id.Scopes,
	})
}

// count records one exchange; an exchange that named no entry is counted
// under "unknown".
func (d *ciTokenDeps) count(issuerName, outcome string) {
	if d.exchanges == nil {
		return
	}
	if issuerName == "" {
		issuerName = "unknown"
	}
	d.exchanges.RecordCIOIDCExchange(issuerName, outcome)
}

// record audits an exchange in tenant. A failed write is logged; the
// exchange's answer does not depend on it.
func (d *ciTokenDeps) record(ctx context.Context, tenant, issuerName, subject, role, result, outcome string) {
	if d.audit == nil {
		return
	}
	fields := map[string]string{"ci_issuer": issuerName, "subject": subject, "role": role, "outcome": outcome}
	if err := d.audit.RecordAuthEvent(ctx, tenant, "", auditCIOIDCExchange, "ci:"+issuerName, result, fields); err != nil {
		d.logger.WarnContext(ctx, "auth audit write failed", "action", auditCIOIDCExchange, "error", err)
	}
}
