//go:build integration

package storage_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/retention"
	"github.com/dexadata/dexaflow/internal/storage"
)

// retentionFixture is one isolated tenant seeded with runs in every shape the
// retention janitor must tell apart.
type retentionFixture struct {
	tenant, otherTenant string
	runs                map[string]string // name -> dag_runs.id
}

func openRetention(t *testing.T) (*storage.RetentionStore, *pgxpool.Pool, context.Context) {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must point at a migrated database for integration tests")
	}
	ctx := context.Background()
	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: url})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pg.Close)
	return storage.NewRetentionStore(pg), pg.Pool, ctx
}

func mustExec(t *testing.T, pool *pgxpool.Pool, ctx context.Context, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func mustUUID(t *testing.T, pool *pgxpool.Pool, ctx context.Context, sql string, args ...any) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return id
}

func seedTenant(t *testing.T, pool *pgxpool.Pool, ctx context.Context, name string) (tenant, version, dag string) {
	t.Helper()
	tenant = mustUUID(t, pool, ctx, `INSERT INTO tenants (name) VALUES ($1) RETURNING id::text`, name)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenant) })
	dag = mustUUID(t, pool, ctx, `INSERT INTO dags (tenant_id, dag_id) VALUES ($1, 'retention_dag') RETURNING id::text`, tenant)
	version = mustUUID(t, pool, ctx, `INSERT INTO dag_versions (dag_id, version, image_reference, spec, spec_hash)
		VALUES ($1, 'v1', 'img:v1', '{}'::jsonb, 'h') RETURNING id::text`, dag)
	return tenant, version, dag
}

// seedRun inserts a run that ended `ago` before now (nil leaves ended_at NULL)
// with one task instance per given state, each with a state-history row, an
// archived attempt and an XCom index row.
func seedRun(t *testing.T, pool *pgxpool.Pool, ctx context.Context, tenant, dag, version, runID, state string, ago *time.Duration, tiStates ...string) string {
	t.Helper()
	var ended any
	if ago != nil {
		ended = time.Now().Add(-*ago)
	}
	id := mustUUID(t, pool, ctx, `INSERT INTO dag_runs (tenant_id, dag_id, dag_version_id, run_id, logical_date, state, trigger, ended_at)
		VALUES ($1, $2, $3, $4, now(), $5::dag_run_state, 'manual', $6) RETURNING id::text`, tenant, dag, version, runID, state, ended)
	for i, st := range tiStates {
		ti := mustUUID(t, pool, ctx, `INSERT INTO task_instances (tenant_id, dag_run_id, task_id, state, operator)
			VALUES ($1, $2, $3, $4::task_state, 'python') RETURNING id::text`, tenant, id, fmt.Sprintf("t%d", i), st)
		mustExec(t, pool, ctx, `INSERT INTO task_state_history (task_instance_id, from_state, to_state) VALUES ($1, 'none', $2::task_state)`, ti, st)
		mustExec(t, pool, ctx, `INSERT INTO task_state_history (task_instance_id, from_state, to_state) VALUES ($1, 'queued', 'running')`, ti)
		mustExec(t, pool, ctx, `INSERT INTO task_instance_history (task_instance_id, try_number, state) VALUES ($1, 1, 'failed')`, ti)
		mustExec(t, pool, ctx, `INSERT INTO xcom_index (tenant_id, dag_run_id, task_id, key, redis_key, size_bytes, expires_at)
			VALUES ($1, $2, $3, 'return_value', 'k', 1, now() + interval '1 day')`, tenant, id, fmt.Sprintf("t%d", i))
	}
	return id
}

