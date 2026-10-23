package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// RecordTaskActionAudit logs a task-level action (clear, mark state) with the
// acting user and the run/task/try in metadata, so the Audit Log view shows the
// owner and the task columns. Scoped to the DAG (resource_id = dag_id) so it
// appears on the DAG's Audit Log tab.
func (r *Repository) RecordTaskActionAudit(ctx context.Context, tenant, userID, action, dagID, runID, taskID string, tryNumber int) error {
	fields := map[string]any{"run_id": runID}
	if taskID != "" { // run-level events (e.g. trigger) carry no task columns
		fields["task_id"] = taskID
		fields["try_number"] = tryNumber
	}
	return r.recordDagAudit(ctx, tenant, userID, action, dagID, fields)
}

// RecordRunActionAudit logs a run-level action on the DAG's Audit Log tab, as
// RecordTaskActionAudit does, with extra metadata fields next to run_id (for
// example requested_state when a PATCH stored another state, #1766).
func (r *Repository) RecordRunActionAudit(ctx context.Context, tenant, userID, action, dagID, runID string, metadata map[string]string) error {
	fields := map[string]any{"run_id": runID}
	for k, v := range metadata {
		if k != "run_id" {
			fields[k] = v
		}
	}
	return r.recordDagAudit(ctx, tenant, userID, action, dagID, fields)
}

// recordDagAudit writes one audit entry scoped to the DAG with the given
// metadata fields.
func (r *Repository) recordDagAudit(ctx context.Context, tenant, userID, action, dagID string, fields map[string]any) error {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return err
	}
	var uid pgtype.UUID
	if u, perr := parseUUID(userID); perr == nil {
		uid = u
	}
	meta, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("encoding audit metadata: %w", err)
	}
	if err := r.q.CreateAuditLog(ctx, queries.CreateAuditLogParams{
		TenantID: tid, UserID: uid, Action: action,
		ResourceType: strPtr("dag"), ResourceID: strPtr(dagID), Metadata: meta,
	}); err != nil {
		return fmt.Errorf("writing task action audit: %w", err)
	}
	return nil
}

// RecordUserCreatedAudit logs an account creation with the acting admin as the
// owner and the new account's email and granted roles in metadata, scoped to the
// "user" resource so account-management actions are visible in the Audit Log. The
// roles arrive as a single comma-joined string (empty when none were granted).
func (r *Repository) RecordUserCreatedAudit(ctx context.Context, tenant, actorUserID, createdUserID, email, roles string) error {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return err
	}
	var uid pgtype.UUID
	if u, perr := parseUUID(actorUserID); perr == nil {
		uid = u
	}
	fields := map[string]any{"email": email}
	if roles != "" {
		fields["roles"] = roles
	}
	meta, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("encoding audit metadata: %w", err)
	}
	if err := r.q.CreateAuditLog(ctx, queries.CreateAuditLogParams{
		TenantID: tid, UserID: uid, Action: "user.create",
		ResourceType: strPtr("user"), ResourceID: strPtr(createdUserID), Metadata: meta,
	}); err != nil {
		return fmt.Errorf("writing user-created audit: %w", err)
	}
	return nil
}

