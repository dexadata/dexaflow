package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/domain"
)

// suspensionResponse is the body of the suspension routes: whether the tenant
// is suspended and, when it is, how, why and since when.
type suspensionResponse struct {
	Tenant      string     `json:"tenant"`
	Suspended   bool       `json:"suspended"`
	Mode        string     `json:"mode,omitempty"`
	Reason      string     `json:"reason,omitempty"`
	SuspendedAt *time.Time `json:"suspended_at,omitempty"`
}

func newSuspensionResponse(tenant string, s domain.TenantSuspension) suspensionResponse {
	out := suspensionResponse{Tenant: tenant, Suspended: s.Active()}
	if out.Suspended {
		out.Mode, out.Reason = string(s.Mode), s.Reason
		if !s.Since.IsZero() {
			since := s.Since.UTC()
			out.SuspendedAt = &since
		}
	}
	return out
}

// serviceTenantParam returns the tenant of the path, or answers 400 and
// returns false when it is not a name the service API accepts.
func serviceTenantParam(c *gin.Context) (string, bool) {
	name := c.Param("tenant")
	if !serviceTenantName.MatchString(name) {
		AbortProblem(c, http.StatusBadRequest, "bad request", "tenant must be 1-63 lowercase letters, digits or '-'")
		return "", false
	}
	return name, true
}

// abortSuspensionError answers a store error of the suspension routes.
func abortSuspensionError(c *gin.Context, err error, what string) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		AbortProblem(c, http.StatusNotFound, "not found", "no such tenant")
	case errors.Is(err, domain.ErrValidation):
		AbortProblemCause(c, http.StatusBadRequest, "bad request", safeDetail(err, detailInvalidInput), err)
	default:
		AbortProblemCause(c, http.StatusInternalServerError, "internal error", what, err)
	}
}

// suspendTenantHandler implements PUT /api/v2/service/tenants/{tenant}/suspension
// with {"mode": "hold", "reason": "..."} (#2181). From then on the tenant gets
// no new run, manual or scheduled; the work already queued or running
// finishes, and the tenant's DAGs keep their own paused flag. Calling it again
// changes the reason and keeps the time the suspension started. "drain" and
// "stop" are refused with a 400 until the engine applies them.
func suspendTenantHandler(deps Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		name, ok := serviceTenantParam(c)
		if !ok {
			return
		}
		var body struct {
			Mode   string `json:"mode"`
			Reason string `json:"reason"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", `body must be {"mode": "hold", "reason": "..."}`)
			return
		}
		s := domain.TenantSuspension{Mode: domain.SuspensionMode(body.Mode), Reason: body.Reason}
		if err := s.Validate(); err != nil {
			abortSuspensionError(c, err, "")
			return
		}
		ctx := c.Request.Context()
		err := deps.Issuers.ServiceTenants.SuspendTenant(ctx, name, s)
		recordService(c, deps, auditServiceTenantSuspend, name, "", "", err, map[string]string{"mode": body.Mode, "reason": body.Reason})
		if err != nil {
			abortSuspensionError(c, err, "could not suspend the tenant")
			return
		}
		stored, err := deps.Issuers.ServiceTenants.TenantSuspension(ctx, name)
		if err != nil {
			abortSuspensionError(c, err, "the tenant is suspended, but its suspension could not be read back")
			return
		}
		deps.Observability.Logger.Info("service api: tenant suspended", "tenant", logSafe(name), "mode", body.Mode)
		c.JSON(http.StatusOK, newSuspensionResponse(name, stored))
	}
}

// tenantSuspensionHandler implements GET /api/v2/service/tenants/{tenant}/suspension.
func tenantSuspensionHandler(deps Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		name, ok := serviceTenantParam(c)
		if !ok {
			return
		}
		s, err := deps.Issuers.ServiceTenants.TenantSuspension(c.Request.Context(), name)
		if err != nil {
			abortSuspensionError(c, err, "could not read the tenant's suspension")
			return
		}
		c.JSON(http.StatusOK, newSuspensionResponse(name, s))
	}
}

// resumeTenantHandler implements DELETE /api/v2/service/tenants/{tenant}/suspension:
// it lifts the suspension and answers 204, also when the tenant was not
// suspended. Scheduled DAGs pick up their due slots on the next tick.
func resumeTenantHandler(deps Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		name, ok := serviceTenantParam(c)
		if !ok {
			return
		}
		err := deps.Issuers.ServiceTenants.ResumeTenant(c.Request.Context(), name)
		recordService(c, deps, auditServiceTenantResume, name, "", "", err, nil)
		if err != nil {
			abortSuspensionError(c, err, "could not resume the tenant")
			return
		}
		deps.Observability.Logger.Info("service api: tenant resumed", "tenant", logSafe(name))
		c.Status(http.StatusNoContent)
	}
}