func seedRetention(t *testing.T, pool *pgxpool.Pool, ctx context.Context) retentionFixture {
	t.Helper()
	suffix := time.Now().UnixNano()
	tenant, version, dag := seedTenant(t, pool, ctx, fmt.Sprintf("retention_%d", suffix))
	other, otherVersion, otherDag := seedTenant(t, pool, ctx, fmt.Sprintf("retention_other_%d", suffix))
	old := 400 * 24 * time.Hour
	recent := 24 * time.Hour
	f := retentionFixture{tenant: tenant, otherTenant: other, runs: map[string]string{}}
	f.runs["old_success"] = seedRun(t, pool, ctx, tenant, dag, version, "old_success", "success", &old, "success", "success")
	f.runs["old_failed"] = seedRun(t, pool, ctx, tenant, dag, version, "old_failed", "failed", &old, "failed", "upstream_failed", "none")
	f.runs["recent"] = seedRun(t, pool, ctx, tenant, dag, version, "recent", "success", &recent, "success")
	f.runs["running"] = seedRun(t, pool, ctx, tenant, dag, version, "running", "running", nil, "running")
	f.runs["old_running"] = seedRun(t, pool, ctx, tenant, dag, version, "old_running", "running", &old, "success")
	f.runs["old_active_ti"] = seedRun(t, pool, ctx, tenant, dag, version, "old_active_ti", "failed", &old, "failed", "up_for_retry")
	f.runs["old_staging"] = seedRun(t, pool, ctx, tenant, dag, version, "old_staging", "success", &old, "success")
	mustExec(t, pool, ctx, `INSERT INTO staging_volumes (tenant_id, dag_id, run_id, pvc_name) VALUES ($1, 'retention_dag', $2, $3)`,
		tenant, f.runs["old_staging"], fmt.Sprintf("pvc-%d", suffix))
	f.runs["other_old"] = seedRun(t, pool, ctx, other, otherDag, otherVersion, "other_old", "success", &old, "success")
	return f
}

func runExists(t *testing.T, pool *pgxpool.Pool, ctx context.Context, id string) bool {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM dag_runs WHERE id = $1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func countFor(t *testing.T, pool *pgxpool.Pool, ctx context.Context, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", sql, err)
	}
	return n
}

// TestRetentionDeletesOnlyExpiredSettledRunsIntegration: one call per batch
// removes only finished runs past the cutoff, with no active task instance and
// no live staging volume, in the asked tenant only, together with every child
// row; everything else survives.
func TestRetentionDeletesOnlyExpiredSettledRunsIntegration(t *testing.T) {
	store, pool, ctx := openRetention(t)
	f := seedRetention(t, pool, ctx)
	cutoff := time.Now().Add(-30 * 24 * time.Hour)

	// rowLimit 1 forces every child table through several statements.
	var total retention.Counts
	for i := 0; i < 10; i++ {
		c, err := store.DeleteFinishedRuns(ctx, f.tenant, cutoff, 1, 1)
		if err != nil {
			t.Fatalf("DeleteFinishedRuns: %v", err)
		}
		if c.DagRuns > 1 {
			t.Fatalf("batch removed %d runs, want at most maxRuns=1", c.DagRuns)
		}
		total = total.Add(c)
		if c.DagRuns == 0 {
			break
		}
	}
	want := retention.Counts{DagRuns: 2, TaskInstances: 5, TaskStateHistory: 10, TaskInstanceHistory: 5, XComIndex: 5}
	if total != want {
		t.Fatalf("deleted %+v, want %+v", total, want)
	}
	for _, name := range []string{"old_success", "old_failed"} {
		if runExists(t, pool, ctx, f.runs[name]) {
			t.Errorf("%s survived retention", name)
		}
	}
	for _, name := range []string{"recent", "running", "old_running", "old_active_ti", "old_staging", "other_old"} {
		if !runExists(t, pool, ctx, f.runs[name]) {
			t.Errorf("%s was deleted, want it kept", name)
		}
	}
	for _, id := range []string{f.runs["old_success"], f.runs["old_failed"]} {
		if n := countFor(t, pool, ctx, `SELECT count(*) FROM task_instances WHERE dag_run_id = $1`, id); n != 0 {
			t.Errorf("%d task instances of a deleted run remain", n)
		}
		if n := countFor(t, pool, ctx, `SELECT count(*) FROM xcom_index WHERE dag_run_id = $1`, id); n != 0 {
			t.Errorf("%d xcom rows of a deleted run remain", n)
		}
	}
	if n := countFor(t, pool, ctx, `SELECT count(*) FROM task_state_history h JOIN task_instances ti ON ti.id = h.task_instance_id WHERE ti.dag_run_id = $1`, f.runs["old_active_ti"]); n != 4 {
		t.Errorf("history of a kept run = %d rows, want 4", n)
	}
}

