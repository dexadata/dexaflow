package storage

import (
	"context"
	"errors"
	"fmt"
	"math"

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
	limits, _, err := loadTenantState(ctx, q, tid)
	return limits, err
}

// loadTenantState reads a tenant's limits and its suspension from the one row
// that holds both.
func loadTenantState(ctx context.Context, q *queries.Queries, tid pgtype.UUID) (domain.TenantLimits, domain.TenantSuspension, error) {
	row, err := q.GetTenantLimits(ctx, tid)
	if err != nil {
		return domain.TenantLimits{}, domain.TenantSuspension{}, fmt.Errorf("loading tenant limits: %w", mapNotFound(err))
	}
	var suspension domain.TenantSuspension
	if row.SuspendedAt.Valid && row.SuspensionMode != nil {
		suspension = domain.TenantSuspension{
			Mode:   domain.SuspensionMode(*row.SuspensionMode),
			Reason: row.SuspensionReason,
			Since:  row.SuspendedAt.Time,
		}
	}
	return domain.TenantLimits{
		MaxDags:                    int(row.MaxDags),
		MaxRunsPerDay:              int(row.MaxRunsPerDay),
		MinScheduleIntervalSeconds: int(row.MinScheduleIntervalSeconds),
		MaxTaskPoolSlots:           int(row.MaxTaskPoolSlots),
	}, suspension, nil
}

// TenantSuspension returns the tenant's suspension; the zero value means it is
// not suspended.
func (r *Repository) TenantSuspension(ctx context.Context, tenant string) (domain.TenantSuspension, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return domain.TenantSuspension{}, err
	}
	_, suspension, err := loadTenantState(ctx, r.q, tid)
	return suspension, err
}

// SuspendTenant suspends the tenant, or changes the mode or reason of its
// suspension (#2181). From then on no new run is created for it, manual or
// scheduled; its DAGs keep their own paused flag. Under stop it also fails the
// tenant's queued and running task instances; the scheduler repeats that on
// every tick, so an attempt dispatched while this call ran is stopped too. An
// unknown tenant is ErrNotFound and a suspension the engine cannot apply is
// ErrValidation.
func (r *Repository) SuspendTenant(ctx context.Context, tenant string, s domain.TenantSuspension) error {
	if err := s.Validate(); err != nil {
		return err
	}
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return err
	}
	if err := r.q.SuspendTenant(ctx, queries.SuspendTenantParams{TenantID: tid, Mode: string(s.Mode), Reason: s.Reason}); err != nil {
		return fmt.Errorf("suspending tenant: %w", err)
	}
	if s.Mode == domain.SuspensionStop {
		if _, err := r.q.FailStoppedTenantTaskInstances(ctx, tid); err != nil {
			return fmt.Errorf("stopping the tenant's task instances: %w", err)
		}
	}
	return nil
}

// ResumeTenant lifts the tenant's suspension. Resuming a tenant that is not
// suspended does nothing; an unknown tenant is ErrNotFound.
func (r *Repository) ResumeTenant(ctx context.Context, tenant string) error {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return err
	}
	if err := r.q.ResumeTenant(ctx, tid); err != nil {
		return fmt.Errorf("resuming tenant: %w", err)
	}
	return nil
}

