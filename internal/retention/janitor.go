// Package retention deletes metadata rows past an operator-configured age. It
// is opt-in per data class and runs on the scheduler leader only: every class
// is off by default, so a default install never deletes a row. Deletes are
// tenant scoped, bounded per statement, paused between batches and capped per
// cycle, so the janitor never turns into one long DELETE that holds locks or
// floods the WAL.
package retention

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// Table names used as the `table` metric label.
const (
	TableDagRuns             = "dag_runs"
	TableTaskInstances       = "task_instances"
	TableTaskStateHistory    = "task_state_history"
	TableTaskInstanceHistory = "task_instance_history"
	TableXComIndex           = "xcom_index"
	TableAuditLog            = "audit_log"
)

// maxRunsPerBatch bounds the runs one DeleteFinishedRuns call removes. Every
// child row of those runs goes in the same transaction, so this keeps that
// transaction short even when a run has many task instances.
const maxRunsPerBatch = 100

// ErrNotLeading ends a cycle when this instance lost leadership mid-cycle.
var ErrNotLeading = errors.New("retention: not leading")

// Counts is the number of rows deleted (or, in dry run, eligible) per table.
type Counts struct {
	DagRuns             int64
	TaskInstances       int64
	TaskStateHistory    int64
	TaskInstanceHistory int64
	XComIndex           int64
	AuditLog            int64
}

// Add returns the field-wise sum of c and o.
func (c Counts) Add(o Counts) Counts {
	return Counts{
		DagRuns:             c.DagRuns + o.DagRuns,
		TaskInstances:       c.TaskInstances + o.TaskInstances,
		TaskStateHistory:    c.TaskStateHistory + o.TaskStateHistory,
		TaskInstanceHistory: c.TaskInstanceHistory + o.TaskInstanceHistory,
		XComIndex:           c.XComIndex + o.XComIndex,
		AuditLog:            c.AuditLog + o.AuditLog,
	}
}

// Total is the number of rows across every table.
func (c Counts) Total() int64 {
	return c.DagRuns + c.TaskInstances + c.TaskStateHistory + c.TaskInstanceHistory + c.XComIndex + c.AuditLog
}

func (c Counts) byTable() map[string]int64 {
	return map[string]int64{
		TableDagRuns:             c.DagRuns,
		TableTaskInstances:       c.TaskInstances,
		TableTaskStateHistory:    c.TaskStateHistory,
		TableTaskInstanceHistory: c.TaskInstanceHistory,
		TableXComIndex:           c.XComIndex,
		TableAuditLog:            c.AuditLog,
	}
}

// Store is the storage the janitor deletes through.
type Store interface {
	// TenantIDs lists every tenant.
	TenantIDs(ctx context.Context) ([]string, error)
	// DeleteFinishedRuns removes, in one transaction, at most rowLimit rows of
	// up to maxRuns settled runs of tenant that ended before cutoff, run rows
	// included, children first. A run with more children than fit is finished
	// by later calls, each of which re-checks that the run is still eligible.
	DeleteFinishedRuns(ctx context.Context, tenant string, cutoff time.Time, maxRuns, rowLimit int) (Counts, error)
	// DeleteAuditLog removes up to limit audit rows of tenant older than cutoff.
	// An empty tenant means the tenant-less system rows.
	DeleteAuditLog(ctx context.Context, tenant string, cutoff time.Time, limit int) (int64, error)
	// CountEligible counts what a cycle would delete, summed across all tenants;
	// a nil cutoff skips that class.
	CountEligible(ctx context.Context, runCutoff, auditCutoff *time.Time) (Counts, error)
	// RecordRetentionPurge writes an audit entry for rows of the audit log a
	// cycle deleted in the scope of tenant ("" is the system rows).
	RecordRetentionPurge(ctx context.Context, tenant string, rows int64, cutoff time.Time) error
}

// Recorder receives the janitor's metrics.
type Recorder interface {
	RecordRetentionDeleted(table string, n int64)
	RecordRetentionEligible(table string, n int64)
	ObserveRetentionCycle(d time.Duration)
}

