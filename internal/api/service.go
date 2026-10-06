package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"io"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

// ServiceTenantStore is what the service API needs from storage (#1283).
// storage.Repository implements it.
type ServiceTenantStore interface {
	// EnsureTenant creates a tenant with the built-in roles and default pool,
	// or fills in what is missing; created reports whether it was new.
	// limits sets the tenant limits it carries and leaves the others.
	EnsureTenant(ctx context.Context, name, displayName string, defaultPoolSlots int, limits domain.TenantLimitsUpdate) (created bool, err error)
	// EnsureIssuerUser makes sure a passwordless user linked to (provider,
	// subject) exists in tenant with exactly roles.
	EnsureIssuerUser(ctx context.Context, tenant, email, provider, subject string, roles []string) (*auth.User, bool, error)
}

// serviceTenantName is a tenant name the service API accepts: lowercase
// letters, digits and '-', so it can travel in a URL, a claim and a label.
var serviceTenantName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// Audit actions for the operator service API (#1283). Both create or change
// accounts and roles, so every call that reaches the store is recorded.
const (
	auditServiceTenantEnsure = "service.tenant.ensure"
	auditServiceUserEnsure   = "service.user.ensure"
)

// recordService writes one service API audit event. A failed write is logged,
// never returned: the operation itself already happened or already failed.
func recordService(c *gin.Context, deps Dependencies, action, tenant, userID, email string, err error, extra map[string]string) {
	if deps.AuthAudit == nil {
		return
	}
	outcome := "success"
	if err != nil {
		outcome = "failure"
	}
	if aerr := deps.AuthAudit.RecordAuthEvent(c.Request.Context(), tenant, userID, action, email, outcome, extra); aerr != nil {
		deps.Logger.Warn("auth audit write failed", "action", action, "error", aerr)
	}
}

// issuerMaySignInTo reports whether the trusted issuer is allowed to sign in
// to tenant, the same list its handoff tokens are checked against.
func issuerMaySignInTo(allowed []string, tenant string) bool {
	return slices.Contains(allowed, "*") || slices.Contains(allowed, tenant)
}

// registerService mounts the operator service API (#1283) under
// /api/v2/service/, guarded by the service token instead of a user session.
// It is called only when a service token is configured.
func registerService(r gin.IRouter, deps Dependencies) {
	g := r.Group("/api/v2/service", serviceAuth(deps.ServiceToken))
	g.PUT("/tenants/:tenant", ensureTenantHandler(deps))
	g.PUT("/tenants/:tenant/users/:subject", ensureIssuerUserHandler(deps))
}

// serviceAuth accepts only `Authorization: Bearer <service token>`. The
// comparison is constant-time over SHA-256 digests, so neither the token nor
// its length leaks through timing.
func serviceAuth(token string) gin.HandlerFunc {
	want := sha256.Sum256([]byte(token))
	return func(c *gin.Context) {
		got, ok := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer ")
		sum := sha256.Sum256([]byte(got))
		if !ok || subtle.ConstantTimeCompare(sum[:], want[:]) != 1 {
			AbortProblem(c, http.StatusUnauthorized, "unauthorized", "the service API needs the service token")
			return
		}
		c.Next()
	}
}

// tenantLimitsBody is the optional limits part of the ensure-tenant body. A
// field left out (or null) leaves that limit as it is; 0 makes it unlimited.
type tenantLimitsBody struct {
	MaxDags                    *int `json:"max_dags"`
	MaxRunsPerDay              *int `json:"max_runs_per_day"`
	MinScheduleIntervalSeconds *int `json:"min_schedule_interval_seconds"`
	MaxTaskPoolSlots           *int `json:"max_task_pool_slots"`
	MaxTasks                   *int `json:"max_tasks"`
	MaxTaskRunsPerMonth        *int `json:"max_task_runs_per_month"`
}

