package config

import (
	"strings"
	"testing"
	"time"
)

// TestRetentionDefaultsDeleteNothing: retention is opt-in per data class. A
// config that never mentions it keeps every row, and the pacing knobs carry
// their documented values so an operator who turns a class on inherits small
// batches.
func TestRetentionDefaultsDeleteNothing(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	r := c.Retention
	if r.DagRunsDays != 0 || r.AuditLogDays != 0 {
		t.Errorf("retention days = (%d, %d), want both 0 (off)", r.DagRunsDays, r.AuditLogDays)
	}
	if r.Enabled() {
		t.Error("Enabled() = true on defaults, want false: nothing is deleted unless configured")
	}
	if r.DryRun {
		t.Error("dry_run default = true, want false")
	}
	if r.Interval != time.Hour {
		t.Errorf("interval = %v, want 1h", r.Interval)
	}
	if r.BatchSize != 1000 {
		t.Errorf("batch_size = %d, want 1000", r.BatchSize)
	}
	if r.BatchPause != 100*time.Millisecond {
		t.Errorf("batch_pause = %v, want 100ms", r.BatchPause)
	}
	if r.MaxRowsPerCycle != 100000 {
		t.Errorf("max_rows_per_cycle = %d, want 100000", r.MaxRowsPerCycle)
	}
	if err := r.Validate(); err != nil {
		t.Errorf("Validate() on defaults = %v, want nil", err)
	}
}

// TestRetentionEnvBindsBothPrefixes: the current DEXAFLOW_* name and the legacy
// LEOFLOW_* name both reach the retention keys.
func TestRetentionEnvBindsBothPrefixes(t *testing.T) {
	t.Setenv("DEXAFLOW_RETENTION_DAG_RUNS_DAYS", "90")
	t.Setenv("LEOFLOW_RETENTION_AUDIT_LOG_DAYS", "365")
	t.Setenv("DEXAFLOW_RETENTION_DRY_RUN", "true")
	t.Setenv("LEOFLOW_RETENTION_BATCH_PAUSE", "250ms")
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	r := c.Retention
	if r.DagRunsDays != 90 || r.AuditLogDays != 365 || !r.DryRun || r.BatchPause != 250*time.Millisecond {
		t.Fatalf("retention = %+v, want dag_runs_days=90 audit_log_days=365 dry_run=true batch_pause=250ms", r)
	}
	if !r.Enabled() {
		t.Error("Enabled() = false with a class configured, want true")
	}
}

// TestRetentionValidateRejectsUnsafeValues: a negative window or pacing that
// would turn the janitor into one unbounded DELETE fails boot closed, but only
// when a class is on, so the defaults never trip it.
func TestRetentionValidateRejectsUnsafeValues(t *testing.T) {
	base := func() RetentionSection {
		return RetentionSection{DagRunsDays: 30, Interval: time.Hour, BatchSize: 1000, BatchPause: 100 * time.Millisecond, MaxRowsPerCycle: 100000}
	}
	cases := map[string]struct {
		mut  func(*RetentionSection)
		want string
	}{
		"negative runs window":  {func(r *RetentionSection) { r.DagRunsDays = -1 }, "retention.dag_runs_days"},
		"negative audit window": {func(r *RetentionSection) { r.AuditLogDays = -1 }, "retention.audit_log_days"},
		"zero batch":            {func(r *RetentionSection) { r.BatchSize = 0 }, "retention.batch_size"},
		"huge batch":            {func(r *RetentionSection) { r.BatchSize = 50001 }, "retention.batch_size"},
		"negative pause":        {func(r *RetentionSection) { r.BatchPause = -time.Second }, "retention.batch_pause"},
		"zero interval":         {func(r *RetentionSection) { r.Interval = 0 }, "retention.interval"},
		"zero cycle cap":        {func(r *RetentionSection) { r.MaxRowsPerCycle = 0 }, "retention.max_rows_per_cycle"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := base()
			tc.mut(&r)
			err := r.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want an error naming %s", err, tc.want)
			}
		})
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("Validate() on a sane config = %v", err)
	}
	off := RetentionSection{}
	if err := off.Validate(); err != nil {
		t.Fatalf("Validate() with retention off and zero pacing = %v, want nil", err)
	}
}

// TestValidateIncludesRetention: the server's boot validation runs the
// retention checks.
func TestValidateIncludesRetention(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	c.Auth.JWT.Secret = "test-secret-test-secret-test-secret"
	c.Retention.AuditLogDays = -5
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "retention.audit_log_days") {
		t.Fatalf("Validate() = %v, want the retention error", err)
	}
}
