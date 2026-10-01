//go:build integration

package storage_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/scheduler"
)

// TestPoolCRUDAndDefaultSeedIntegration proves the pools table round-trips
// through the repository and that migration 023 seeded the implicit default_pool
// (ADR 0053 Stage 3).
func TestPoolCRUDAndDefaultSeedIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)

	// The seed migration created default_pool for the default tenant.
	def, err := repo.GetPool(ctx, "default", domain.DefaultPoolName)
	if err != nil {
		t.Fatalf("default_pool must be seeded: %v", err)
	}
	if !def.IsDefault || def.Slots != 128 {
		t.Errorf("default_pool = %+v, want IsDefault + 128 slots", def)
	}

	name := fmt.Sprintf("pool_%d", time.Now().UnixNano())
	if err := repo.SetPool(ctx, "default", domain.Pool{Name: name, Slots: 3, Description: "batch"}); err != nil {
		t.Fatalf("SetPool: %v", err)
	}
	got, err := repo.GetPool(ctx, "default", name)
	if err != nil || got.Slots != 3 || got.Description != "batch" {
		t.Fatalf("GetPool = %+v, err=%v", got, err)
	}

	// Update the slot cap through upsert.
	if err := repo.SetPool(ctx, "default", domain.Pool{Name: name, Slots: 7}); err != nil {
		t.Fatalf("update SetPool: %v", err)
	}
	if got, _ := repo.GetPool(ctx, "default", name); got.Slots != 7 {
		t.Errorf("after update slots = %d, want 7", got.Slots)
	}

	if err := repo.DeletePool(ctx, "default", name); err != nil {
		t.Fatalf("DeletePool: %v", err)
	}
	if _, err := repo.GetPool(ctx, "default", name); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("get deleted pool err = %v, want ErrNotFound", err)
	}
}

// TestPoolDeleteDefaultBlockedIntegration: the seeded default pool cannot be
// deleted — the guarded query returns a conflict, not a silent no-op.
func TestPoolDeleteDefaultBlockedIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	if err := repo.DeletePool(ctx, "default", domain.DefaultPoolName); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("delete default_pool err = %v, want ErrConflict", err)
	}
	if _, err := repo.GetPool(ctx, "default", domain.DefaultPoolName); err != nil {
		t.Errorf("default_pool must survive a delete attempt: %v", err)
	}
}

// TestPoolBudgetsKeyedByTenantIntegration: the scheduler's PoolBudgets snapshot
// keys each cap by scheduler.PoolKey(tenantUUID, name), the key the pool gate
// looks up.
func TestPoolBudgetsKeyedByTenantIntegration(t *testing.T) {
	repo, store, ctx := openRepo(t)
	tid, err := repo.TenantUUID(ctx, "default")
	if err != nil {
		t.Fatal(err)
	}
	budgets, err := store.PoolBudgets(ctx)
	if err != nil {
		t.Fatalf("PoolBudgets: %v", err)
	}
	if got := budgets[scheduler.PoolKey(tid, domain.DefaultPoolName)]; got != 128 {
		t.Errorf("default_pool budget = %d, want 128 (keyed by tenant)", got)
	}
}

// TestPoolSlotUsageCountsMaterializedPoolIntegration: a task materialized with a
// declared pool is counted under that pool by PoolSlotUsage once it is queued —
// the occupancy the Airflow PoolResponse reports.
func TestPoolSlotUsageCountsMaterializedPoolIntegration(t *testing.T) {
	repo, store, ctx := openRepo(t)

	suffix := time.Now().UnixNano()
	dagID := fmt.Sprintf("pool_usage_test_%d", suffix)
	poolName := fmt.Sprintf("usage_%d", suffix)
	if err := repo.SetPool(ctx, "default", domain.Pool{Name: poolName, Slots: 5}); err != nil {
		t.Fatalf("SetPool: %v", err)
	}
	spec := domain.DAGSpec{
		SchemaVersion: "1.0", DagID: dagID, DagVersion: "v1", Image: "img:v1",
		Tasks: []domain.TaskSpec{{TaskID: "a", Type: domain.TaskTypePython, Entrypoint: "dag:a", Pool: poolName}},
	}
	hash, err := spec.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RegisterDagVersion(ctx, "default", spec, hash); err != nil {
		t.Fatalf("register version: %v", err)
	}
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: fmt.Sprintf("pool-usage-run-%d", suffix), State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	runUUID := resolveRunUUID(t, store, ctx, dagID)
	if err := store.MaterializeTasks(ctx, runUUID, spec.Tasks); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	// Move the task into a slot-occupying state.
	if err := store.ApplyTransition(ctx, runUUID, "a", domain.TaskStateQueued); err != nil {
		t.Fatalf("transition: %v", err)
	}

	usage, err := repo.PoolSlotUsage(ctx, "default")
	if err != nil {
		t.Fatalf("PoolSlotUsage: %v", err)
	}
	if got := usage[poolName].Queued; got != 1 {
		t.Errorf("pool %q queued usage = %d, want 1 (materialized pool must be counted)", poolName, got)
	}
}
