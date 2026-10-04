package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// tenantName owns every row the experiment writes, so the dataset is found,
// reused and dropped by one key and never mixes with real tenants' data.
const tenantName = "load-query-plans"

// dataset sizes the synthetic data. The defaults match the dataset the
// performance review measured on.
type dataset struct {
	dags         int // DAGs in the tenant; every 20th is inactive, every 10th paused
	runsPerDag   int // hourly runs per DAG, newest first
	tasksPerRun  int // task instances per run
	auditRows    int // audit_log rows
	versions     int // versions of the DAG measured by DeleteDag
	stagingRows  int // active staging volumes
	runningEvery int // every Nth DAG has its newest run (and its tasks) running
}

// deleteTarget is the DAG the DeleteDag case removes, inside a rolled back
// transaction. It carries d.versions versions.
const deleteTarget = "qp_dag_00001"

// findTenant returns the experiment tenant's id, or "" when it does not exist.
func findTenant(ctx context.Context, pool *pgxpool.Pool) (string, error) {
	var id string
	err := pool.QueryRow(ctx, `SELECT id::text FROM tenants WHERE name = $1`, tenantName).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// seed writes the dataset in a fresh tenant with set based INSERTs and returns
// the tenant id. It then ANALYZEs the touched tables so plans reflect the data.
func seed(ctx context.Context, pool *pgxpool.Pool, d dataset) (string, error) {
	var tenant string
	if err := pool.QueryRow(ctx,
		`INSERT INTO tenants (name, display_name) VALUES ($1, 'Load experiment: query plans') RETURNING id::text`,
		tenantName).Scan(&tenant); err != nil {
		return "", fmt.Errorf("create tenant: %w", err)
	}
	steps := []struct {
		what string
		sql  string
		args []any
	}{
		{"dags", `
INSERT INTO dags (tenant_id, dag_id, is_active, is_paused, schedule, start_date)
SELECT $1::uuid, 'qp_dag_' || lpad(g::text, 5, '0'), g % 20 <> 0, g % 10 = 0, '@hourly', now() - interval '365 days'
FROM generate_series(1, $2::int) g`, []any{tenant, d.dags}},
		{"dag_versions", `
INSERT INTO dag_versions (dag_id, version, image_reference, spec, spec_hash)
SELECT d.id, 'v' || v, 'load/query-plans:' || v,
       jsonb_build_object('dag_id', d.dag_id, 'version', v, 'tasks', '[]'::jsonb),
       md5(d.dag_id || ':' || v)
FROM dags d
CROSS JOIN LATERAL generate_series(1, CASE WHEN d.dag_id = $2 THEN $3::int ELSE 1 END) v
WHERE d.tenant_id = $1::uuid`, []any{tenant, deleteTarget, d.versions}},
		{"current versions", `
UPDATE dags d SET current_version_id = (
    SELECT id FROM dag_versions v WHERE v.dag_id = d.id ORDER BY v.created_at DESC, v.version DESC LIMIT 1)
WHERE d.tenant_id = $1::uuid`, []any{tenant}},
		{"dag_runs", `
INSERT INTO dag_runs (tenant_id, dag_id, dag_version_id, run_id, logical_date,
                      data_interval_start, data_interval_end, state, trigger,
                      queued_at, started_at, ended_at)
SELECT d.tenant_id, d.id, d.current_version_id,
       'scheduled__' || to_char(ld, 'YYYY-MM-DD"T"HH24:MI:SS'), ld, ld - interval '1 hour', ld,
       s.state, 'scheduled', ld, ld + interval '1 minute',
       CASE WHEN s.state = 'running' THEN NULL ELSE ld + interval '10 minutes' END
FROM dags d
CROSS JOIN LATERAL generate_series(1, $2::int) g
CROSS JOIN LATERAL (SELECT date_trunc('hour', now()) - (g - 1) * interval '1 hour' AS ld) t
CROSS JOIN LATERAL (SELECT (CASE
        WHEN g = 1 AND right(d.dag_id, 5)::int % $3::int = 0 THEN 'running'
        WHEN g % 17 = 0 THEN 'failed'
        ELSE 'success' END)::dag_run_state AS state) s
WHERE d.tenant_id = $1::uuid`, []any{tenant, d.runsPerDag, d.runningEvery}},
		{"task_instances", `
INSERT INTO task_instances (tenant_id, dag_run_id, task_id, try_number, state, operator,
                            queued_at, started_at, ended_at, duration_seconds)
SELECT r.tenant_id, r.id, 'task_' || t, 1, s.state, 'python',
       r.started_at, r.started_at + interval '10 seconds',
       CASE WHEN s.state = 'running' THEN NULL ELSE r.started_at + interval '5 minutes' END,
       CASE WHEN s.state = 'running' THEN NULL ELSE 290 END
FROM dag_runs r
CROSS JOIN LATERAL generate_series(1, $2::int) t
CROSS JOIN LATERAL (SELECT (CASE
        WHEN r.state = 'running' THEN 'running'
        WHEN r.state = 'failed' AND t = $2::int THEN 'failed'
        ELSE 'success' END)::task_state AS state) s
WHERE r.tenant_id = $1::uuid`, []any{tenant, d.tasksPerRun}},
		{"audit_log", `
INSERT INTO audit_log (tenant_id, action, resource_type, resource_id, metadata, occurred_at)
SELECT $1::uuid, (ARRAY['dag.trigger', 'dag.pause', 'dag.update', 'task.clear'])[1 + g % 4], 'dag',
       'qp_dag_' || lpad((1 + g % $3::int)::text, 5, '0'), '{}'::jsonb, now() - g * interval '1 second'
FROM generate_series(1, $2::int) g`, []any{tenant, d.auditRows, d.dags}},
		{"staging_volumes", `
INSERT INTO staging_volumes (tenant_id, dag_id, run_id, pvc_name, size)
SELECT r.tenant_id, 'qp', r.id::text, 'qp-staging-' || r.id, '1Gi'
FROM dag_runs r
WHERE r.tenant_id = $1::uuid AND r.state = 'running'
LIMIT $2`, []any{tenant, d.stagingRows}},
	}
	for _, s := range steps {
		start := time.Now()
		tag, err := pool.Exec(ctx, s.sql, s.args...)
		if err != nil {
			return tenant, fmt.Errorf("seed %s: %w", s.what, err)
		}
		slog.Info("seeded", "table", s.what, "rows", tag.RowsAffected(), "took", time.Since(start).Round(time.Millisecond))
	}
	start := time.Now()
	if _, err := pool.Exec(ctx, `ANALYZE dags, dag_versions, dag_runs, task_instances, audit_log, staging_volumes`); err != nil {
		return tenant, fmt.Errorf("analyze: %w", err)
	}
	slog.Info("analyzed", "took", time.Since(start).Round(time.Millisecond))
	return tenant, nil
}

// drop deletes the experiment tenant and everything it owns. Child tables go
// first: dag_runs.dag_version_id has no index, so letting the tenant cascade
// would check every deleted version against every remaining run.
func drop(ctx context.Context, pool *pgxpool.Pool, tenant string) error {
	steps := []struct{ what, sql string }{
		{"staging_volumes", `DELETE FROM staging_volumes WHERE tenant_id = $1::uuid`},
		{"audit_log", `DELETE FROM audit_log WHERE tenant_id = $1::uuid`},
		{"task_instances", `DELETE FROM task_instances WHERE tenant_id = $1::uuid`},
		{"dag_runs", `DELETE FROM dag_runs WHERE tenant_id = $1::uuid`},
		{"current versions", `UPDATE dags SET current_version_id = NULL WHERE tenant_id = $1::uuid`},
		{"dag_versions", `DELETE FROM dag_versions v USING dags d WHERE v.dag_id = d.id AND d.tenant_id = $1::uuid`},
		{"dags", `DELETE FROM dags WHERE tenant_id = $1::uuid`},
		{"tenant", `DELETE FROM tenants WHERE id = $1::uuid`},
	}
	for _, s := range steps {
		start := time.Now()
		tag, err := pool.Exec(ctx, s.sql, tenant)
		if err != nil {
			return fmt.Errorf("drop %s: %w", s.what, err)
		}
		slog.Info("dropped", "table", s.what, "rows", tag.RowsAffected(), "took", time.Since(start).Round(time.Millisecond))
	}
	return nil
}
