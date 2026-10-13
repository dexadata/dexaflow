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
// It runs in one transaction under the same row lock as RegisterDagVersion, so
// it is serialized with registers of the DAG, and it applies the target
// version's metadata (schedule, catchup, start_date, max_active_runs,
// description, owner, tags) to the dags row together with current_version_id.
// is_paused is never changed: pausing is an operator decision, not a deploy one.
//
// Before anything is written the target spec must pass what a register of it
// would have to pass today: its declared Variables and Connections, the
// tenant's limits, and admit (the caller's checks, such as the execution
// policy); a refusal is returned as is. A target that is already current
// changes nothing and records nothing (Changed is false). An unknown DAG, an
// unknown version and a DAG of another tenant are domain.ErrNotFound. The
// change is audited as dag.version.set_current by actorUserID, with the from
// and to versions.
func (r *Repository) SetCurrentDagVersion(ctx context.Context, tenant, dagID string, sel domain.DagVersionSelector, actorUserID string, admit func(domain.DAGSpec) error) (domain.CurrentVersionChange, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return domain.CurrentVersionChange{}, err
	}
	check := func(spec domain.DAGSpec) error {
		if verr := r.validateDeclaredSecrets(ctx, tid, spec); verr != nil {
			return verr
		}
		if lerr := checkRegistrationLimits(ctx, r.q, tid, spec); lerr != nil {
			return lerr
		}
		if admit != nil {
			return admit(spec)
		}
		return nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.CurrentVersionChange{}, fmt.Errorf("beginning set-current tx: %w", err)
	}
	// Rollback after a successful commit is a no-op; on any early return it
	// undoes everything this call wrote.
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // best-effort; the commit path returns the meaningful error
	change, err := setCurrentDagVersionTx(ctx, r.q.WithTx(tx), tid, dagID, sel, actorUUID(actorUserID), check)
	if err != nil || !change.Changed {
		return change, err
	}
	if cerr := tx.Commit(ctx); cerr != nil {
		return domain.CurrentVersionChange{}, fmt.Errorf("committing set-current tx: %w", cerr)
	}
	return change, nil
}

// setCurrentDagVersionTx is the body of SetCurrentDagVersion, run on a
// transaction: lock the DAG, resolve the current and target versions, check
// the target, then point the DAG at it and apply its metadata.
func setCurrentDagVersionTx(ctx context.Context, qtx *queries.Queries, tid pgtype.UUID, dagID string, sel domain.DagVersionSelector, actor pgtype.UUID, check func(domain.DAGSpec) error) (domain.CurrentVersionChange, error) {
	dag, err := qtx.LockDagByDagID(ctx, queries.LockDagByDagIDParams{TenantID: tid, DagID: dagID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.CurrentVersionChange{}, domain.Safef(domain.ErrNotFound, "dag %q not found", dagID)
		}
		return domain.CurrentVersionChange{}, fmt.Errorf("locking dag: %w", err)
	}
	rows, err := qtx.ListDagVersions(ctx, queries.ListDagVersionsParams{TenantID: tid, DagID: dagID})
	if err != nil {
		return domain.CurrentVersionChange{}, fmt.Errorf("listing dag versions: %w", err)
	}
	change, target, err := pickCurrentVersionChange(dagID, rows, sel)
	if err != nil || !change.Changed {
		return change, err
	}
	spec, err := versionSpec(ctx, qtx, tid, target)
	if err != nil {
		return domain.CurrentVersionChange{}, err
	}
	if cerr := check(spec); cerr != nil {
		return domain.CurrentVersionChange{}, cerr
	}
	meta := dagMetadataOf(spec)
	meta.ID, meta.TenantID = dag.ID, tid
	if err := qtx.UpdateDagMetadata(ctx, meta); err != nil {
		return domain.CurrentVersionChange{}, fmt.Errorf("updating dag metadata: %w", err)
	}
	if err := qtx.SetCurrentDagVersion(ctx, queries.SetCurrentDagVersionParams{ID: dag.ID, CurrentVersionID: target, TenantID: tid}); err != nil {
		return domain.CurrentVersionChange{}, fmt.Errorf("setting current version: %w", err)
	}
	if err := writeDagAudit(ctx, qtx, tid, actor, "dag.version.set_current", dagID, map[string]any{
		"from_version": change.From.Version, "from_version_number": change.From.VersionNumber,
		"to_version": change.To.Version, "to_version_number": change.To.VersionNumber,
		"image": change.To.ImageReference, "spec_hash": change.To.SpecHash,
	}); err != nil {
		return domain.CurrentVersionChange{}, err
	}
	change.From.IsCurrent, change.To.IsCurrent = false, true
	return change, nil
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
func versionSpec(ctx context.Context, qtx *queries.Queries, tid, id pgtype.UUID) (domain.DAGSpec, error) {
	row, err := qtx.GetDagVersionInTenant(ctx, queries.GetDagVersionInTenantParams{ID: id, TenantID: tid})
	if err != nil {
		return domain.DAGSpec{}, fmt.Errorf("loading dag version: %w", mapNotFound(err))
	}
	var spec domain.DAGSpec
	if err := json.Unmarshal(row.Spec, &spec); err != nil {
		return domain.DAGSpec{}, fmt.Errorf("decoding dag version spec: %w", err)
	}
	return spec, nil
}
