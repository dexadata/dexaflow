package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// SetCurrentDagVersion makes a registered version of dagID its current version
// again: the rollback (or roll forward) of #1702. New runs are created with
// that version from the next scheduler tick; runs that already exist keep the
// version they were created with (dag_runs.dag_version_id is never rewritten).
//
// The target spec must first pass what a register of it would have to pass
// today: its declared Variables and Connections, the tenant's limits, and
// admit (the caller's checks, such as the execution policy); a refusal is
// returned as is. Like RegisterDagVersion, these checks run before the
// transaction, on reads of their own, so the transaction never waits for a
// second connection while it holds one and the DAG's row lock.
//
// The write runs in one transaction under the same row lock as
// RegisterDagVersion, so it is serialized with registers of the DAG. Under
// the lock the target is resolved again and must be the version that was
// checked (else domain.ErrConflict); then the target version's metadata
// (schedule, catchup, start_date, max_active_runs, description, owner, tags) is
// applied to the dags row together with current_version_id. is_paused is never
// changed: pausing is an operator decision, not a deploy one.
//
// A target that is already current changes nothing and records nothing
// (Changed is false). An unknown DAG, an unknown version and a DAG of another
// tenant are domain.ErrNotFound. The change is audited as
// dag.version.set_current by actorUserID, with the from and to versions.
func (r *Repository) SetCurrentDagVersion(ctx context.Context, tenant, dagID string, sel domain.DagVersionSelector, actorUserID string, admit func(domain.DAGSpec) error) (domain.CurrentVersionChange, error) {
	return r.SetCurrentDagVersionAs(ctx, tenant, domain.DagSetCurrent{DagID: dagID, Selector: sel, ActorUserID: actorUserID, Admit: admit})
}

// SetCurrentDagVersionAs is SetCurrentDagVersion for req, whose
// RequireDigest, while warn or enforce, the audit entry records with whether
// the target's image is pinned by digest (#1703).
func (r *Repository) SetCurrentDagVersionAs(ctx context.Context, tenant string, req domain.DagSetCurrent) (domain.CurrentVersionChange, error) {
	dagID, sel, admit := req.DagID, req.Selector, req.Admit
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return domain.CurrentVersionChange{}, err
	}
	checked, err := r.checkSetCurrentTarget(ctx, tid, dagID, sel, admit)
	if err != nil || !checked.Changed {
		return checked, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.CurrentVersionChange{}, fmt.Errorf("beginning set-current tx: %w", err)
	}
	// Rollback after a successful commit is a no-op; on any early return it
	// undoes everything this call wrote.
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // best-effort; the commit path returns the meaningful error
	change, err := setCurrentDagVersionTx(ctx, r.q.WithTx(tx), tid, req, checked.To)
	if err != nil || !change.Changed {
		return change, err
	}
	if cerr := tx.Commit(ctx); cerr != nil {
		return domain.CurrentVersionChange{}, fmt.Errorf("committing set-current tx: %w", cerr)
	}
	return change, nil
}

// checkSetCurrentTarget resolves the version sel names, without a lock, and
// runs the register checks on its spec. A target that is already current is
// returned unchanged and unchecked: nothing will be written.
func (r *Repository) checkSetCurrentTarget(ctx context.Context, tid pgtype.UUID, dagID string, sel domain.DagVersionSelector, admit func(domain.DAGSpec) error) (domain.CurrentVersionChange, error) {
	change, target, err := resolveCurrentVersionChange(ctx, r.q, tid, dagID, sel)
	if err != nil || !change.Changed {
		return change, err
	}
	spec, err := versionSpec(ctx, r.q, tid, target)
	if err != nil {
		return domain.CurrentVersionChange{}, err
	}
	if verr := r.validateDeclaredSecrets(ctx, tid, spec); verr != nil {
		return domain.CurrentVersionChange{}, verr
	}
	if lerr := checkRegistrationLimits(ctx, r.q, tid, spec); lerr != nil {
		return domain.CurrentVersionChange{}, lerr
	}
	if admit != nil {
		if aerr := admit(spec); aerr != nil {
			return domain.CurrentVersionChange{}, aerr
		}
	}
	return change, nil
}

