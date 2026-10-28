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
	seedNewerRun(t, pool, ctx, tenant, dag, version)

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
	if n := countFor(t, pool, ctx, `SELECT count(*) FROM task_instance_history h JOIN task_instances ti ON ti.id = h.task_instance_id WHERE ti.dag_run_id = $1`, run); n != 2 {
		t.Errorf("attempt history rows left = %d, want 2", n)
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
	seedNewerRun(t, pool, ctx, tenant, dag, version)

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

// TestRetentionNeverLeavesARunWithPartOfItsTaskInstances: a run whose rows
// span two batches is cleared between them. A clear re-opens the run and only
// resets the task instances it finds, so a run left with part of its task
// instances would re-run without the deleted tasks and never settle. A batch
// therefore deletes a run's task instances all together or not at all: here the
// first batch spends its limit on the child rows and stops before the task
// instances, and the cleared run keeps all three.
func TestRetentionNeverLeavesARunWithPartOfItsTaskInstances(t *testing.T) {
	store, pool, ctx := openRetention(t)
	tenant, version, dag := seedTenant(t, pool, ctx, fmt.Sprintf("retention_split_%d", time.Now().UnixNano()))
	old := 400 * 24 * time.Hour
	run := seedRun(t, pool, ctx, tenant, dag, version, "split", "success", &old, "success", "success", "success")
	seedNewerRun(t, pool, ctx, tenant, dag, version)
	cutoff := time.Now().Add(-24 * time.Hour)

	// 3 task instances, each with 1 attempt row and 1 XCom row: 6 child rows,
	// so a limit of 7 has 1 row left for the 3 instances.
	got, err := store.DeleteFinishedRuns(ctx, tenant, cutoff, 100, 7)
	if err != nil {
		t.Fatalf("DeleteFinishedRuns: %v", err)
	}
	if got.TaskInstances != 0 {
		t.Errorf("first batch deleted %d task instances, want 0 (all 3 do not fit the 1 row left)", got.TaskInstances)
	}
	mustExec(t, pool, ctx, `UPDATE task_instances SET state = 'none' WHERE dag_run_id = $1`, run)
	mustExec(t, pool, ctx, `UPDATE dag_runs SET state = 'queued', ended_at = NULL WHERE id = $1`, run)

	if _, err := store.DeleteFinishedRuns(ctx, tenant, cutoff, 100, 7); err != nil {
		t.Fatalf("DeleteFinishedRuns: %v", err)
	}
	if n := countFor(t, pool, ctx, `SELECT count(*) FROM task_instances WHERE dag_run_id = $1`, run); n != 3 {
		t.Errorf("the cleared run has %d of its 3 task instances, want all 3", n)
	}
}

// TestRetentionDeletesARunLargerThanTheBatchInOneGo: a run with more task
// instances than the batch limit is still deleted, all its task instances in
// the batch that finds the whole limit free, so a large run cannot be kept
// forever by the rule above.
func TestRetentionDeletesARunLargerThanTheBatchInOneGo(t *testing.T) {
	store, pool, ctx := openRetention(t)
	tenant, version, dag := seedTenant(t, pool, ctx, fmt.Sprintf("retention_large_%d", time.Now().UnixNano()))
	old := 400 * 24 * time.Hour
	run := seedRun(t, pool, ctx, tenant, dag, version, "large", "success", &old, "success", "success", "success")
	seedNewerRun(t, pool, ctx, tenant, dag, version)
	cutoff := time.Now().Add(-24 * time.Hour)

	var total int64
	for range 20 {
		got, err := store.DeleteFinishedRuns(ctx, tenant, cutoff, 100, 2)
		if err != nil {
			t.Fatalf("DeleteFinishedRuns: %v", err)
		}
		if got.TaskInstances != 0 && got.TaskInstances != 3 {
			t.Fatalf("a batch deleted %d task instances, want all 3 of the run or none", got.TaskInstances)
		}
		total += got.Total()
		if got.Total() == 0 {
			break
		}
	}
	if runExists(t, pool, ctx, run) {
		t.Errorf("the run was not deleted after %d rows", total)
	}
}
