package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// TenantExists reports whether a tenant with this name exists.
//
// It backs the boot-time check on auth.oidc.tenant_claims: a claim value mapped
// to a tenant that does not exist denies every login carrying it, and since the
// only tenant anything creates is "default" (migration 001), a typo there is
// unrecoverable without direct SQL. A lookup failure is returned rather than
// folded into false, so the caller can tell "absent" from "could not ask".
func (r *Repository) TenantExists(ctx context.Context, name string) (bool, error) {
	if _, err := r.tenantID(ctx, name); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// TenantUUID resolves a tenant name to its UUID string, the form the agent
// token carries and that the secret-delivery methods expect.
func (r *Repository) TenantUUID(ctx context.Context, name string) (string, error) {
	tid, err := r.tenantID(ctx, name)
	if err != nil {
		return "", err
	}
	return uuidToString(tid), nil
}

// EnsureTenant creates the tenant name with the built-in roles, their
// permissions and the default pool copied from the "default" tenant, all in
// one transaction (#1283). It is idempotent: for an existing tenant it fills in
// anything missing and reports created=false. A positive defaultPoolSlots sizes
// the tenant's default pool to that many slots, on creation or later; a
// non-positive value leaves an existing pool alone and gives a new one the
// default tenant's size. limits sets the tenant limits it carries and leaves
// the others as they are (a new tenant starts with none).
func (r *Repository) EnsureTenant(ctx context.Context, name, displayName string, defaultPoolSlots int, limits domain.TenantLimitsUpdate) (created bool, err error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("beginning ensure-tenant tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // best-effort; the commit path returns the meaningful error
	qtx := r.q.WithTx(tx)
	n, err := qtx.InsertTenantIfMissing(ctx, queries.InsertTenantIfMissingParams{Name: name, DisplayName: strPtr(displayName)})
	if err != nil {
		return false, fmt.Errorf("inserting tenant: %w", err)
	}
	t, err := qtx.GetTenantByName(ctx, name)
	if err != nil {
		return false, fmt.Errorf("loading tenant: %w", err)
	}
	if err := qtx.CopyDefaultSystemRoles(ctx, t.ID); err != nil {
		return false, fmt.Errorf("seeding roles: %w", err)
	}
	if err := qtx.CopyDefaultRolePermissions(ctx, t.ID); err != nil {
		return false, fmt.Errorf("seeding role permissions: %w", err)
	}
	if err := ensureDefaultPool(ctx, qtx, t.ID, defaultPoolSlots); err != nil {
		return false, err
	}
	if err := applyTenantLimits(ctx, qtx, t.ID, limits); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("committing ensure-tenant tx: %w", err)
	}
	return n > 0, nil
}

// ensureDefaultPool gives the tenant its default pool: sized to slots when
// slots is positive, otherwise copied from the default tenant if missing (a
// non-positive slots means "not given"). The caller bounds slots to the int32
// column; toInt32 saturates rather than wrapping if it ever does not.
func ensureDefaultPool(ctx context.Context, q *queries.Queries, tenantID pgtype.UUID, slots int) error {
	if slots <= 0 {
		if err := q.InsertDefaultPool(ctx, tenantID); err != nil {
			return fmt.Errorf("seeding default pool: %w", err)
		}
		return nil
	}
	if err := q.UpsertDefaultPoolSlots(ctx, queries.UpsertDefaultPoolSlotsParams{
		TenantID: tenantID, Name: domain.DefaultPoolName, Slots: toInt32(slots),
	}); err != nil {
		return fmt.Errorf("sizing default pool: %w", err)
	}
	return nil
}

// TenantRolePermissions lists a tenant's built-in role grants as
// "role:action:resource", so a caller can compare two tenants' ladders.
func (r *Repository) TenantRolePermissions(ctx context.Context, tenant string) ([]string, error) {
	return r.q.ListTenantRolePermissions(ctx, tenant)
}

// TenantHasDefaultPool reports whether the tenant has its default pool.
func (r *Repository) TenantHasDefaultPool(ctx context.Context, tenant string) (bool, error) {
	return r.q.TenantHasDefaultPool(ctx, tenant)
}
