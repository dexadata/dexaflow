package scheduler

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
)

// advanceSafely advances one run, isolating it: a panic or error in a single run
// is recovered, logged, and metered, but never aborts the tick. This keeps one
// poison run (a malformed spec, a panicking dispatcher, a transient per-run DB
// error) from stalling every other run or crashing the process, the scheduler
// may fall behind on that run, but it stays alive and keeps the rest moving. It
// returns how many tasks it admitted to queued (zero on a panic or error), the
// per-pool breakdown of those admissions (nil unless the pool gate is on),
// which the caller folds into the per-DAG max_active_tasks and per-pool budgets
// for sibling runs, and the tasks held only by their pool (ADR 0066 §4).
func (s *Scheduler) advanceSafely(ctx context.Context, run RunState) (res advanced) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("scheduler run panic recovered",
				"run", run.RunID, "dag", run.DagID, "panic", r, "stack", string(debug.Stack()))
			s.record("panic")
			res = advanced{}
		}
	}()
	res, err := s.advance(ctx, run)
	if err != nil {
		s.logger.Error("advancing run", "run", run.RunID, "dag", run.DagID, "error", err)
		s.record("run_error")
		return advanced{}
	}
	return res
}

// createDueRuns creates a new run for each scheduled DAG whose next cron slot
// after its latest run has arrived. activeByDAG carries the count of already-
// active runs per DAG so the per-DAG max_active_runs cap is honored (#200): a
// DAG that has reached its cap is skipped this tick, and a backfill that would
// exceed the cap is truncated to the remaining headroom. The local
// `createdThisTick` map folds creations made in this same tick into the cap
// so a single tick cannot itself breach the limit.
func (s *Scheduler) createDueRuns(ctx context.Context, activeByDAG map[dagRef]int) error {
	dags, err := s.store.ScheduledDAGs(ctx)
	if err != nil {
		return fmt.Errorf("listing scheduled dags: %w", err)
	}
	now := time.Now().UTC()
	createdThisTick := make(map[dagRef]int, len(dags))
	// capped holds the tenants whose daily run limit or suspension refused a
	// run this tick; their other due slots wait for a later tick instead of
	// each asking again.
	capped := map[string]bool{}
	for _, d := range dags {
		key := dagRef{d.TenantID, d.DagID}
		if capped[d.TenantID] {
			continue
		}
		if domain.IsOnceSchedule(d.Schedule) {
			// @once: fire exactly one run on first sight, then never again. Once
			// the run exists, the DAG's LastLogical is non-nil and this is
			// skipped, that single-shot semantic already prevents any cap
			// breach, so no headroom check is needed here.
			if d.LastLogical == nil && !s.createScheduledRun(ctx, d, now, capped) {
				createdThisTick[key]++
			}
			continue
		}
		if domain.IsCronlessSchedule(d.Schedule) {
			// Manual-only or @continuous: nothing to cron-schedule, skip quietly.
			continue
		}
		// A non-empty but unparseable schedule (a 4-field cron, a typo) would
		// otherwise be swallowed silently: nextScheduledRun returns "not due" and
		// the DAG never runs, with nothing logged. Surface it (once per bad
		// expression) so the operator sees why a scheduled DAG sits idle. Compile
		// validation (domain.ValidateSchedule) catches this earlier; this is the
		// backstop for DAGs registered before the fix.
		if !scheduleParseable(d.Schedule) {
			if s.warnedSchedules[key] != d.Schedule {
				s.logger.Warn("DAG has an unparseable cron schedule; it will not run on a schedule until fixed",
					"tenant", d.TenantID, "dag", d.DagID, "schedule", d.Schedule)
				s.warnedSchedules[key] = d.Schedule
			}
			continue
		}
		for _, logical := range dueSlots(d, now) {
			if !s.hasHeadroom(d, key, activeByDAG, createdThisTick) {
				s.recordCapSkip(d)
				break
			}
			if s.createScheduledRun(ctx, d, logical, capped) {
				break
			}
			createdThisTick[key]++
		}
	}
	return nil
}

