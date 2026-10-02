package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"io"
	"net/http"
	"regexp"
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
	EnsureTenant(ctx context.Context, name, displayName string) (created bool, err error)
	// EnsureIssuerUser makes sure a passwordless user linked to (provider,
	// subject) exists in tenant with exactly roles.
	EnsureIssuerUser(ctx context.Context, tenant, email, provider, subject string, roles []string) (*auth.User, bool, error)
}

// serviceTenantName is a tenant name the service API accepts: lowercase
// letters, digits and '-', so it can travel in a URL, a claim and a label.
var serviceTenantName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

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

// ensureTenantHandler implements PUT /api/v2/service/tenants/{tenant} with an
// optional {"display_name": "..."}: 201 when the tenant is new, 200 when it
// already existed. Either way it ends with the built-in roles and default pool.
func ensureTenantHandler(deps Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		name := c.Param("tenant")
		if !serviceTenantName.MatchString(name) {
			AbortProblem(c, http.StatusBadRequest, "bad request", "tenant must be 1-63 lowercase letters, digits or '-'")
			return
		}
		var body struct {
			DisplayName string `json:"display_name"`
		}
		// The body is optional: an empty one is io.EOF, not a malformed request.
		if err := c.ShouldBindJSON(&body); err != nil && !errors.Is(err, io.EOF) {
			AbortProblem(c, http.StatusBadRequest, "bad request", "body must be JSON")
			return
		}
		created, err := deps.ServiceTenants.EnsureTenant(c.Request.Context(), name, body.DisplayName)
		if err != nil {
			AbortProblemCause(c, http.StatusInternalServerError, "internal error", "could not ensure the tenant", err)
			return
		}
		deps.Logger.Info("service api: tenant ensured", "tenant", name, "created", created)
		c.JSON(statusFor(created), gin.H{"name": name, "created": created})
	}
}

// ensureIssuerUserHandler implements
// PUT /api/v2/service/tenants/{tenant}/users/{subject} with
// {"email": "...", "roles": [...]}: it makes sure a passwordless user linked
// to the trusted issuer's subject exists with exactly those roles, so the
// issuer's handoff (#1284) can sign them in. It needs a trusted issuer.
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
		user, created, err := deps.ServiceTenants.EnsureIssuerUser(c.Request.Context(), tenant, body.Email,
			deps.TrustedIssuer.Provider(), subject, body.Roles)
		switch {
		case err == nil:
			deps.Logger.Info("service api: user ensured", "tenant", tenant, "user_id", user.ID, "created", created)
			c.JSON(statusFor(created), gin.H{"id": user.ID, "tenant": user.TenantID, "email": user.Email, "roles": user.Roles, "created": created})
		case errors.Is(err, domain.ErrNotFound):
			AbortProblem(c, http.StatusNotFound, "not found", "no such tenant; create it first")
		case errors.Is(err, domain.ErrValidation):
			AbortProblemCause(c, http.StatusUnprocessableEntity, "unprocessable", "a role is not a role of this tenant", err)
		case errors.Is(err, domain.ErrConflict):
			AbortProblem(c, http.StatusConflict, "conflict", "this subject is already linked in another tenant")
		default:
			AbortProblemCause(c, http.StatusInternalServerError, "internal error", "could not ensure the user", err)
		}
	}
}

func statusFor(created bool) int {
	if created {
		return http.StatusCreated
	}
	return http.StatusOK
}
