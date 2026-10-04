//go:build integration

package storage_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/scheduler"
)

// TestScheduledRunLandsInTheOwningTenantIntegration covers #209: the default
// tenant and a second tenant each own a scheduled DAG with the same dag_id.
// ScheduledDAGs reports each with its own tenant, and a scheduled run created
// for the second tenant lands on its DAG only. Before the fix it landed on the
// default tenant's same-named DAG, which is what tenant A stands for here.
func TestScheduledRunLandsInTheOwningTenantIntegration(t *testing.T) {
	repo, store, ctx := openRepo(t)
	tenantA, tenantB := "default", uniqueTenant("sched-b")
	if _, err := repo.EnsureTenant(ctx, tenantB, tenantB); err != nil {
		t.Fatalf("EnsureTenant %s: %v", tenantB, err)
	}
	dagID := fmt.Sprintf("sched_%d", time.Now().UnixNano())
	schedule := "@hourly"
	spec := domain.DAGSpec{
		SchemaVersion: "1.0", DagID: dagID, DagVersion: "v1", Image: "img:v1", Schedule: &schedule,
		Tasks: []domain.TaskSpec{{TaskID: "a", Type: domain.TaskTypePython}},
	}
	hash, err := spec.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{tenantA, tenantB} {
		if _, err := repo.RegisterDagVersion(ctx, name, spec, hash); err != nil {
			t.Fatalf("register %s in %s: %v", dagID, name, err)
		}
	}
	uuidA, err := repo.TenantUUID(ctx, tenantA)
	if err != nil {
		t.Fatal(err)
	}
	uuidB, err := repo.TenantUUID(ctx, tenantB)
	if err != nil {
		t.Fatal(err)
	}

	dags, err := store.ScheduledDAGs(ctx)
	if err != nil {
		t.Fatalf("ScheduledDAGs: %v", err)
	}
	seen := map[string]bool{}
	var forB scheduler.ScheduledDAG
	for _, d := range dags {
		if d.DagID != dagID {
			continue
		}
		seen[d.TenantID] = true
		if d.TenantID == uuidB {
			forB = d
		}
	}
	if !seen[uuidA] || !seen[uuidB] || len(seen) != 2 {
		t.Fatalf("ScheduledDAGs tenants for %s = %v, want %s and %s", dagID, seen, uuidA, uuidB)
	}

	logical := time.Now().UTC().Truncate(time.Hour)
	if err := store.CreateScheduledRun(ctx, forB.TenantID, forB.DagID, logical); err != nil {
		t.Fatalf("CreateScheduledRun: %v", err)
	}

	runsB, totalB, err := repo.ListDagRuns(ctx, tenantB, dagID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if totalB != 1 || len(runsB) != 1 {
		t.Fatalf("tenant B runs = %d, want 1", totalB)
	}
	if _, totalA, err := repo.ListDagRuns(ctx, tenantA, dagID, 10, 0); err != nil || totalA != 0 {
		t.Errorf("tenant A must not receive tenant B's scheduled run: total=%d err=%v", totalA, err)
	}
}