// TestRetentionAuditLogIntegration: audit rows older than the cutoff go, in
// bounded batches, per tenant; the tenant-less system rows are their own call.
func TestRetentionAuditLogIntegration(t *testing.T) {
	store, pool, ctx := openRetention(t)
	f := seedRetention(t, pool, ctx)
	action := fmt.Sprintf("retention.test.%d", time.Now().UnixNano())
	for _, tenant := range []any{f.tenant, f.tenant, f.tenant, f.otherTenant, nil} {
		mustExec(t, pool, ctx, `INSERT INTO audit_log (tenant_id, action, occurred_at) VALUES ($1, $2, now() - interval '400 days')`, tenant, action)
	}
	mustExec(t, pool, ctx, `INSERT INTO audit_log (tenant_id, action) VALUES ($1, $2)`, f.tenant, action)
	cutoff := time.Now().Add(-90 * 24 * time.Hour)

	n, err := store.DeleteAuditLog(ctx, f.tenant, cutoff, 2)
	if err != nil || n != 2 {
		t.Fatalf("first batch = %d, %v; want 2 (the limit)", n, err)
	}
	n, err = store.DeleteAuditLog(ctx, f.tenant, cutoff, 2)
	if err != nil || n != 1 {
		t.Fatalf("second batch = %d, %v; want 1", n, err)
	}
	if left := countFor(t, pool, ctx, `SELECT count(*) FROM audit_log WHERE action = $1 AND tenant_id = $2`, action, f.tenant); left != 1 {
		t.Errorf("tenant rows left = %d, want the recent one", left)
	}
	if left := countFor(t, pool, ctx, `SELECT count(*) FROM audit_log WHERE action = $1 AND tenant_id = $2`, action, f.otherTenant); left != 1 {
		t.Errorf("other tenant rows left = %d, want 1 (not in scope)", left)
	}
	if _, err := store.DeleteAuditLog(ctx, "", cutoff, 1000); err != nil {
		t.Fatalf("system rows: %v", err)
	}
	if left := countFor(t, pool, ctx, `SELECT count(*) FROM audit_log WHERE action = $1 AND tenant_id IS NULL`, action); left != 0 {
		t.Errorf("system rows left = %d, want 0", left)
	}
}

// TestRetentionCountEligibleIntegration: the dry-run count applies the same
// predicate as the delete and deletes nothing.
func TestRetentionCountEligibleIntegration(t *testing.T) {
	store, pool, ctx := openRetention(t)
	runCutoff := time.Now().Add(-30 * 24 * time.Hour)
	auditCutoff := time.Now().Add(-90 * 24 * time.Hour)
	before, err := store.CountEligible(ctx, &runCutoff, &auditCutoff)
	if err != nil {
		t.Fatalf("CountEligible: %v", err)
	}
	f := seedRetention(t, pool, ctx)
	mustExec(t, pool, ctx, `INSERT INTO audit_log (tenant_id, action, occurred_at) VALUES ($1, 'retention.count', now() - interval '400 days')`, f.tenant)
	after, err := store.CountEligible(ctx, &runCutoff, &auditCutoff)
	if err != nil {
		t.Fatalf("CountEligible: %v", err)
	}
	// old_success, old_failed and the other tenant's old run are eligible.
	if d := after.DagRuns - before.DagRuns; d != 3 {
		t.Errorf("eligible runs delta = %d, want 3", d)
	}
	if d := after.TaskInstances - before.TaskInstances; d != 6 {
		t.Errorf("eligible task instances delta = %d, want 6", d)
	}
	if d := after.AuditLog - before.AuditLog; d != 1 {
		t.Errorf("eligible audit delta = %d, want 1", d)
	}
	if !runExists(t, pool, ctx, f.runs["old_success"]) {
		t.Error("CountEligible deleted a run")
	}
	runsOnly, err := store.CountEligible(ctx, &runCutoff, nil)
	if err != nil || runsOnly.AuditLog != 0 {
		t.Errorf("audit counted with its class off: %+v, %v", runsOnly, err)
	}
}

// TestRetentionTenantIDsIntegration lists every tenant, including new ones.
func TestRetentionTenantIDsIntegration(t *testing.T) {
	store, pool, ctx := openRetention(t)
	f := seedRetention(t, pool, ctx)
	ids, err := store.TenantIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		seen[id] = true
	}
	if !seen[f.tenant] || !seen[f.otherTenant] {
		t.Fatalf("TenantIDs missing the seeded tenants: %v", ids)
	}
}