// setCurrentDagVersionTx is the write of SetCurrentDagVersion, run on a
// transaction: lock the DAG, resolve the versions again, confirm the target is
// the checked one, then point the DAG at it and apply its metadata.
func setCurrentDagVersionTx(ctx context.Context, qtx *queries.Queries, tid pgtype.UUID, req domain.DagSetCurrent, checked domain.DagVersion) (domain.CurrentVersionChange, error) {
	dagID, sel := req.DagID, req.Selector
	dag, err := qtx.LockDagByDagID(ctx, queries.LockDagByDagIDParams{TenantID: tid, DagID: dagID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.CurrentVersionChange{}, domain.Safef(domain.ErrNotFound, "dag %q not found", dagID)
		}
		return domain.CurrentVersionChange{}, fmt.Errorf("locking dag: %w", err)
	}
	change, target, err := resolveCurrentVersionChange(ctx, qtx, tid, dagID, sel)
	if err != nil {
		return domain.CurrentVersionChange{}, err
	}
	if change.To.ID != checked.ID || change.To.SpecHash != checked.SpecHash {
		return domain.CurrentVersionChange{}, domain.Safef(domain.ErrConflict,
			"the versions of dag %q changed while the rollback was checked; run it again", dagID)
	}
	if !change.Changed {
		return change, nil
	}
	spec, err := versionSpec(ctx, qtx, tid, target)
	if err != nil {
		return domain.CurrentVersionChange{}, err
	}
	meta := dagMetadataOf(spec)
	meta.ID, meta.TenantID = dag.ID, tid
	if err := qtx.UpdateDagMetadata(ctx, meta); err != nil {
		return domain.CurrentVersionChange{}, fmt.Errorf("updating dag metadata: %w", err)
	}
	if err := qtx.SetCurrentDagVersion(ctx, queries.SetCurrentDagVersionParams{ID: dag.ID, CurrentVersionID: target, TenantID: tid}); err != nil {
		return domain.CurrentVersionChange{}, fmt.Errorf("setting current version: %w", err)
	}
	if err := auditSetCurrent(ctx, qtx, tid, actorUUID(req.ActorUserID), dagID, change, req.RequireDigest); err != nil {
		return domain.CurrentVersionChange{}, err
	}
	change.From.IsCurrent, change.To.IsCurrent = false, true
	return change, nil
}

// auditSetCurrent records change as dag.version.set_current by actor (a user
// of the tenant, else no user), with the deploy.require_digest it was
// admitted under while that is warn or enforce.
func auditSetCurrent(ctx context.Context, qtx *queries.Queries, tid, actor pgtype.UUID, dagID string, change domain.CurrentVersionChange, requireDigest domain.DigestRequirement) error {
	user, err := tenantActor(ctx, qtx, tid, actor)
	if err != nil {
		return err
	}
	fields := map[string]any{
		"from_version": change.From.Version, "from_version_number": change.From.VersionNumber,
		"to_version": change.To.Version, "to_version_number": change.To.VersionNumber,
		"image": change.To.ImageReference, "spec_hash": change.To.SpecHash,
	}
	if requireDigest.Active() {
		fields["require_digest"] = string(requireDigest)
		fields["image_pinned"] = domain.IsDigestPinned(change.To.ImageReference)
	}
	return writeDagAudit(ctx, qtx, tid, user, "dag.version.set_current", dagID, fields)
}

// resolveCurrentVersionChange lists the DAG's versions with q and picks the
// current one and the one sel names. A DAG with no version (or no DAG) is
// domain.ErrNotFound.
func resolveCurrentVersionChange(ctx context.Context, q *queries.Queries, tid pgtype.UUID, dagID string, sel domain.DagVersionSelector) (domain.CurrentVersionChange, pgtype.UUID, error) {
	rows, err := q.ListDagVersions(ctx, queries.ListDagVersionsParams{TenantID: tid, DagID: dagID})
	if err != nil {
		return domain.CurrentVersionChange{}, pgtype.UUID{}, fmt.Errorf("listing dag versions: %w", err)
	}
	if len(rows) == 0 {
		return domain.CurrentVersionChange{}, pgtype.UUID{}, domain.Safef(domain.ErrNotFound, "dag %q not found", dagID)
	}
	return pickCurrentVersionChange(dagID, rows, sel)
}

// pickCurrentVersionChange finds, in a DAG's listed versions, the current one
// (From, zero when none is) and the one sel names (To, whose id it returns).
// A selector that names no version is domain.ErrNotFound.
func pickCurrentVersionChange(dagID string, rows []queries.ListDagVersionsRow, sel domain.DagVersionSelector) (domain.CurrentVersionChange, pgtype.UUID, error) {
	var change domain.CurrentVersionChange
	var target pgtype.UUID
	found := false
	for _, row := range rows {
		if row.IsCurrent {
			change.From = dagVersionOf(row)
		}
		if (sel.Version != "" && row.Version == sel.Version) || (sel.Version == "" && int(row.VersionNumber) == sel.Number) {
			change.To, target, found = dagVersionOf(row), row.ID, true
		}
	}
	if !found {
		if sel.Version != "" {
			return domain.CurrentVersionChange{}, target, domain.Safef(domain.ErrNotFound, "dag %q has no version %q", dagID, sel.Version)
		}
		return domain.CurrentVersionChange{}, target, domain.Safef(domain.ErrNotFound, "dag %q has no version number %d", dagID, sel.Number)
	}
	change.Changed = !change.To.IsCurrent
	return change, target, nil
}

// versionSpec reads and decodes the spec of version id, in tenant tid.
func versionSpec(ctx context.Context, q *queries.Queries, tid, id pgtype.UUID) (domain.DAGSpec, error) {
	row, err := q.GetDagVersionInTenant(ctx, queries.GetDagVersionInTenantParams{ID: id, TenantID: tid})
	if err != nil {
		return domain.DAGSpec{}, fmt.Errorf("loading dag version: %w", mapNotFound(err))
	}
	var spec domain.DAGSpec
	if err := json.Unmarshal(row.Spec, &spec); err != nil {
		return domain.DAGSpec{}, fmt.Errorf("decoding dag version spec: %w", err)
	}
	return spec, nil
}