// Config is the janitor's view of the retention section.
type Config struct {
	DagRunsDays     int
	AuditLogDays    int
	DryRun          bool
	BatchSize       int
	BatchPause      time.Duration
	MaxRowsPerCycle int
}

// Enabled reports whether any class is configured.
func (c Config) Enabled() bool { return c.DagRunsDays > 0 || c.AuditLogDays > 0 }

// Janitor runs retention cycles.
type Janitor struct {
	store   Store
	cfg     Config
	rec     Recorder
	logger  *slog.Logger
	leading func() bool
	now     func() time.Time
	sleep   func(context.Context, time.Duration) error
}

// New builds a Janitor. A nil recorder drops metrics.
func New(store Store, cfg Config, rec Recorder, logger *slog.Logger) *Janitor {
	return &Janitor{store: store, cfg: cfg, rec: rec, logger: logger, now: time.Now, sleep: sleepCtx}
}

// SetLeading gates every batch on leadership: once leading reports false the
// cycle stops with ErrNotLeading before its next batch.
func (j *Janitor) SetLeading(leading func() bool) { j.leading = leading }

// cycle tracks one RunOnce: the rows done so far against the per-cycle cap.
type cycle struct {
	done Counts
}

// lane is one stream of batches in a cycle: one tenant's runs, or one scope's
// audit rows ("" is the tenant-less system rows).
type lane struct {
	tenant string
	audit  bool
	cutoff time.Time
	done   bool
	purged int64
}

// RunOnce runs one retention cycle and returns what it deleted, or in dry run
// what it would delete. The lanes (each tenant's runs, then each scope's audit
// rows) take turns, one batch each, so under the cycle cap no tenant and no
// class waits behind another's backlog.
func (j *Janitor) RunOnce(ctx context.Context) (Counts, error) {
	if !j.cfg.Enabled() {
		return Counts{}, nil
	}
	start := time.Now()
	defer func() {
		if j.rec != nil {
			j.rec.ObserveRetentionCycle(time.Since(start))
		}
	}()
	if j.cfg.DryRun {
		return j.countOnly(ctx)
	}
	j.resetEligible()
	tenants, err := j.store.TenantIDs(ctx)
	if err != nil {
		return Counts{}, err
	}
	lanes := j.lanes(tenants)
	c := &cycle{}
	err = j.roundRobin(ctx, c, lanes)
	j.recordPurges(ctx, lanes)
	return c.done, err
}

func (j *Janitor) lanes(tenants []string) []*lane {
	var out []*lane
	if j.cfg.DagRunsDays > 0 {
		cutoff := j.now().Add(-days(j.cfg.DagRunsDays))
		for _, t := range tenants {
			out = append(out, &lane{tenant: t, cutoff: cutoff})
		}
	}
	if j.cfg.AuditLogDays > 0 {
		cutoff := j.now().Add(-days(j.cfg.AuditLogDays))
		for _, t := range append(tenants[:len(tenants):len(tenants)], "") {
			out = append(out, &lane{tenant: t, audit: true, cutoff: cutoff})
		}
	}
	return out
}