// RecordAuthEvent records an authentication event to the audit log (H5): OIDC
// login success/failure, tenant-pin rejection, JIT provisioning, break-glass
// login, and logout. It NEVER records tokens or the client secret, only the
// actor's email, the resolved tenant (best-effort), the outcome, and small
// non-secret detail fields (e.g. the rejection reason, the attempted tenant
// claim). It is best-effort: the caller logs and continues on error so a flaky
// audit sink never turns a security decision (a 403 rejection, a successful
// login) into a 5xx.
//
// The event is scoped to the resolved tenant when known; events that never
// resolved a tenant (a login failure, a tenant-pin rejection) fall back to the
// "default" tenant so the row still lands, with the attempted values in the
// metadata. resourceID carries the email so account-scoped auth activity is
// filterable alongside user.create.
func (r *Repository) RecordAuthEvent(ctx context.Context, tenant, actorUserID, action, email, outcome string, extra map[string]string) error {
	if tenant == "" {
		tenant = "default"
	}
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		// The resolved tenant may not exist (an attacker-supplied claim); fall back
		// to default so the security event is never silently dropped.
		tid, err = r.tenantID(ctx, "default")
		if err != nil {
			return fmt.Errorf("resolving audit tenant: %w", err)
		}
	}
	var uid pgtype.UUID
	if u, perr := parseUUID(actorUserID); perr == nil {
		uid = u
	}
	fields := map[string]any{"outcome": outcome}
	if email != "" {
		fields["email"] = email
	}
	for k, v := range extra {
		fields[k] = v
	}
	meta, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("encoding audit metadata: %w", err)
	}
	var resourceID *string
	if email != "" {
		resourceID = strPtr(email)
	}
	if err := r.q.CreateAuditLog(ctx, queries.CreateAuditLogParams{
		TenantID: tid, UserID: uid, Action: action,
		ResourceType: strPtr("auth"), ResourceID: resourceID, Metadata: meta,
	}); err != nil {
		return fmt.Errorf("writing auth-event audit: %w", err)
	}
	return nil
}

// RecordSecretScopeWarning records that a task received the full tenant secret
// set while it declared only a narrower subset (ADR 0045, ADR 0055): under
// secret_scoping: enforce it would receive only its declared set. It is scoped to
// the DAG resource so the event surfaces on the DAG's Audit Log tab, with the
// kind ("variables" or "connections"), the run and task, and the declared/total
// counts in metadata. It records counts only, never secret names or values.
//
// tenantID is the tenant UUID the agent token carries (not the tenant name): the
// caller is the agent RPC path, which passes AgentIdentity.TenantID. Resolving it
// by name silently dropped every row (#722), so mirror the secret-delivery path
// and parse it as a UUID.
func (r *Repository) RecordSecretScopeWarning(ctx context.Context, tenantID, dagID, runID, taskID, kind string, declared, total int) error {
	tid, err := parseUUID(tenantID)
	if err != nil {
		return err
	}
	meta, err := json.Marshal(map[string]any{
		"kind": kind, "run_id": runID, "task_id": taskID,
		"declared": declared, "total": total,
	})
	if err != nil {
		return fmt.Errorf("encoding audit metadata: %w", err)
	}
	if err := r.q.CreateAuditLog(ctx, queries.CreateAuditLogParams{
		TenantID: tid, Action: "secret.scope_warning",
		ResourceType: strPtr("dag"), ResourceID: strPtr(dagID), Metadata: meta,
	}); err != nil {
		return fmt.Errorf("writing secret-scope warning audit: %w", err)
	}
	return nil
}

// RecordSecretLivenessDenial records that the secret-path liveness gate fired
// for a task instance whose attempt is no longer live (ADR 0055): a
// would-have-denied in observe mode, or a denial in enforce mode. It is scoped
// to the DAG resource so the event surfaces on the DAG's Audit Log tab, with the
// kind ("variables" or "connections"), the run, task, attempt, and the gate mode
// in metadata. It records identity + kind + mode only, never secret names or
// values.
//
// tenantID is the tenant UUID the agent token carries (not the tenant name): the
// caller is the agent RPC path, which passes AgentIdentity.TenantID. Resolving it
// by name silently dropped every row (#722), including enforce-mode security
// denials, so mirror the secret-delivery path and parse it as a UUID.
func (r *Repository) RecordSecretLivenessDenial(ctx context.Context, tenantID, dagID, runID, taskID string, tryNumber int, kind, mode string) error {
	tid, err := parseUUID(tenantID)
	if err != nil {
		return err
	}
	meta, err := json.Marshal(map[string]any{
		"kind": kind, "run_id": runID, "task_id": taskID,
		"try_number": tryNumber, "mode": mode,
	})
	if err != nil {
		return fmt.Errorf("encoding audit metadata: %w", err)
	}
	// Distinct actions per mode so an operator can tell an observe-phase
	// would-have-denied from an enforce-phase denial at a glance.
	action := "secret.liveness_would_deny"
	if mode == "enforce" {
		action = "secret.liveness_denied"
	}
	if err := r.q.CreateAuditLog(ctx, queries.CreateAuditLogParams{
		TenantID: tid, Action: action,
		ResourceType: strPtr("dag"), ResourceID: strPtr(dagID), Metadata: meta,
	}); err != nil {
		return fmt.Errorf("writing secret-path liveness audit: %w", err)
	}
	return nil
}

