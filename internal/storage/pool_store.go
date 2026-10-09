package storage

import (
	"context"
	"fmt"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// ListPools returns a page of the tenant's named pools and the total count.
func (r *Repository) ListPools(ctx context.Context, tenant string, limit, offset int) ([]domain.Pool, int, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.q.ListPools(ctx, queries.ListPoolsParams{TenantID: tid, Limit: toInt32(limit), Offset: toInt32(offset)})
	if err != nil {
		return nil, 0, fmt.Errorf("listing pools: %w", err)
	}
	total, err := r.q.CountPools(ctx, tid)
	if err != nil {
		return nil, 0, fmt.Errorf("counting pools: %w", err)
	}
	out := make([]domain.Pool, 0, len(rows))
	for _, p := range rows {
		out = append(out, domain.Pool{
			Name: p.Name, Slots: int(p.Slots), Description: strOrEmpty(p.Description), IsDefault: p.IsDefault,
		})
	}
	return out, int(total), nil
}

// GetPool returns one pool by name, or domain.ErrNotFound.
func (r *Repository) GetPool(ctx context.Context, tenant, name string) (domain.Pool, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return domain.Pool{}, err
	}
	p, err := r.q.GetPool(ctx, queries.GetPoolParams{TenantID: tid, Name: name})
	if err != nil {
		return domain.Pool{}, mapNotFound(err)
	}
	return domain.Pool{
		Name: p.Name, Slots: int(p.Slots), Description: strOrEmpty(p.Description), IsDefault: p.IsDefault,
	}, nil
}

// SetPool creates or updates a pool (its slot cap and description). The
// is_default flag is not writable through this path — only the seed migration
// marks the implicit default pool.
func (r *Repository) SetPool(ctx context.Context, tenant string, p domain.Pool) error {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return err
	}
	if err := r.q.UpsertPool(ctx, queries.UpsertPoolParams{
		TenantID: tid, Name: p.Name, Slots: toInt32(p.Slots), Description: strPtr(p.Description),
	}); err != nil {
		return fmt.Errorf("upserting pool: %w", err)
	}
	return nil
}

// DeletePool removes a pool. It returns domain.ErrNotFound when none matched and
// domain.ErrConflict when the target is the implicit default pool — Airflow
// parity: the fallback pool the gate resolves to must never be deleted.
func (r *Repository) DeletePool(ctx context.Context, tenant, name string) error {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return err
	}
	rows, err := r.q.DeletePool(ctx, queries.DeletePoolParams{TenantID: tid, Name: name})
	if err != nil {
		return fmt.Errorf("deleting pool: %w", err)
	}
	if rows == 0 {
		// The guarded DELETE skips the default pool, so a zero here is either a
		// missing pool or an attempt to delete the default. Disambiguate for a
		// clear status without leaking the guard into the query's row count.
		if p, gerr := r.GetPool(ctx, tenant, name); gerr == nil && p.IsDefault {
			return domain.Safef(domain.ErrConflict, "the default pool cannot be deleted")
		}
		return domain.ErrNotFound
	}
	return nil
}

// PoolSlotUsage returns per-pool occupancy for the tenant, keyed by pool name (a
// task instance with no pool is counted under the implicit default_pool). Each
// state sums its task instances' pool_slots, so the slots match what the
// admission gate charges (ADR 0066, #1499). It feeds the Airflow PoolResponse
// occupancy fields.
func (r *Repository) PoolSlotUsage(ctx context.Context, tenant string) (map[string]domain.PoolUsage, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return nil, err
	}
	rows, err := r.q.PoolSlotUsage(ctx, tid)
	if err != nil {
		return nil, fmt.Errorf("counting pool slot usage: %w", err)
	}
	out := make(map[string]domain.PoolUsage, len(rows))
	for _, row := range rows {
		u := out[row.Pool]
		switch row.State {
		case queries.TaskStateRunning:
			u.Running = int(row.N)
		case queries.TaskStateQueued:
			u.Queued = int(row.N)
		case queries.TaskStateScheduled:
			u.Scheduled = int(row.N)
		case queries.TaskStateDeferred:
			u.Deferred = int(row.N)
		default:
			// The query only selects the four slot-relevant states; any other is
			// ignored defensively rather than mis-attributed to a slot bucket.
			continue
		}
		out[row.Pool] = u
	}
	return out, nil
}
