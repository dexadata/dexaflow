package scheduler

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// A suspended tenant is left out of the scheduled DAG list, so the scheduler
// normally never asks for its runs. A suspension that lands between the list
// and the insert reaches the store instead, which refuses the run with
// domain.ErrTenantSuspended; the scheduler must skip the tenant for the rest
// of the tick, meter it apart from errors, and keep every other tenant moving.

// TestStepSkipsATenantSuspendedMidTick: tenant A's two DAGs are listed, its
// first creation is refused as suspended, so its second DAG is not tried and
// tenant B still gets its run.
func TestStepSkipsATenantSuspendedMidTick(t *testing.T) {
	// Arrange
	store := newFakeStore()
	store.suspendedTenants = map[string]bool{tenantA: true}
	store.scheduled = []ScheduledDAG{
		{TenantID: tenantA, DagID: "etl", Schedule: "@hourly"},
		{TenantID: tenantA, DagID: "report", Schedule: "@hourly"},
		{TenantID: tenantB, DagID: "etl", Schedule: "@hourly"},
	}
	rec := &fakeRecorder{}
	s := newScheduler(store)
	s.SetRecorder(rec)

	// Act
	err := s.Step(context.Background())

	// Assert
	if err != nil {
		t.Fatalf("Step = %v, want the suspension to be absorbed", err)
	}
	if store.suspendedCalls != 1 {
		t.Errorf("store refused %d creations of the suspended tenant, want 1", store.suspendedCalls)
	}
	if len(store.createdTenants) != 1 || store.createdTenants[0] != tenantB {
		t.Errorf("created runs in tenants %v, want only tenant B", store.createdTenants)
	}
	if strings.Join(rec.decisions, ",") != "tenant_suspended,create_run" {
		t.Errorf("decisions = %v, want [tenant_suspended create_run]", rec.decisions)
	}
}

// TestSuspensionWarningIsLoggedOncePerTenant: the refusal repeats every tick
// while the race lasts, but the warning is written once and carries the reason.
func TestSuspensionWarningIsLoggedOncePerTenant(t *testing.T) {
	// Arrange
	store := newFakeStore()
	store.suspendedTenants = map[string]bool{tenantA: true}
	store.scheduled = []ScheduledDAG{{TenantID: tenantA, DagID: "etl", Schedule: "@hourly"}}
	var buf bytes.Buffer
	s := NewScheduler(store, slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})), time.Millisecond)
	s.SetLeading(true)

	// Act
	for range 3 {
		if err := s.Step(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	// Assert
	if n := strings.Count(buf.String(), "skipping scheduled runs"); n != 1 {
		t.Errorf("warnings over 3 ticks = %d, want 1\n%s", n, buf.String())
	}
	if !strings.Contains(buf.String(), "contract ended") {
		t.Errorf("warning does not carry the reason:\n%s", buf.String())
	}
}