// ListAuditLogs returns a page of audit-log entries for the tenant, newest
// first, optionally filtered to a single DAG (dagID == "" means no filter).
func (r *Repository) ListAuditLogs(ctx context.Context, tenant, dagID string, limit, offset int) ([]domain.AuditLogEntry, int, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return nil, 0, err
	}
	var dagFilter *string
	if dagID != "" {
		dagFilter = &dagID
	}
	rows, err := r.q.ListAuditLogs(ctx, queries.ListAuditLogsParams{
		TenantID: tid, Limit: toInt32(limit), Offset: toInt32(offset), DagID: dagFilter,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("listing audit logs: %w", err)
	}
	total, err := r.q.CountAuditLogs(ctx, queries.CountAuditLogsParams{TenantID: tid, DagID: dagFilter})
	if err != nil {
		return nil, 0, fmt.Errorf("counting audit logs: %w", err)
	}
	out := make([]domain.AuditLogEntry, 0, len(rows))
	for _, row := range rows {
		out = append(out, mapAuditLogEntry(row))
	}
	return out, int(total), nil
}

func mapAuditLogEntry(row queries.ListAuditLogsRow) domain.AuditLogEntry {
	return domain.AuditLogEntry{
		ID:           row.ID,
		When:         timeVal(row.OccurredAt),
		Action:       row.Action,
		ResourceType: strOrEmpty(row.ResourceType),
		ResourceID:   strOrEmpty(row.ResourceID),
		Owner:        row.Owner,
		Extra:        string(row.Metadata),
	}
}

// ListAuditLogsAfter returns up to limit of the tenant's audit entries
// strictly before the cursor, newest first, optionally filtered to one DAG, and
// the number of entries matching the filter. It is the keyset form of
// ListAuditLogs; the cursor key is the entry id.
func (r *Repository) ListAuditLogsAfter(ctx context.Context, tenant, dagID string, after domain.PageCursor, limit int) ([]domain.AuditLogEntry, int, error) {
	afterID, err := strconv.ParseInt(after.Key, 10, 64)
	if err != nil {
		return nil, 0, domain.Safef(domain.ErrValidation, "invalid audit log cursor")
	}
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return nil, 0, err
	}
	var dagFilter *string
	if dagID != "" {
		dagFilter = &dagID
	}
	rows, err := r.q.ListAuditLogsAfter(ctx, queries.ListAuditLogsAfterParams{
		TenantID: tid, DagID: dagFilter, AfterOccurredAt: pgtype.Timestamptz{Time: after.At, Valid: true},
		AfterID: afterID, RowLimit: toInt32(limit),
	})
	if err != nil {
		return nil, 0, fmt.Errorf("listing audit logs: %w", err)
	}
	total, err := r.q.CountAuditLogs(ctx, queries.CountAuditLogsParams{TenantID: tid, DagID: dagFilter})
	if err != nil {
		return nil, 0, fmt.Errorf("counting audit logs: %w", err)
	}
	out := make([]domain.AuditLogEntry, 0, len(rows))
	for _, row := range rows {
		out = append(out, mapAuditLogEntry(queries.ListAuditLogsRow(row)))
	}
	return out, int(total), nil
}