// applyTenantLimits stores the limits the update sets and leaves the others. A
// limit below 0 or above 2147483647 (the INTEGER columns) is ErrValidation and
// reaches no query; the service API refuses the same values with a 400 first.
func applyTenantLimits(ctx context.Context, q *queries.Queries, tid pgtype.UUID, u domain.TenantLimitsUpdate) error {
	if u.IsZero() {
		return nil
	}
	for _, l := range []struct {
		name  string
		value *int
	}{
		{"max_dags", u.MaxDags},
		{"max_runs_per_day", u.MaxRunsPerDay},
		{"min_schedule_interval_seconds", u.MinScheduleIntervalSeconds},
		{"max_task_pool_slots", u.MaxTaskPoolSlots},
	} {
		if l.value != nil && (*l.value < 0 || *l.value > math.MaxInt32) {
			return domain.Safef(domain.ErrValidation,
				"tenant limit %s must be from 0 (unlimited) to %d, got %d", l.name, math.MaxInt32, *l.value)
		}
	}
	if err := q.UpdateTenantLimits(ctx, queries.UpdateTenantLimitsParams{
		TenantID:                   tid,
		MaxDags:                    int32Ptr(u.MaxDags),
		MaxRunsPerDay:              int32Ptr(u.MaxRunsPerDay),
		MinScheduleIntervalSeconds: int32Ptr(u.MinScheduleIntervalSeconds),
		MaxTaskPoolSlots:           int32Ptr(u.MaxTaskPoolSlots),
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
// a task whose pool_slots is above max_task_pool_slots, or a DAG the tenant
// does not have yet once it holds max_dags of them. A new
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
	if serr := domain.CheckScheduleInterval(spec, limits.MinScheduleIntervalSeconds); serr != nil {
		return serr
	}
	if terr := domain.CheckTaskPoolSlots(spec, limits.MaxTaskPoolSlots); terr != nil {
		return terr
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

// runCreation is one DAG run to create under the tenant's daily run cap
// (createRunWithinDailyLimit).
type runCreation struct {
	// insert creates the run on q and reports whether it did; false means the
	// run already existed and nothing was written (ON CONFLICT DO NOTHING).
	insert func(q *queries.Queries) (bool, error)
	// exists reports whether the run already exists. It is read only when the
	// cap refuses the charge, so that a run which exists gets its usual
	// answer, existsErr (nil for a scheduled slot, a conflict for a manual
	// run id), with or without headroom left, and nothing is written.
	exists    func(q *queries.Queries) (bool, error)
	existsErr error
}

// createRunWithinDailyLimit charges one run to the tenant's max_runs_per_day
// and then creates it. A suspended tenant is refused first, with
// ErrTenantSuspended and nothing charged or written (#2181). A tenant without the limit runs the insert on q as
// before. A tenant with it runs the charge and the insert in one transaction,
// charge first: the charge locks the tenant row, so concurrent triggers cannot
// both take the last run of the day, and a refusal (zero rows from the
// conditional UPDATE) ends the transaction before anything is written, so the
// retries of a capped tenant leave no dead rows in dag_runs or its indexes.
// An insert that creates nothing (a scheduled slot that already exists) or
// fails rolls the whole transaction back, charge included.
func createRunWithinDailyLimit(ctx context.Context, q *queries.Queries, pool txBeginner, tid pgtype.UUID, run runCreation) error {
	limits, suspension, err := loadTenantState(ctx, q, tid)
	if err != nil {
		return err
	}
	if suspension.Active() {
		return suspension.RefusalError()
	}
	if limits.MaxRunsPerDay <= 0 {
		_, ierr := run.insert(q)
		return ierr
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning run tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // best-effort; the commit path returns the meaningful error
	qtx := q.WithTx(tx)
	n, err := qtx.ReserveTenantDailyRun(ctx, tid)
	if err != nil {
		return fmt.Errorf("charging the daily run limit: %w", err)
	}
	if n == 0 {
		// Nothing has been written. A run that already exists is not a
		// refusal: it gets the answer the insert would have given it.
		exists, eerr := run.exists(qtx)
		if eerr != nil {
			return fmt.Errorf("looking up the run: %w", eerr)
		}
		if exists {
			return run.existsErr
		}
		return domain.Safef(domain.ErrLimitExceeded,
			"the tenant reached its limit max_runs_per_day of %d for today (UTC)", limits.MaxRunsPerDay)
	}
	if created, ierr := run.insert(qtx); ierr != nil || !created {
		return ierr // the deferred rollback gives the charge back
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing run tx: %w", err)
	}
	return nil
}
