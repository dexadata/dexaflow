//go:build integration

package storage_test

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestRetentionKeepsARunClearedAfterItsLock: a clear commits after the janitor
// locked an expired run but before it deleted the run's rows. The clear resets
// task instances without locking the run row, so the run lock does not stop it.
// The janitor must notice and keep the whole run: no task instance (cleared or
// not), history or XCom row of it may be deleted.
func TestRetentionKeepsARunClearedAfterItsLock(t *testing.T) {
	store, pool, ctx := openRetention(t)
	tenant, version, dag := seedTenant(t, pool, ctx, fmt.Sprintf("retention_race_%d", time.Now().UnixNano()))
	old := 400 * 24 * time.Hour
	run := seedRun(t, pool, ctx, tenant, dag, version, "cleared_mid_batch", "success", &old, "success", "success")

	store.SetAfterRunLockHook(func() {
		mustExec(t, pool, ctx, `UPDATE task_instances SET state = 'none' WHERE dag_run_id = $1 AND task_id = 't0'`, run)
	})
	got, err := store.DeleteFinishedRuns(ctx, tenant, time.Now().Add(-24*time.Hour), 100, 10000)
	if err != nil {
		t.Fatalf("DeleteFinishedRuns: %v", err)
	}
	if got.Total() != 0 {
		t.Errorf("deleted %+v, want nothing: the run was cleared after it was locked", got)
	}
	if n := countFor(t, pool, ctx, `SELECT count(*) FROM task_instances WHERE dag_run_id = $1`, run); n != 2 {
		t.Errorf("task instances left = %d, want 2 (the cleared run must stay whole)", n)
	}
	if n := countFor(t, pool, ctx, `SELECT count(*) FROM task_state_history h JOIN task_instances ti ON ti.id = h.task_instance_id WHERE ti.dag_run_id = $1`, run); n != 4 {
		t.Errorf("state history rows left = %d, want 4", n)
	}
	if !runExists(t, pool, ctx, run) {
		t.Error("the cleared run was deleted")
	}
}

// TestRetentionSkipsARunAClearIsWriting: a clear holds a task instance row lock
// (its transaction is still open). The janitor must neither wait on it (a clear
// then updating the run row would deadlock with the janitor's run lock) nor
// delete the run; it skips the run, and the clear commits normally.
func TestRetentionSkipsARunAClearIsWriting(t *testing.T) {
	store, pool, ctx := openRetention(t)
	tenant, version, dag := seedTenant(t, pool, ctx, fmt.Sprintf("retention_lock_%d", time.Now().UnixNano()))
	old := 400 * 24 * time.Hour
	run := seedRun(t, pool, ctx, tenant, dag, version, "being_cleared", "success", &old, "success")

	clear, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = clear.Rollback(ctx) }()
	if _, err := clear.Exec(ctx, `UPDATE task_instances SET state = 'none' WHERE dag_run_id = $1`, run); err != nil {
		t.Fatal(err)
	}

	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	got, err := store.DeleteFinishedRuns(cctx, tenant, time.Now().Add(-24*time.Hour), 100, 10000)
	if err != nil {
		t.Fatalf("DeleteFinishedRuns: %v (it must skip a run whose task instances are locked, not wait or fail)", err)
	}
	if got.Total() != 0 {
		t.Errorf("deleted %+v, want nothing while a clear writes the run", got)
	}
	// The clear goes on to reopen the run: it must not be blocked or deadlocked.
	if _, err := clear.Exec(cctx, `UPDATE dag_runs SET state = 'queued' WHERE id = $1`, run); err != nil {
		t.Fatalf("clear reopening the run: %v", err)
	}
	if err := clear.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if n := countFor(t, pool, ctx, `SELECT count(*) FROM task_instances WHERE dag_run_id = $1 AND state = 'none'`, run); n != 1 {
		t.Errorf("cleared task instances = %d, want 1", n)
	}
}

// TestRetentionDryRunCountIsBoundedPerTenant: the dry run counts at most the cap
// per tenant and says the total is a lower bound, so its cost on a large
// install is bounded instead of growing with the whole history.
func TestRetentionDryRunCountIsBoundedPerTenant(t *testing.T) {
	store, pool, ctx := openRetention(t)
	tenant, version, dag := seedTenant(t, pool, ctx, fmt.Sprintf("retention_cap_%d", time.Now().UnixNano()))
	old := 400 * 24 * time.Hour
	for i := range 3 {
		seedRun(t, pool, ctx, tenant, dag, version, fmt.Sprintf("capped_%d", i), "success", &old, "success")
	}
	for range 3 {
		mustExec(t, pool, ctx, `INSERT INTO audit_log (tenant_id, action, occurred_at) VALUES ($1, 'retention.cap', now() - interval '400 days')`, tenant)
	}
	store.SetDryRunCap(2)
	cutoff := time.Now().Add(-30 * 24 * time.Hour)
	got, err := store.CountEligibleForTenant(ctx, tenant, &cutoff, &cutoff)
	if err != nil {
		t.Fatalf("CountEligibleForTenant: %v", err)
	}
	if got.DagRuns != 2 || got.TaskInstances != 2 || got.AuditLog != 2 || !got.Capped {
		t.Errorf("got %+v, want 2 runs, 2 task instances, 2 audit rows, capped", got)
	}
}