// fields pairs each limit with its JSON name, in a fixed order.
func (b tenantLimitsBody) fields() []struct {
	name  string
	value *int
} {
	return []struct {
		name  string
		value *int
	}{
		{"max_dags", b.MaxDags},
		{"max_runs_per_day", b.MaxRunsPerDay},
		{"min_schedule_interval_seconds", b.MinScheduleIntervalSeconds},
		{"max_task_pool_slots", b.MaxTaskPoolSlots},
		{"max_tasks", b.MaxTasks},
		{"max_task_runs_per_month", b.MaxTaskRunsPerMonth},
	}
}

// validate returns the name of the first limit outside 0..2147483647 (the
// INTEGER columns), or "" when every limit given fits.
func (b tenantLimitsBody) validate() string {
	for _, f := range b.fields() {
		if f.value != nil && (*f.value < 0 || *f.value > math.MaxInt32) {
			return f.name
		}
	}
	return ""
}

// audit records each limit given in the audit metadata.
func (b tenantLimitsBody) audit(meta map[string]string) {
	for _, f := range b.fields() {
		if f.value != nil {
			meta[f.name] = strconv.Itoa(*f.value)
		}
	}
}

func (b tenantLimitsBody) update() domain.TenantLimitsUpdate {
	return domain.TenantLimitsUpdate{
		MaxDags: b.MaxDags, MaxRunsPerDay: b.MaxRunsPerDay, MinScheduleIntervalSeconds: b.MinScheduleIntervalSeconds,
		MaxTaskPoolSlots: b.MaxTaskPoolSlots, MaxTasks: b.MaxTasks, MaxTaskRunsPerMonth: b.MaxTaskRunsPerMonth,
	}
}

// ensureTenantHandler implements PUT /api/v2/service/tenants/{tenant} with an
// optional {"display_name": "...", "default_pool_slots": N, "max_dags": N,
// "max_runs_per_day": N, "min_schedule_interval_seconds": N,
// "max_task_pool_slots": N, "max_tasks": N, "max_task_runs_per_month": N}:
// 201 when the
// tenant is new, 200 when it already existed. Either way it ends with the
// built-in roles and default pool; default_pool_slots, when given, sizes that
// pool, otherwise a new tenant gets the default tenant's size. Each limit
// given is stored; one left out keeps its stored value.
func ensureTenantHandler(deps Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		name := c.Param("tenant")
		if !serviceTenantName.MatchString(name) {
			AbortProblem(c, http.StatusBadRequest, "bad request", "tenant must be 1-63 lowercase letters, digits or '-'")
			return
		}
		var body struct {
			DisplayName      string `json:"display_name"`
			DefaultPoolSlots *int   `json:"default_pool_slots"`
			tenantLimitsBody
		}
		// The body is optional: an empty one is io.EOF, not a malformed request.
		if err := c.ShouldBindJSON(&body); err != nil && !errors.Is(err, io.EOF) {
			AbortProblem(c, http.StatusBadRequest, "bad request", "body must be JSON")
			return
		}
		poolSlots := 0
		if body.DefaultPoolSlots != nil {
			poolSlots = *body.DefaultPoolSlots
			if poolSlots < 1 || poolSlots > math.MaxInt32 {
				AbortProblem(c, http.StatusBadRequest, "bad request", "default_pool_slots must be a whole number from 1 to 2147483647")
				return
			}
		}
		if bad := body.validate(); bad != "" {
			AbortProblem(c, http.StatusBadRequest, "bad request", bad+" must be a whole number from 0 (unlimited) to 2147483647")
			return
		}
		created, err := deps.ServiceTenants.EnsureTenant(c.Request.Context(), name, body.DisplayName, poolSlots, body.update())
		meta := map[string]string{"created": strconv.FormatBool(created)}
		if poolSlots > 0 {
			meta["default_pool_slots"] = strconv.Itoa(poolSlots)
		}
		body.audit(meta)
		recordService(c, deps, auditServiceTenantEnsure, name, "", "", err, meta)
		if err != nil {
			AbortProblemCause(c, http.StatusInternalServerError, "internal error", "could not ensure the tenant", err)
			return
		}
		deps.Logger.Info("service api: tenant ensured", "tenant", logSafe(name), "created", created)
		c.JSON(statusFor(created), gin.H{"name": name, "created": created})
	}
}

