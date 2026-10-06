package scheduler

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// A tenant may carry a daily run cap (max_runs_per_day). The store refuses a
// scheduled run past it with domain.ErrLimitExceeded; the scheduler must skip
// the slot, log it once, and carry on with every other DAG.

// TestStepSkipsScheduledRunsOfATenantAtItsDailyCap: tenant A is at its cap, so
// its due slot is skipped and metered as a cap skip, not as a create error;
// tenant B's DAG still gets its run in the same tick.
func TestStepSkipsScheduledRunsOfATenantAtItsDailyCap(t *testing.T) {
	store := newFakeStore()
	store.limitedTenants = map[string]bool{tenantA: true}
	store.scheduled = []ScheduledDAG{
		{TenantID: tenantA, DagID: "etl", Schedule: "@hourly"},
		{TenantID: tenantB, DagID: "etl", Schedule: "@hourly"},
	}
	rec := &fakeRecorder{}
	s := newScheduler(store)
	s.SetRecorder(rec)

	err := s.Step(context.Background())

	if err != nil {
		t.Fatalf("Step = %v, want the cap to be absorbed", err)
	}
	if len(store.createdTenants) != 1 || store.createdTenants[0] != tenantB {
		t.Errorf("created runs in tenants %v, want only tenant B", store.createdTenants)
	}
	if strings.Join(rec.decisions, ",") != "tenant_daily_run_cap,create_run" {
		t.Errorf("decisions = %v, want [tenant_daily_run_cap create_run]", rec.decisions)
	}
}

// TestStepStopsACatchupAtTheDailyCap: once the cap refuses one catchup slot, the
// remaining slots of that DAG and the other DAGs of the same tenant are not
// tried again in the same tick.
func TestStepStopsACatchupAtTheDailyCap(t *testing.T) {
	last := time.Now().UTC().Add(-4 * time.Hour).Truncate(time.Hour)
	store := newFakeStore()
	store.limitedTenants = map[string]bool{tenantA: true}
	store.scheduled = []ScheduledDAG{
		{TenantID: tenantA, DagID: "etl", Schedule: "@hourly", LastLogical: &last, Catchup: true},
		{TenantID: tenantA, DagID: "report", Schedule: "@hourly", LastLogical: &last, Catchup: true},
	}

	err := newScheduler(store).Step(context.Background())

	if err != nil {
		t.Fatal(err)
	}
	if store.limitedCalls != 1 {
		t.Errorf("store refused %d creations in one tick, want 1 (no retry of a capped tenant)", store.limitedCalls)
	}
}

// clearOfMidnightUTC waits out the last seconds of a UTC day, so a test whose
// ticks must all fall on one UTC day cannot straddle midnight, where a second
// warning would be correct.
func clearOfMidnightUTC(t *testing.T) {
	t.Helper()
	now := time.Now().UTC()
	midnight := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
	if left := midnight.Sub(now); left < 10*time.Second {
		t.Logf("waiting %v for the UTC day to turn", left)
		time.Sleep(left)
	}
}

// TestDailyCapWarningIsLoggedOncePerTenantAndDay: a capped tenant's due slot is
// retried every tick, but the warning is written once, not on every tick.
func TestDailyCapWarningIsLoggedOncePerTenantAndDay(t *testing.T) {
	clearOfMidnightUTC(t)
	store := newFakeStore()
	store.limitedTenants = map[string]bool{tenantA: true}
	store.scheduled = []ScheduledDAG{{TenantID: tenantA, DagID: "etl", Schedule: "@hourly"}}
	var buf bytes.Buffer
	s := NewScheduler(store, slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})), time.Millisecond)
	s.SetLeading(true)

	for range 3 {
		if err := s.Step(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	if n := strings.Count(buf.String(), "daily run limit"); n != 1 {
		t.Errorf("warnings over 3 ticks = %d, want 1\n%s", n, buf.String())
	}
	if !strings.Contains(buf.String(), "max_runs_per_day of 1") {
		t.Errorf("warning does not name the limit:\n%s", buf.String())
	}
}