// dueSlots returns the logical dates of a cron DAG's runs that are due now, in
// order. First-run with no start_date keeps the legacy single-slot semantics
// (most recent slot at or before now): backfilling unbounded history for a
// fresh DAG would be unsafe by default. The catchup helper opts in only when
// there is either a last_logical or a start_date floor.
func dueSlots(d ScheduledDAG, now time.Time) []time.Time {
	if d.LastLogical == nil && d.StartDate == nil {
		if logical, due := nextScheduledRun(d.Schedule, d.LastLogical, now); due {
			return []time.Time{logical}
		}
		return nil
	}
	return dueScheduledSlots(d.Schedule, d.LastLogical, d.StartDate, now, d.Catchup, maxCatchupSlotsPerTick)
}

// hasHeadroom reports whether the DAG may take another active run without
// exceeding its max_active_runs cap. A non-positive cap is treated as
// "unlimited", a defensive guard for hand-edited rows: the column is
// `NOT NULL DEFAULT 16` (migration 002) and the repository upsert defaults
// missing values to 16 (`repository.go:509`), so a zero only appears via a
// direct DB write. Diverging from Airflow's "missing == default 16" makes
// the scheduler fail open rather than locking the DAG out forever when a
// bad row is encountered. Callers pass `justCreated` so a single tick
// folds in its own creations and cannot itself breach the cap.
func (s *Scheduler) hasHeadroom(d ScheduledDAG, key dagRef, active, justCreated map[dagRef]int) bool {
	if d.MaxActiveRuns <= 0 {
		return true
	}
	return active[key]+justCreated[key] < d.MaxActiveRuns
}

// recordCapSkip logs (once per DAG between successful creations) and meters
// that a due slot was skipped because the DAG is at its max_active_runs cap.
// We use a single metric label so dashboards can see "is concurrency the
// bottleneck right now?" without per-DAG cardinality.
func (s *Scheduler) recordCapSkip(d ScheduledDAG) {
	s.logger.Debug("skipping due run; DAG is at max_active_runs cap", "tenant", d.TenantID, "dag", d.DagID)
	s.record("max_active_runs_cap")
}

// createScheduledRun creates one scheduled run for a DAG, isolating per-DAG
// failures: a single DAG's creation error is logged and metered but never blocks
// run creation for the other scheduled DAGs in this tick.
//
// It reports whether the tenant's daily run limit (max_runs_per_day) refused
// the run. That is not an error: the slot is skipped, the tenant is added to
// capped so the rest of the tick leaves it alone, and the skip is metered and
// logged once per tenant and UTC day. The slot stays due, so it is created on
// a later tick once the UTC day turns (catchup decides whether the slots
// missed in between are created too).
//
// A tenant suspension is handled the same way. The scheduled DAG list already
// leaves suspended tenants out, so the store only refuses a run as suspended
// when the suspension lands between the list and the insert; the warning is
// logged once per tenant and reason, and the slot is created by the first
// tick after the suspension is lifted.
func (s *Scheduler) createScheduledRun(ctx context.Context, d ScheduledDAG, logical time.Time, capped map[string]bool) bool {
	err := s.store.CreateScheduledRun(ctx, d.TenantID, d.DagID, logical)
	switch {
	case err == nil:
		s.record("create_run")
		delete(s.warnedSuspensions, d.TenantID)
	case errors.Is(err, domain.ErrTenantSuspended):
		capped[d.TenantID] = true
		s.record("tenant_suspended")
		if reason := err.Error(); s.warnedSuspensions[d.TenantID] != reason {
			s.logger.Warn("skipping scheduled runs: the tenant is suspended",
				"tenant", d.TenantID, "dag", d.DagID, "logical_date", logical, "reason", reason)
			s.warnedSuspensions[d.TenantID] = reason
		}
		return true
	case errors.Is(err, domain.ErrLimitExceeded):
		capped[d.TenantID] = true
		s.record("tenant_daily_run_cap")
		if day := time.Now().UTC().Format(time.DateOnly); s.warnedRunCaps[d.TenantID] != day {
			s.logger.Warn("skipping scheduled runs: the tenant reached its daily run limit",
				"tenant", d.TenantID, "dag", d.DagID, "logical_date", logical, "reason", err)
			s.warnedRunCaps[d.TenantID] = day
		}
		return true
	default:
		s.logger.Error("creating scheduled run", "tenant", d.TenantID, "dag", d.DagID, "error", err)
		s.record("create_run_error")
	}
	return false
}
