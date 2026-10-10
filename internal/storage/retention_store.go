package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dexadata/dexaflow/internal/retention"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// RetentionStore is the sqlc-backed implementation of retention.Store.
type RetentionStore struct {
	q    *queries.Queries
	pool poolBeginner
	// dryRunCap bounds the dry-run count per tenant (runs, and audit rows):
	// past it the count is a lower bound (Counts.Capped).
	dryRunCap int
	// afterRunLock is a test seam run right after the expired runs are locked.
	afterRunLock func()
}

// retentionDryRunCap is how many eligible runs and audit rows the dry run
// counts per tenant before it stops: enough to size a first purge, cheap
// enough to run on the largest install.
const retentionDryRunCap = 10000

var _ retention.Store = (*RetentionStore)(nil)

// NewRetentionStore builds a RetentionStore over the given Postgres connection.
func NewRetentionStore(pg *Postgres) *RetentionStore {
	return &RetentionStore{q: pg.Queries, pool: pg.Pool, dryRunCap: retentionDryRunCap}
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
	if s.afterRunLock != nil {
		s.afterRunLock()
	}
	runs, err = stillSettled(ctx, qtx, tid, runs)
	if errors.Is(err, errRetentionRunBusy) {
		// A clear is writing one of these runs right now; leave the batch to a
		// later cycle rather than wait on it (it would deadlock on the run row).
		return retention.Counts{}, nil
	}
	if err != nil {
		return retention.Counts{}, err
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
// errRetentionRunBusy reports that another transaction holds a task instance
// row of the batch (a clear in flight).
var errRetentionRunBusy = errors.New("retention: a run of the batch is being written")

// stillSettled locks every task instance of the locked runs and returns the
// runs whose task instances are all still settled. The run lock does not keep a
// clear out (a clear resets task instances before it touches the run row), so a
// run cleared since it was selected is dropped here, whole: none of its rows is
// deleted. With the task instances locked, no clear can change them until the
// batch commits. A task instance locked by another transaction returns
// errRetentionRunBusy instead of waiting (NOWAIT).
func stillSettled(ctx context.Context, q *queries.Queries, tid pgtype.UUID, runs []pgtype.UUID) ([]pgtype.UUID, error) {
	rows, err := q.LockTaskInstancesOfRuns(ctx, queries.LockTaskInstancesOfRunsParams{TenantID: tid, RunIds: runs})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgLockNotAvailable {
			return nil, errRetentionRunBusy
		}
		return nil, fmt.Errorf("locking task instances of expired runs: %w", err)
	}
	unsettled := map[pgtype.UUID]bool{}
	for _, r := range rows {
		if !r.Settled {
			unsettled[r.DagRunID] = true
		}
	}
	kept := runs[:0]
	for _, id := range runs {
		if !unsettled[id] {
			kept = append(kept, id)
		}
	}
	return kept, nil
}

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

// CountEligible counts what a retention cycle would delete, for the dry run:
// expired settled runs and their task instances when runCutoff is set, audit
// rows when auditCutoff is set (system rows included). Each tenant is counted
// through its own index range and stops at the dry-run cap, so the count's cost
// is bounded per tenant; Capped reports that some tenant reached it and the
// totals are a lower bound.
func (s *RetentionStore) CountEligible(ctx context.Context, runCutoff, auditCutoff *time.Time) (retention.Counts, error) {
	var c retention.Counts
	tenants, err := s.TenantIDs(ctx)
	if err != nil {
		return c, err
	}
	for _, tenant := range tenants {
		got, err := s.CountEligibleForTenant(ctx, tenant, runCutoff, auditCutoff)
		if err != nil {
			return c, err
		}
		c = c.Add(got)
	}
	if auditCutoff != nil {
		n, err := s.q.CountExpiredSystemAuditLog(ctx, queries.CountExpiredSystemAuditLogParams{
			Cutoff: pgtype.Timestamptz{Time: *auditCutoff, Valid: true}, MaxRows: s.dryRunLimit(),
		})
		if err != nil {
			return c, fmt.Errorf("counting expired system audit rows: %w", err)
		}
		c.AuditLog += n
		c.Capped = c.Capped || n >= int64(s.dryRunLimit())
	}
	return c, nil
}

// CountEligibleForTenant is CountEligible for one tenant, each class stopped
// at the dry-run cap.
func (s *RetentionStore) CountEligibleForTenant(ctx context.Context, tenant string, runCutoff, auditCutoff *time.Time) (retention.Counts, error) {
	var c retention.Counts
	tid, err := parseUUID(tenant)
	if err != nil {
		return c, fmt.Errorf("tenant id: %w", err)
	}
	limit := s.dryRunLimit()
	if runCutoff != nil {
		row, err := s.q.CountExpiredSettledRunsOfTenant(ctx, queries.CountExpiredSettledRunsOfTenantParams{
			TenantID: tid, Cutoff: pgtype.Timestamptz{Time: *runCutoff, Valid: true}, MaxRuns: limit,
		})
		if err != nil {
			return c, fmt.Errorf("counting expired runs: %w", err)
		}
		c.DagRuns, c.TaskInstances = row.Runs, row.TaskInstances
		c.Capped = row.Runs >= int64(limit)
	}
	if auditCutoff != nil {
		n, err := s.q.CountExpiredTenantAuditLog(ctx, queries.CountExpiredTenantAuditLogParams{
			TenantID: tid, Cutoff: pgtype.Timestamptz{Time: *auditCutoff, Valid: true}, MaxRows: limit,
		})
		if err != nil {
			return c, fmt.Errorf("counting expired audit rows: %w", err)
		}
		c.AuditLog = n
		c.Capped = c.Capped || n >= int64(limit)
	}
	return c, nil
}

// dryRunLimit is the dry-run cap as the queries take it.
func (s *RetentionStore) dryRunLimit() int32 {
	return int32(max(s.dryRunCap, 1)) //nolint:gosec // a small constant, or a test value
}
