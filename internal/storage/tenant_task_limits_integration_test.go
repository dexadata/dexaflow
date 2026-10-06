//go:build integration

package storage_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
)

// registerTasks registers dagID in tenant as version with n bash tasks.
func registerTasks(ctx context.Context, t *testing.T, repo *storage.Repository, tenant, dagID, version string, n int) error {
	t.Helper()
	spec := domain.DAGSpec{SchemaVersion: "1.0", DagID: dagID, DagVersion: version, Image: "img:" + version}
	for i := range n {
		spec.Tasks = append(spec.Tasks, domain.TaskSpec{TaskID: fmt.Sprintf("t%d", i), Type: domain.TaskTypeBash, Entrypoint: "true"})
	}
	hash, err := spec.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.RegisterDagVersion(ctx, tenant, spec, hash)
	return err
}

// TestRegisterDagVersionEnforcesMaxTasks: the tasks of the version being
// registered plus those of the current version of every other DAG may not
// pass max_tasks; a new version replaces its DAG's old count (#1484).
func TestRegisterDagVersionEnforcesMaxTasks(t *testing.T) {
	repo, _, ctx := openRepo(t)
	tenant := limitedTenant(ctx, t, repo, "maxtasks", domain.TenantLimitsUpdate{MaxTasks: limit(5)})
	if err := registerTasks(ctx, t, repo, tenant, "a", "v1", 3); err != nil {
		t.Fatalf("a with 3 tasks: %v", err)
	}

	err := registerTasks(ctx, t, repo, tenant, "b", "v1", 3)

	wantLimitError(t, "b with 3 more tasks", err, "max_tasks of 5")
	if err := registerTasks(ctx, t, repo, tenant, "b", "v1", 2); err != nil {
		t.Errorf("b with 2 tasks (exactly 5): %v", err)
	}
	if err := registerTasks(ctx, t, repo, tenant, "a", "v2", 4); err == nil {
		t.Error("a v2 with 4 tasks (6 in all) was accepted, want max_tasks refusal")
	}
	if err := registerTasks(ctx, t, repo, tenant, "a", "v3", 1); err != nil {
		t.Errorf("a v3 shrinking to 1 task: %v", err)
	}
	got, err := repo.TenantLimits(ctx, tenant)
	if err != nil || got.MaxTasks != 5 {
		t.Errorf("TenantLimits = %+v, %v; want max_tasks 5", got, err)
	}
}

// TestTaskRunsPerMonthChargesTheRunsTasks: a run charges its DAG's task count
// to the month, manual and scheduled alike; a run whose tasks do not fit what
// is left is refused whole and leaves nothing behind; the count starts again
// in a new UTC month (#1484).
func TestTaskRunsPerMonthChargesTheRunsTasks(t *testing.T) {
	repo, sched, ctx := openRepo(t)
	clearOfMidnightUTC(t)
	tenant := limitedTenant(ctx, t, repo, "taskruns", domain.TenantLimitsUpdate{MaxTaskRunsPerMonth: limit(7)})
	if err := registerTasks(ctx, t, repo, tenant, "etl", "v1", 3); err != nil {
		t.Fatal(err)
	}
	tenantID := tenantUUID(ctx, t, tenant)
	manual := func(runID string) error {
		_, err := repo.CreateDagRun(ctx, tenant, "etl", domain.DagRun{
			RunID: runID, LogicalDate: time.Now().UTC(), State: domain.DagRunStateQueued, RunType: "manual",
		})
		return err
	}
	slot := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if err := manual("m1"); err != nil {
		t.Fatalf("first run (3 of 7): %v", err)
	}
	if err := sched.CreateScheduledRun(ctx, tenantID, "etl", slot); err != nil {
		t.Fatalf("second run (6 of 7): %v", err)
	}
	if err := sched.CreateScheduledRun(ctx, tenantID, "etl", slot); err != nil {
		t.Fatalf("an existing scheduled slot is a no-op, got %v", err)
	}
	wantLimitError(t, "third run (9 of 7)", manual("m2"), "max_task_runs_per_month of 7")
	wantLimitError(t, "third run, scheduled", sched.CreateScheduledRun(ctx, tenantID, "etl", slot.Add(time.Hour)), "max_task_runs_per_month of 7")
	if n := countRuns(ctx, t, tenant); n != 2 {
		t.Errorf("runs stored = %d, want 2", n)
	}

	if _, err := adminPool(ctx, t).Exec(ctx,
		`UPDATE tenants SET task_runs_month = (task_runs_month - interval '1 month')::date WHERE name = $1`, tenant); err != nil {
		t.Fatal(err)
	}
	if err := manual("m3"); err != nil {
		t.Errorf("first run of a new UTC month: %v", err)
	}
}

// TestTaskRunsPerMonthAndRunsPerDayChargeTogether: with both limits set, a
// run must fit both, and a refusal by one gives back the other's charge.
func TestTaskRunsPerMonthAndRunsPerDayChargeTogether(t *testing.T) {
	repo, _, ctx := openRepo(t)
	clearOfMidnightUTC(t)
	tenant := limitedTenant(ctx, t, repo, "bothlimits", domain.TenantLimitsUpdate{
		MaxRunsPerDay: limit(5), MaxTaskRunsPerMonth: limit(4),
	})
	if err := registerTasks(ctx, t, repo, tenant, "etl", "v1", 2); err != nil {
		t.Fatal(err)
	}
	manual := func(runID string) error {
		_, err := repo.CreateDagRun(ctx, tenant, "etl", domain.DagRun{
			RunID: runID, LogicalDate: time.Now().UTC(), State: domain.DagRunStateQueued, RunType: "manual",
		})
		return err
	}

	for _, id := range []string{"r1", "r2"} {
		if err := manual(id); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
	wantLimitError(t, "third run", manual("r3"), "max_task_runs_per_month of 4")

	var day int
	if err := adminPool(ctx, t).QueryRow(ctx, `SELECT runs_day_count FROM tenants WHERE name = $1`, tenant).Scan(&day); err != nil {
		t.Fatal(err)
	}
	if day != 2 {
		t.Errorf("runs_day_count = %d, want 2 (the refused run gives its daily charge back)", day)
	}
}
