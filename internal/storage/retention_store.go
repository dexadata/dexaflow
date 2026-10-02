package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dexadata/dexaflow/internal/retention"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// RetentionStore is the sqlc-backed implementation of retention.Store.
type RetentionStore struct {
	q    *queries.Queries
	pool poolBeginner
}

var _ retention.Store = (*RetentionStore)(nil)

// NewRetentionStore builds a RetentionStore over the given Postgres connection.
func NewRetentionStore(pg *Postgres) *RetentionStore {
	return &RetentionStore{q: pg.Queries, pool: pg.Pool}
}

// TenantIDs lists every tenant.
func (s *RetentionStore) TenantIDs(ctx context.Context) ([]string, error) {
	ids, err := s.q.ListTenantIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing tenants: %w", err)
	}
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = uuidToString(id)
	}
	return out, nil
}

// DeleteFinishedRuns removes up to maxRuns of the tenant's settled runs that
// ended before cutoff, in one transaction: the runs are locked first, then
// their rows go children first (state and attempt history, XCom index, task
// instances, the runs), each table in statements of at most rowLimit rows. The
// FK cascades find nothing left to do.
func (s *RetentionStore) DeleteFinishedRuns(ctx context.Context, tenant string, cutoff time.Time, maxRuns, rowLimit int) (retention.Counts, error) {
	tid, err := parseUUID(tenant)
	if err != nil {
		return retention.Counts{}, fmt.Errorf("tenant id: %w", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return retention.Counts{}, fmt.Errorf("beginning retention tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // best-effort; the commit path returns the meaningful error
	qtx := s.q.WithTx(tx)
	runs, err := qtx.LockExpiredSettledRuns(ctx, queries.LockExpiredSettledRunsParams{
		TenantID: tid, Cutoff: pgtype.Timestamptz{Time: cutoff, Valid: true}, MaxRuns: int32(maxRuns), //nolint:gosec // bounded by config validation
	})
	if err != nil {
		return retention.Counts{}, fmt.Errorf("selecting expired runs: %w", err)
	}
	if len(runs) == 0 {
		return retention.Counts{}, nil
	}
	limit := int32(rowLimit) //nolint:gosec // bounded by config validation
	var c retention.Counts
	steps := []struct {
		n   *int64
		del func() (int64, error)
	}{
		{&c.TaskStateHistory, func() (int64, error) {
			return qtx.DeleteTaskStateHistoryOfRuns(ctx, queries.DeleteTaskStateHistoryOfRunsParams{TenantID: tid, RunIds: runs, RowLimit: limit})
		}},
		{&c.TaskInstanceHistory, func() (int64, error) {
			return qtx.DeleteTaskInstanceHistoryOfRuns(ctx, queries.DeleteTaskInstanceHistoryOfRunsParams{TenantID: tid, RunIds: runs, RowLimit: limit})
		}},
		{&c.XComIndex, func() (int64, error) {
			return qtx.DeleteXComIndexOfRuns(ctx, queries.DeleteXComIndexOfRunsParams{TenantID: tid, RunIds: runs, RowLimit: limit})
		}},
		{&c.TaskInstances, func() (int64, error) {
			return qtx.DeleteTaskInstancesOfRuns(ctx, queries.DeleteTaskInstancesOfRunsParams{TenantID: tid, RunIds: runs, RowLimit: limit})
		}},
	}
	for _, st := range steps {
		n, derr := drain(st.del, int64(limit))
		if derr != nil {
			return retention.Counts{}, derr
		}
		*st.n = n
	}
	if c.DagRuns, err = qtx.DeleteDagRunsByID(ctx, queries.DeleteDagRunsByIDParams{TenantID: tid, RunIds: runs}); err != nil {
		return retention.Counts{}, fmt.Errorf("deleting runs: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return retention.Counts{}, fmt.Errorf("committing retention tx: %w", err)
	}
	return c, nil
}

// drain repeats a bounded delete until a statement comes back short.
func drain(del func() (int64, error), limit int64) (int64, error) {
	var total int64
	for {
		n, err := del()
		if err != nil {
			return total, fmt.Errorf("deleting run children: %w", err)
		}
		total += n
		if n < limit {
			return total, nil
		}
	}
}

// DeleteAuditLog removes up to limit of the tenant's audit rows older than
// cutoff, oldest first. An empty tenant means the tenant-less system rows.
func (s *RetentionStore) DeleteAuditLog(ctx context.Context, tenant string, cutoff time.Time, limit int) (int64, error) {
	ts := pgtype.Timestamptz{Time: cutoff, Valid: true}
	lim := int32(limit) //nolint:gosec // bounded by config validation
	if tenant == "" {
		n, err := s.q.DeleteSystemAuditLog(ctx, queries.DeleteSystemAuditLogParams{Cutoff: ts, RowLimit: lim})
		if err != nil {
			return 0, fmt.Errorf("deleting system audit rows: %w", err)
		}
		return n, nil
	}
	tid, err := parseUUID(tenant)
	if err != nil {
		return 0, fmt.Errorf("tenant id: %w", err)
	}
	n, err := s.q.DeleteTenantAuditLog(ctx, queries.DeleteTenantAuditLogParams{TenantID: tid, Cutoff: ts, RowLimit: lim})
	if err != nil {
		return 0, fmt.Errorf("deleting audit rows: %w", err)
	}
	return n, nil
}

// CountEligible counts what a cycle would delete, without deleting. A nil
// cutoff leaves that class at zero.
func (s *RetentionStore) CountEligible(ctx context.Context, runCutoff, auditCutoff *time.Time) (retention.Counts, error) {
	var c retention.Counts
	if runCutoff != nil {
		row, err := s.q.CountExpiredSettledRuns(ctx, pgtype.Timestamptz{Time: *runCutoff, Valid: true})
		if err != nil {
			return c, fmt.Errorf("counting expired runs: %w", err)
		}
		c.DagRuns, c.TaskInstances = row.Runs, row.TaskInstances
	}
	if auditCutoff != nil {
		n, err := s.q.CountExpiredAuditLog(ctx, pgtype.Timestamptz{Time: *auditCutoff, Valid: true})
		if err != nil {
			return c, fmt.Errorf("counting expired audit rows: %w", err)
		}
		c.AuditLog = n
	}
	return c, nil
}
