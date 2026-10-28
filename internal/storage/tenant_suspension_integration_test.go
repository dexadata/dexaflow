//go:build integration

package storage_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
)

// TestTenantSuspensionHoldsNewRuns covers #2181: a suspended tenant gets no new
// run, manual or scheduled, and drops out of the scheduled DAG list, while
// another tenant is not affected. The DAG's own paused flag is never touched,
// and lifting the suspension restores exactly what the tenant had.
func TestTenantSuspensionHoldsNewRuns(t *testing.T) {
	// Arrange
	repo, sched, ctx := openRepo(t)
	hourly := "@hourly"
	held := limitedTenant(ctx, t, repo, "held", domain.TenantLimitsUpdate{})
	other := limitedTenant(ctx, t, repo, "free", domain.TenantLimitsUpdate{})
	for _, tenant := range []string{held, other} {
		if err := registerIn(ctx, t, repo, tenant, "etl", "v1", &hourly); err != nil {
			t.Fatal(err)
		}
	}
	heldID, otherID := tenantUUID(ctx, t, held), tenantUUID(ctx, t, other)
	manual := func(tenant, runID string) error {
		_, err := repo.CreateDagRun(ctx, tenant, "etl", domain.DagRun{
			RunID: runID, LogicalDate: time.Now().UTC(), State: domain.DagRunStateQueued, RunType: "manual",
		})
		return err
	}
	slot := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Act
	err := repo.SuspendTenant(ctx, held, domain.TenantSuspension{Mode: domain.SuspensionHold, Reason: "contract ended"})

	// Assert
	if err != nil {
		t.Fatalf("SuspendTenant: %v", err)
	}
	got, err := repo.TenantSuspension(ctx, held)
	if err != nil || got.Mode != domain.SuspensionHold || got.Reason != "contract ended" || got.Since.IsZero() {
		t.Fatalf("TenantSuspension = %+v, %v; want hold, the reason and a time", got, err)
	}
	wantSuspended(t, "manual run", manual(held, "m1"), "contract ended")
	wantSuspended(t, "scheduled run", sched.CreateScheduledRun(ctx, heldID, "etl", slot), "contract ended")
	if n := countRuns(ctx, t, held); n != 0 {
		t.Errorf("runs stored for the suspended tenant = %d, want 0", n)
	}
	if err := manual(other, "m1"); err != nil {
		t.Errorf("manual run of another tenant: %v", err)
	}
	listed := scheduledTenants(ctx, t, sched)
	if listed[heldID] || !listed[otherID] {
		t.Errorf("scheduled DAG list has held=%v other=%v, want only the other tenant", listed[heldID], listed[otherID])
	}
	if dag, err := repo.GetDag(ctx, held, "etl"); err != nil || dag.IsPaused {
		t.Errorf("GetDag = paused %v, %v; want the DAG left unpaused", dag.IsPaused, err)
	}

	if err := repo.ResumeTenant(ctx, held); err != nil {
		t.Fatalf("ResumeTenant: %v", err)
	}
	if got, err := repo.TenantSuspension(ctx, held); err != nil || got.Active() {
		t.Errorf("after resume, TenantSuspension = %+v, %v; want none", got, err)
	}
	if err := manual(held, "m2"); err != nil {
		t.Errorf("manual run after resume: %v", err)
	}
	if !scheduledTenants(ctx, t, sched)[heldID] {
		t.Error("after resume, the tenant's DAG is not in the scheduled DAG list")
	}
}

// TestTenantSuspensionComesBeforeTheDailyCap: a suspended tenant with a daily
// run cap is refused as suspended, and the refusal takes nothing from the cap.
func TestTenantSuspensionComesBeforeTheDailyCap(t *testing.T) {
	// Arrange
	repo, _, ctx := openRepo(t)
	clearOfMidnightUTC(t)
	tenant := limitedTenant(ctx, t, repo, "heldcap", domain.TenantLimitsUpdate{MaxRunsPerDay: limit(1)})
	if err := registerIn(ctx, t, repo, tenant, "etl", "v1", nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.SuspendTenant(ctx, tenant, domain.TenantSuspension{Mode: domain.SuspensionHold}); err != nil {
		t.Fatal(err)
	}
	manual := func(runID string) error {
		_, err := repo.CreateDagRun(ctx, tenant, "etl", domain.DagRun{
			RunID: runID, LogicalDate: time.Now().UTC(), State: domain.DagRunStateQueued, RunType: "manual",
		})
		return err
	}

	// Act
	refused := manual("m1")
	if err := repo.ResumeTenant(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	afterResume := manual("m2")

	// Assert
	wantSuspended(t, "run while suspended", refused, "suspended")
	if afterResume != nil {
		t.Errorf("first run after resume: %v, want the cap untouched by the refusal", afterResume)
	}
}

// TestTenantSuspensionRefusesBadInput: an unknown tenant is not found, a mode
// other than hold is a validation error, and resuming a tenant that is not
// suspended is a no-op.
func TestTenantSuspensionRefusesBadInput(t *testing.T) {
	// Arrange
	repo, _, ctx := openRepo(t)
	tenant := limitedTenant(ctx, t, repo, "suspinput", domain.TenantLimitsUpdate{})

	// Act
	unknown := repo.SuspendTenant(ctx, uniqueTenant("missing"), domain.TenantSuspension{Mode: domain.SuspensionHold})
	drain := repo.SuspendTenant(ctx, tenant, domain.TenantSuspension{Mode: "drain"})
	resume := repo.ResumeTenant(ctx, tenant)

	// Assert
	if !errors.Is(unknown, domain.ErrNotFound) {
		t.Errorf("unknown tenant: err = %v, want ErrNotFound", unknown)
	}
	if !errors.Is(drain, domain.ErrValidation) {
		t.Errorf("drain mode: err = %v, want ErrValidation", drain)
	}
	if resume != nil {
		t.Errorf("resuming a tenant that is not suspended: %v, want nil", resume)
	}
}

func wantSuspended(t *testing.T, what string, err error, phrase string) {
	t.Helper()
	if !errors.Is(err, domain.ErrTenantSuspended) {
		t.Fatalf("%s: err = %v, want ErrTenantSuspended", what, err)
	}
	if !strings.Contains(err.Error(), phrase) {
		t.Errorf("%s: error %q does not carry %q", what, err, phrase)
	}
}

// scheduledTenants returns the tenant UUIDs the scheduled DAG list holds.
func scheduledTenants(ctx context.Context, t *testing.T, sched *storage.SchedulerStore) map[string]bool {
	t.Helper()
	dags, err := sched.ScheduledDAGs(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, d := range dags {
		out[d.TenantID] = true
	}
	return out
}
