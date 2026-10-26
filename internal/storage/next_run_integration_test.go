//go:build integration

package storage_test

import (
	"context"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/scheduler"
	"github.com/dexadata/dexaflow/internal/storage"
)

// listedIn returns the scheduled DAGs of tenant the list holds at now, by dag_id.
func listedIn(ctx context.Context, t *testing.T, sched *storage.SchedulerStore, tenantID string, now time.Time) map[string]scheduler.ScheduledDAG {
	t.Helper()
	dags, err := sched.ScheduledDAGs(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]scheduler.ScheduledDAG{}
	for _, d := range dags {
		if d.TenantID == tenantID {
			out[d.DagID] = d
		}
	}
	return out
}

// TestScheduledDAGsListsOnlyDAGsThatMayBeDue covers #2175: a DAG with no
// stored next due time is listed, and one with a time later than now is left
// out until that time.
func TestScheduledDAGsListsOnlyDAGsThatMayBeDue(t *testing.T) {
	// Arrange
	repo, sched, ctx := openRepo(t)
	hourly := "@hourly"
	tenant := limitedTenant(ctx, t, repo, "nextrun", domain.TenantLimitsUpdate{})
	for _, id := range []string{"later", "unknown"} {
		if err := registerIn(ctx, t, repo, tenant, id, "v1", &hourly); err != nil {
			t.Fatal(err)
		}
	}
	tid := tenantUUID(ctx, t, tenant)
	now := time.Now().UTC().Truncate(time.Second)
	first := listedIn(ctx, t, sched, tid, now)

	// Act
	if err := sched.SetNextRunAt(ctx, tid, "later", first["later"].ScheduleEpoch, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	// Assert
	if len(first) != 2 || first["unknown"].NextRunAt != nil {
		t.Fatalf("first list = %v, want both DAGs with no stored time", first)
	}
	atNow := listedIn(ctx, t, sched, tid, now)
	if _, ok := atNow["later"]; ok {
		t.Error("a DAG due in an hour is listed now")
	}
	if _, ok := atNow["unknown"]; !ok {
		t.Error("a DAG with no stored time is not listed")
	}
	inTwoHours := listedIn(ctx, t, sched, tid, now.Add(2*time.Hour))
	if d, ok := inTwoHours["later"]; !ok || d.NextRunAt == nil || !d.NextRunAt.Equal(now.Add(time.Hour)) {
		t.Errorf("two hours later, later = %+v (listed %v), want it listed with its stored time", d, ok)
	}
}

// TestNextRunIsClearedByAChangeDuringATick: a new current version, a deleted
// run and a cleared history each clear the stored time and move the epoch, so
// a time computed from the DAG as it was before the change is dropped.
func TestNextRunIsClearedByAChangeDuringATick(t *testing.T) {
	hourly := "@hourly"
	for _, tc := range []struct {
		name   string
		change func(ctx context.Context, t *testing.T, repo *storage.Repository, tenant string)
	}{
		{name: "new version", change: func(ctx context.Context, t *testing.T, repo *storage.Repository, tenant string) {
			daily := "@daily"
			if err := registerIn(ctx, t, repo, tenant, "etl", "v2", &daily); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "deleted run", change: func(ctx context.Context, t *testing.T, repo *storage.Repository, tenant string) {
			if err := repo.DeleteDagRun(ctx, tenant, "etl", "m1"); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "cleared history", change: func(ctx context.Context, t *testing.T, repo *storage.Repository, tenant string) {
			if err := repo.ClearDagHistory(ctx, tenant, "etl"); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Arrange
			repo, sched, ctx := openRepo(t)
			tenant := limitedTenant(ctx, t, repo, "nextrunchange", domain.TenantLimitsUpdate{})
			if err := registerIn(ctx, t, repo, tenant, "etl", "v1", &hourly); err != nil {
				t.Fatal(err)
			}
			if _, err := repo.CreateDagRun(ctx, tenant, "etl", domain.DagRun{
				RunID: "m1", LogicalDate: time.Now().UTC(), State: domain.DagRunStateQueued, RunType: "manual",
			}); err != nil {
				t.Fatal(err)
			}
			tid := tenantUUID(ctx, t, tenant)
			now := time.Now().UTC()
			listed := listedIn(ctx, t, sched, tid, now)["etl"]

			// Act
			tc.change(ctx, t, repo, tenant)
			err := sched.SetNextRunAt(ctx, tid, "etl", listed.ScheduleEpoch, now.Add(time.Hour))

			// Assert
			if err != nil {
				t.Fatal(err)
			}
			after, ok := listedIn(ctx, t, sched, tid, now)["etl"]
			if !ok || after.NextRunAt != nil || after.ScheduleEpoch == listed.ScheduleEpoch {
				t.Errorf("after the change: listed %v, %+v; want listed with no stored time and a new epoch (was %d)", ok, after, listed.ScheduleEpoch)
			}
		})
	}
}