// roundRobin gives every unfinished lane one batch per pass until all are done
// or the cycle cap is reached.
func (j *Janitor) roundRobin(ctx context.Context, c *cycle, lanes []*lane) error {
	for active := true; active; {
		active = false
		for _, l := range lanes {
			if l.done {
				continue
			}
			if j.budget(c) <= 0 {
				return nil
			}
			if err := j.gate(ctx); err != nil {
				return err
			}
			n, err := j.batch(ctx, c, l)
			if err != nil {
				return err
			}
			if !l.done {
				active = true
			}
			if n > 0 {
				if err := j.sleep(ctx, j.cfg.BatchPause); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// batch runs one bounded delete for the lane and returns the rows it removed.
// Every batch's row limit is the batch size or what is left of the cycle cap,
// whichever is smaller, so a cycle never deletes more than the cap.
func (j *Janitor) batch(ctx context.Context, c *cycle, l *lane) (int64, error) {
	limit := min(int64(j.cfg.BatchSize), j.budget(c))
	if l.audit {
		n, err := j.store.DeleteAuditLog(ctx, l.tenant, l.cutoff, int(limit))
		if err != nil {
			return 0, err
		}
		j.record(c, Counts{AuditLog: n})
		l.purged += n
		l.done = n < limit
		return n, nil
	}
	got, err := j.store.DeleteFinishedRuns(ctx, l.tenant, l.cutoff, min(maxRunsPerBatch, j.cfg.BatchSize), int(limit))
	if err != nil {
		return 0, err
	}
	j.record(c, got)
	// A batch that removed only child rows stopped at its row limit inside a
	// run; the lane is done only when a batch finds nothing left.
	l.done = got.Total() == 0
	return got.Total(), nil
}

// recordPurges writes one audit entry per scope whose audit rows this cycle
// deleted, so removing audit history is itself on the record. The entry is
// new, so the purge that wrote it can never select it.
func (j *Janitor) recordPurges(ctx context.Context, lanes []*lane) {
	for _, l := range lanes {
		if !l.audit || l.purged == 0 {
			continue
		}
		if err := j.store.RecordRetentionPurge(context.WithoutCancel(ctx), l.tenant, l.purged, l.cutoff); err != nil {
			j.logger.Error("recording retention purge in the audit log", "tenant", l.tenant, "rows", l.purged, "error", err)
		}
	}
}

// resetEligible zeroes the dry-run gauges, so a count from an earlier dry run
// does not linger once the janitor deletes.
func (j *Janitor) resetEligible() {
	if j.rec == nil {
		return
	}
	for table := range (Counts{}).byTable() {
		j.rec.RecordRetentionEligible(table, 0)
	}
}

func (j *Janitor) countOnly(ctx context.Context) (Counts, error) {
	var runCutoff, auditCutoff *time.Time
	if j.cfg.DagRunsDays > 0 {
		t := j.now().Add(-days(j.cfg.DagRunsDays))
		runCutoff = &t
	}
	if j.cfg.AuditLogDays > 0 {
		t := j.now().Add(-days(j.cfg.AuditLogDays))
		auditCutoff = &t
	}
	got, err := j.store.CountEligible(ctx, runCutoff, auditCutoff)
	if err != nil {
		return Counts{}, err
	}
	if j.rec != nil {
		for table, n := range got.byTable() {
			j.rec.RecordRetentionEligible(table, n)
		}
	}
	j.logger.Info("retention dry run: rows eligible for deletion, summed across all tenants",
		"dag_runs", got.DagRuns, "task_instances", got.TaskInstances, "audit_log", got.AuditLog)
	return got, nil
}

// gate stops the cycle on cancellation or lost leadership before a batch.
func (j *Janitor) gate(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if j.leading != nil && !j.leading() {
		return ErrNotLeading
	}
	return nil
}

func (j *Janitor) budget(c *cycle) int64 {
	return int64(j.cfg.MaxRowsPerCycle) - c.done.Total()
}

func (j *Janitor) record(c *cycle, got Counts) {
	c.done = c.done.Add(got)
	if j.rec == nil {
		return
	}
	for table, n := range got.byTable() {
		if n > 0 {
			j.rec.RecordRetentionDeleted(table, n)
		}
	}
}

// RunCycle runs one cycle and logs its outcome. A cycle that fails or loses
// leadership is retried on the caller's next tick.
func (j *Janitor) RunCycle(ctx context.Context) {
	got, err := j.RunOnce(ctx)
	switch {
	case errors.Is(err, ErrNotLeading), errors.Is(err, context.Canceled):
	case err != nil:
		j.logger.Error("retention cycle", "error", err, "rows_deleted", got.Total())
	case !j.cfg.DryRun && got.Total() > 0:
		j.logger.Info("retention cycle", "dag_runs", got.DagRuns, "task_instances", got.TaskInstances,
			"audit_log", got.AuditLog, "rows_deleted", got.Total())
	}
}

func days(n int) time.Duration { return time.Duration(n) * 24 * time.Hour }

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
