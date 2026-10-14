package storage

import (
	"context"
	"fmt"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// GetDagVersionSpec returns one version of the DAG and the spec registered
// with it: the version numbered versionNumber (the 1-based version_number of
// ListDagVersions), or the newest version when versionNumber is 0. The newest
// version is not necessarily the current one: after a rollback the current
// version is an older one. It returns domain.ErrNotFound when the DAG has no
// such version in the caller's tenant. The spec is shared with the spec cache
// and must not be mutated.
func (r *Repository) GetDagVersionSpec(ctx context.Context, tenant, dagID string, versionNumber int) (domain.DagVersion, domain.DAGSpec, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return domain.DagVersion{}, domain.DAGSpec{}, err
	}
	rows, err := r.q.ListDagVersions(ctx, queries.ListDagVersionsParams{TenantID: tid, DagID: dagID})
	if err != nil {
		return domain.DagVersion{}, domain.DAGSpec{}, fmt.Errorf("listing dag versions: %w", err)
	}
	row, ok := pickVersionByNumber(rows, versionNumber)
	if !ok {
		return domain.DagVersion{}, domain.DAGSpec{}, domain.Safef(domain.ErrNotFound,
			"version %d of dag %q not found", versionNumber, dagID)
	}
	// The id comes from the tenant-scoped listing above, so the unscoped read
	// by id through the shared spec cache stays in the caller's tenant. The
	// returned spec is shared and must not be mutated (see specCache).
	_, spec, err := r.specs.get(ctx, r.q, row.ID)
	if err != nil {
		return domain.DagVersion{}, domain.DAGSpec{}, fmt.Errorf("loading dag version spec: %w", err)
	}
	return dagVersionOf(row), spec, nil
}

// pickVersionByNumber returns the row numbered n, or the newest row when n is
// 0. rows are newest first, as ListDagVersions orders them.
func pickVersionByNumber(rows []queries.ListDagVersionsRow, n int) (queries.ListDagVersionsRow, bool) {
	if n == 0 && len(rows) > 0 {
		return rows[0], true
	}
	for _, row := range rows {
		if int(row.VersionNumber) == n {
			return row, true
		}
	}
	return queries.ListDagVersionsRow{}, false
}
