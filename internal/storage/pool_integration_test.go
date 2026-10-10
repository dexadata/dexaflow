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

// TestPoolSlotUsageSumsWeightedSlotsIntegration (#1499): PoolSlotUsage reports
// slots, not task counts, so the pools API agrees with the admission gate,
// which charges each queued or running task its pool_slots (ADR 0066). Two
// tasks of size 4 fill a pool of 8; a third of size 2 waits in scheduled.
func TestPoolSlotUsageSumsWeightedSlotsIntegration(t *testing.T) {
	repo, store, ctx := openRepo(t)

	suffix := time.Now().UnixNano()
	dagID := fmt.Sprintf("pool_weighted_%d", suffix)
	poolName := fmt.Sprintf("weighted_%d", suffix)
	if err := repo.SetPool(ctx, "default", domain.Pool{Name: poolName, Slots: 8}); err != nil {
		t.Fatalf("SetPool: %v", err)
	}
	tasks := []domain.TaskSpec{
		{TaskID: "a", Type: domain.TaskTypePython, Entrypoint: "dag:a", Pool: poolName, PoolSlots: 4},
		{TaskID: "b", Type: domain.TaskTypePython, Entrypoint: "dag:b", Pool: poolName, PoolSlots: 4},
		{TaskID: "c", Type: domain.TaskTypePython, Entrypoint: "dag:c", Pool: poolName, PoolSlots: 2},
	}
	registerSpec(t, repo, ctx, dagID, tasks)
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	runUUID := resolveRunUUID(t, store, ctx, dagID)
	if err := store.MaterializeTasks(ctx, runUUID, tasks); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	for id, state := range map[string]domain.TaskState{
		"a": domain.TaskStateQueued, "b": domain.TaskStateRunning, "c": domain.TaskStateScheduled,
	} {
		if err := store.ApplyTransition(ctx, runUUID, id, state); err != nil {
			t.Fatalf("transition %s: %v", id, err)
		}
	}

	usage, err := repo.PoolSlotUsage(ctx, "default")
	if err != nil {
		t.Fatalf("PoolSlotUsage: %v", err)
	}
	if got, want := usage[poolName], (domain.PoolUsage{Queued: 4, Running: 4, Scheduled: 2}); got != want {
		t.Errorf("pool %q usage = %+v, want %+v (slots summed per state, not tasks counted)", poolName, got, want)
	}
}

// TestTaskInstancePoolSlotsCheckIntegration: migration 042 gives a task
// instance inserted without a size one slot, which is what every row created
// before the upgrade reads as, and holds a validated check that refuses fewer
// than one slot, the floor EffectivePoolSlots applies.
func TestTaskInstancePoolSlotsCheckIntegration(t *testing.T) {
	_, _, pg, ctx := openInfra(t)
	var def int
	if err := pg.Pool.QueryRow(ctx, `SELECT column_default::int FROM information_schema.columns
WHERE table_name = 'task_instances' AND column_name = 'pool_slots' AND is_nullable = 'NO'`).Scan(&def); err != nil {
		t.Fatalf("read pool_slots default: %v", err)
	}
	if def != 1 {
		t.Errorf("pool_slots default = %d, want 1", def)
	}
	var validated bool
	var check string
	if err := pg.Pool.QueryRow(ctx, `SELECT convalidated, pg_get_constraintdef(oid) FROM pg_constraint
WHERE conrelid = 'task_instances'::regclass AND conname = 'task_instances_pool_slots_positive'`).Scan(&validated, &check); err != nil {
		t.Fatalf("read pool_slots check: %v", err)
	}
	if !validated || check != "CHECK ((pool_slots >= 1))" {
		t.Errorf("pool_slots check = %q (validated %v), want a validated CHECK ((pool_slots >= 1))", check, validated)
	}
}
