package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// TenantLimits returns the limits the operator set for tenant; a zero field is
// unlimited.
func (r *Repository) TenantLimits(ctx context.Context, tenant string) (domain.TenantLimits, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return domain.TenantLimits{}, err
	}
	return loadTenantLimits(ctx, r.q, tid)
}

func loadTenantLimits(ctx context.Context, q *queries.Queries, tid pgtype.UUID) (domain.TenantLimits, error) {
	row, err := q.GetTenantLimits(ctx, tid)
	if err != nil {
		return domain.TenantLimits{}, fmt.Errorf("loading tenant limits: %w", mapNotFound(err))
	}
	return domain.TenantLimits{
		MaxDags:                    int(row.MaxDags),
		MaxRunsPerDay:              int(row.MaxRunsPerDay),
		MinScheduleIntervalSeconds: int(row.MinScheduleIntervalSeconds),
	}, nil
}

// applyTenantLimits stores the limits the update sets and leaves the others.
func applyTenantLimits(ctx context.Context, q *queries.Queries, tid pgtype.UUID, u domain.TenantLimitsUpdate) error {
	if u.IsZero() {
		return nil
	}
	if err := q.UpdateTenantLimits(ctx, queries.UpdateTenantLimitsParams{
		TenantID:                   tid,
		MaxDags:                    int32Ptr(u.MaxDags),
		MaxRunsPerDay:              int32Ptr(u.MaxRunsPerDay),
		MinScheduleIntervalSeconds: int32Ptr(u.MinScheduleIntervalSeconds),
	}); err != nil {
		return fmt.Errorf("setting tenant limits: %w", err)
	}
	return nil
}

func int32Ptr(n *int) *int32 {
	if n == nil {
		return nil
	}
	v := toInt32(*n)
	return &v
}

// checkRegistrationLimits refuses a DAG version the tenant's limits do not
// allow: a schedule that fires more often than min_schedule_interval_seconds,
// or a DAG the tenant does not have yet once it holds max_dags of them. A new
// version of a DAG the tenant already has never counts against max_dags.
//
// The DAG count is read, not locked, like the max_active_runs check in
// CreateDagRun: registrations of different new DAGs racing at the cap can
// overshoot it by the number of concurrent writers.
func checkRegistrationLimits(ctx context.Context, q *queries.Queries, tid pgtype.UUID, spec domain.DAGSpec) error {
	limits, err := loadTenantLimits(ctx, q, tid)
	if err != nil {
		return err
	}
	if serr := checkScheduleInterval(spec, limits.MinScheduleIntervalSeconds); serr != nil {
		return serr
	}
	if limits.MaxDags <= 0 {
		return nil
	}
	if _, gerr := q.GetDagByDagID(ctx, queries.GetDagByDagIDParams{TenantID: tid, DagID: spec.DagID}); gerr == nil {
		return nil
	} else if !errors.Is(gerr, pgx.ErrNoRows) {
		return fmt.Errorf("looking up dag: %w", gerr)
	}
	n, err := q.CountTenantDags(ctx, tid)
	if err != nil {
		return fmt.Errorf("counting dags: %w", err)
	}
	if n >= int64(limits.MaxDags) {
		return domain.Safef(domain.ErrLimitExceeded,
			"dag %q cannot be registered: the tenant has %d DAGs and its limit max_dags of %d is reached", spec.DagID, n, limits.MaxDags)
	}
	return nil
}

// checkScheduleInterval refuses a schedule whose shortest gap between two runs
// is below minSeconds. A schedule that never fires on a cron passes.
func checkScheduleInterval(spec domain.DAGSpec, minSeconds int) error {
	if minSeconds <= 0 || spec.Schedule == nil {
		return nil
	}
	gap, ok := domain.MinScheduleInterval(*spec.Schedule)
	if !ok {
		return nil
	}
	if limit := time.Duration(minSeconds) * time.Second; gap < limit {
		return domain.Safef(domain.ErrLimitExceeded,
			"dag %q cannot be registered: schedule %q runs as often as every %s, below the tenant limit min_schedule_interval_seconds of %d (%s)",
			spec.DagID, *spec.Schedule, gap, minSeconds, limit)
	}
	return nil
}

// createRunWithinDailyLimit runs insert, which creates one DAG run and reports
// whether it did, and charges that run to the tenant's max_runs_per_day. A
// tenant without the limit runs insert on q as before. A tenant with it runs
// insert and the charge in one transaction: the charge locks the tenant row,
// so concurrent triggers cannot both take the last run of the day, and a
// refused charge rolls the run back. A run insert declined (a slot that
// already exists) charges nothing.
func createRunWithinDailyLimit(ctx context.Context, q *queries.Queries, pool txBeginner, tid pgtype.UUID,
	insert func(*queries.Queries) (bool, error),
) error {
	limits, err := loadTenantLimits(ctx, q, tid)
	if err != nil {
		return err
	}
	if limits.MaxRunsPerDay <= 0 {
		_, ierr := insert(q)
		return ierr
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning run tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // best-effort; the commit path returns the meaningful error
	qtx := q.WithTx(tx)
	created, err := insert(qtx)
	if err != nil || !created {
		return err
	}
	n, err := qtx.ReserveTenantDailyRun(ctx, tid)
	if err != nil {
		return fmt.Errorf("charging the daily run limit: %w", err)
	}
	if n == 0 {
		return domain.Safef(domain.ErrLimitExceeded,
			"the tenant reached its limit max_runs_per_day of %d for today (UTC)", limits.MaxRunsPerDay)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing run tx: %w", err)
	}
	return nil
}
