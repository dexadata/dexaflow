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

// DeleteFinishedRuns removes, in one transaction, at most rowLimit rows of up
// to maxRuns of the tenant's settled runs that ended before cutoff, run rows
// included. The runs are locked first (which re-checks that they are still
// eligible), then their rows go children first: state and attempt history,
// XCom index, task instances, and last the runs whose children are all gone.
// When the limit runs out inside a run, the transaction commits what it
// removed and the next call picks the run up again, so no transaction grows
// with the size of a DAG. The FK cascades find nothing left to do.
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
	c, err := deleteRunRows(ctx, qtx, tid, runs, int32(rowLimit)) //nolint:gosec // bounded by config validation
	if err != nil {
		return retention.Counts{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return retention.Counts{}, fmt.Errorf("committing retention tx: %w", err)
	}
	return c, nil
}

// deleteRunRows spends a budget of limit rows on the locked runs, one table at
// a time in FK order. A table that used the whole remaining budget may hold
// more rows, so the batch stops there and leaves the rest to the next call.
func deleteRunRows(ctx context.Context, q *queries.Queries, tid pgtype.UUID, runs []pgtype.UUID, limit int32) (retention.Counts, error) {
	var c retention.Counts
	steps := []struct {
		n   *int64
		del func(int32) (int64, error)
	}{
		{&c.TaskStateHistory, func(n int32) (int64, error) {
			return q.DeleteTaskStateHistoryOfRuns(ctx, queries.DeleteTaskStateHistoryOfRunsParams{TenantID: tid, RunIds: runs, RowLimit: n})
		}},
		{&c.TaskInstanceHistory, func(n int32) (int64, error) {
			return q.DeleteTaskInstanceHistoryOfRuns(ctx, queries.DeleteTaskInstanceHistoryOfRunsParams{TenantID: tid, RunIds: runs, RowLimit: n})
		}},
		{&c.XComIndex, func(n int32) (int64, error) {
			return q.DeleteXComIndexOfRuns(ctx, queries.DeleteXComIndexOfRunsParams{TenantID: tid, RunIds: runs, RowLimit: n})
		}},
		{&c.TaskInstances, func(n int32) (int64, error) {
			return q.DeleteTaskInstancesOfRuns(ctx, queries.DeleteTaskInstancesOfRunsParams{TenantID: tid, RunIds: runs, RowLimit: n})
		}},
		{&c.DagRuns, func(n int32) (int64, error) {
			return q.DeleteDagRunsByID(ctx, queries.DeleteDagRunsByIDParams{TenantID: tid, RunIds: runs, RowLimit: n})
		}},
	}
	left := limit
	for _, st := range steps {
		if left <= 0 {
			break
		}
		n, err := st.del(left)
		if err != nil {
			return retention.Counts{}, fmt.Errorf("deleting expired run rows: %w", err)
		}
		*st.n = n
		left -= int32(n) //nolint:gosec // n <= left, an int32
		if left <= 0 {
			break
		}
	}
	return c, nil
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

// RecordRetentionPurge writes an audit entry recording that a retention cycle
// deleted rows audit rows older than cutoff in the tenant's scope; an empty
// tenant is the system rows.
func (s *RetentionStore) RecordRetentionPurge(ctx context.Context, tenant string, rows int64, cutoff time.Time) error {
	var tid pgtype.UUID
	if tenant != "" {
		var err error
		if tid, err = parseUUID(tenant); err != nil {
			return fmt.Errorf("tenant id: %w", err)
		}
	}
	if err := s.q.RecordRetentionPurge(ctx, queries.RecordRetentionPurgeParams{
		TenantID: tid, Rows: rows, Cutoff: pgtype.Timestamptz{Time: cutoff, Valid: true},
	}); err != nil {
		return fmt.Errorf("recording retention purge: %w", err)
	}
	return nil
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