// ensureIssuerUserHandler implements
// PUT /api/v2/service/tenants/{tenant}/users/{subject} with
// {"email": "...", "roles": [...]}: it makes sure a passwordless user linked
// to the trusted issuer's subject exists with exactly those roles, so the
// issuer's handoff (#1284) can sign them in. It needs a trusted issuer. A
// subject linked in another tenant and an email already used by another user
// of the tenant (for example a password account) are both 409. A tenant the
// trusted issuer may not sign in to (auth.trusted_issuer.allowed_tenants) is
// 403: such a user could never sign in, and the service token must not mint
// accounts outside the issuer's reach.
func ensureIssuerUserHandler(deps Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if deps.TrustedIssuer == nil {
			AbortProblem(c, http.StatusConflict, "conflict", "no trusted issuer is configured (auth.trusted_issuer), so there is nothing to link users to")
			return
		}
		var body struct {
			Email string   `json:"email"`
			Roles []string `json:"roles"`
		}
		if err := c.ShouldBindJSON(&body); err != nil || body.Email == "" {
			AbortProblem(c, http.StatusBadRequest, "bad request", `body must be {"email": "...", "roles": [...]}`)
			return
		}
		tenant, subject := c.Param("tenant"), c.Param("subject")
		extra := map[string]string{"subject": subject, "roles": strings.Join(body.Roles, ",")}
		if !issuerMaySignInTo(deps.ServiceAllowedTenants, tenant) {
			extra["reason"] = "tenant_not_allowed"
			recordService(c, deps, auditServiceUserEnsure, tenant, "", body.Email, errTenantNotAllowed, extra)
			AbortProblem(c, http.StatusForbidden, "forbidden", "the trusted issuer may not sign in to this tenant (auth.trusted_issuer.allowed_tenants)")
			return
		}
		user, created, err := deps.ServiceTenants.EnsureIssuerUser(c.Request.Context(), tenant, body.Email,
			deps.TrustedIssuer.Provider(), subject, body.Roles)
		userID := ""
		if err == nil {
			userID = user.ID
			extra["created"] = strconv.FormatBool(created)
		}
		recordService(c, deps, auditServiceUserEnsure, tenant, userID, body.Email, err, extra)
		switch {
		case err == nil:
			deps.Logger.Info("service api: user ensured", "tenant", logSafe(user.TenantID), "user_id", user.ID, "created", created)
			c.JSON(statusFor(created), gin.H{"id": user.ID, "tenant": user.TenantID, "email": user.Email, "roles": user.Roles, "created": created})
		case errors.Is(err, domain.ErrNotFound):
			AbortProblem(c, http.StatusNotFound, "not found", "no such tenant; create it first")
		case errors.Is(err, domain.ErrValidation):
			AbortProblemCause(c, http.StatusUnprocessableEntity, "unprocessable", "a role is not a role of this tenant", err)
		case errors.Is(err, domain.ErrConflict):
			AbortProblem(c, http.StatusConflict, "conflict", "the subject is linked in another tenant, or the email belongs to another user of this tenant")
		default:
			AbortProblemCause(c, http.StatusInternalServerError, "internal error", "could not ensure the user", err)
		}
	}
}

// errTenantNotAllowed marks the refused-tenant audit event as a failure.
var errTenantNotAllowed = errors.New("tenant not allowed for the trusted issuer")

func statusFor(created bool) int {
	if created {
		return http.StatusCreated
	}
	return http.StatusOK
}

// logSafe drops line breaks from a request-supplied value before it is logged,
// so a crafted tenant or subject cannot forge a log line.
func logSafe(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\n", ""), "\r", "")
}
