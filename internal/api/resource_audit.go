package api

import (
	"context"
	"log/slog"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
)

// ResourceAuditWriter records an accepted admin write to a named resource of
// the tenant: a variable, a connection or a pool (#2228). metadata names the
// fields the request set and never carries a secret value.
type ResourceAuditWriter interface {
	RecordResourceAudit(ctx context.Context, tenant, userID, action, resourceType, resourceID string, metadata map[string]string) error
}

// resourceAuditor returns w as a ResourceAuditWriter, or nil when the store
// behind it does not record resource writes.
func resourceAuditor(w AuditWriter) ResourceAuditWriter {
	if r, ok := w.(ResourceAuditWriter); ok {
		return r
	}
	return nil
}

// actingUserID is the id of the caller, or "" when the request carries none.
func actingUserID(c *gin.Context) string {
	if u, ok := UserFromContext(c); ok {
		return u.ID
	}
	return ""
}

// recordResourceAudit writes a best-effort entry for an accepted write: a
// failed audit write is logged and never fails the change the user asked for,
// as for the other entries written after the change.
func recordResourceAudit(c *gin.Context, w ResourceAuditWriter, action, resourceType, resourceID string, metadata map[string]string) {
	if w == nil {
		return
	}
	if err := w.RecordResourceAudit(c.Request.Context(), tenantOf(c), actingUserID(c), action, resourceType, resourceID, metadata); err != nil {
		slog.Warn("recording resource audit", "action", action, "resource", logSafe(resourceID), "error", logSafe(err.Error()))
	}
}

// recordDagAudit writes a best-effort entry on the DAG of the request, with
// runID when the action is on one run.
func recordDagAudit(c *gin.Context, w AuditWriter, action, runID string) {
	if w == nil {
		return
	}
	if err := w.RecordRunActionAudit(c.Request.Context(), tenantOf(c), actingUserID(c), action, c.Param("dag_id"), runID, nil); err != nil {
		slog.Warn("recording dag audit", "action", action, "dag", logSafe(c.Param("dag_id")), "error", logSafe(err.Error()))
	}
}

// setFields joins, sorted, the names whose flag is true: the fields a request
// set, which an audit entry records in place of their values.
func setFields(fields map[string]bool) string {
	names := make([]string, 0, len(fields))
	for name, set := range fields {
		if set {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}
