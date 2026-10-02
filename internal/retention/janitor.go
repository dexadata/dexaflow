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
	// DeleteFinishedRuns removes up to maxRuns settled runs of tenant that ended
	// before cutoff, with every child row, children first. Each statement
	// removes at most rowLimit rows.
	DeleteFinishedRuns(ctx context.Context, tenant string, cutoff time.Time, maxRuns, rowLimit int) (Counts, error)
	// DeleteAuditLog removes up to limit audit rows of tenant older than cutoff.
	// An empty tenant means the tenant-less system rows.
	DeleteAuditLog(ctx context.Context, tenant string, cutoff time.Time, limit int) (int64, error)
	// CountEligible counts what a cycle would delete, summed across all tenants;
	// a nil cutoff skips that class.
	CountEligible(ctx context.Context, runCutoff, auditCutoff *time.Time) (Counts, error)
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

// RunOnce runs one retention cycle and returns what it deleted, or in dry run
// what it would delete.
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
	tenants, err := j.store.TenantIDs(ctx)
	if err != nil {
		return Counts{}, err
	}
	c := &cycle{}
	if j.cfg.DagRunsDays > 0 {
		cutoff := j.now().Add(-days(j.cfg.DagRunsDays))
		for _, tenant := range tenants {
			if err := j.deleteRuns(ctx, c, tenant, cutoff); err != nil {
				return c.done, err
			}
		}
	}
	if j.cfg.AuditLogDays > 0 {
		cutoff := j.now().Add(-days(j.cfg.AuditLogDays))
		for _, tenant := range append(tenants[:len(tenants):len(tenants)], "") {
			if err := j.deleteAudit(ctx, c, tenant, cutoff); err != nil {
				return c.done, err
			}
		}
	}
	return c.done, nil
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

func (j *Janitor) deleteRuns(ctx context.Context, c *cycle, tenant string, cutoff time.Time) error {
	maxRuns := min(maxRunsPerBatch, j.cfg.BatchSize)
	for j.budget(c) > 0 {
		if err := j.gate(ctx); err != nil {
			return err
		}
		got, err := j.store.DeleteFinishedRuns(ctx, tenant, cutoff, maxRuns, j.cfg.BatchSize)
		if err != nil {
			return err
		}
		j.record(c, got)
		if got.DagRuns == 0 {
			return nil
		}
		if err := j.sleep(ctx, j.cfg.BatchPause); err != nil {
			return err
		}
	}
	return nil
}

func (j *Janitor) deleteAudit(ctx context.Context, c *cycle, tenant string, cutoff time.Time) error {
	for {
		limit := min(int64(j.cfg.BatchSize), j.budget(c))
		if limit <= 0 {
			return nil
		}
		if err := j.gate(ctx); err != nil {
			return err
		}
		n, err := j.store.DeleteAuditLog(ctx, tenant, cutoff, int(limit))
		if err != nil {
			return err
		}
		j.record(c, Counts{AuditLog: n})
		if n < limit {
			return nil
		}
		if err := j.sleep(ctx, j.cfg.BatchPause); err != nil {
			return err
		}
	}
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
