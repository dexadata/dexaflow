//go:build integration

package storage_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
)

// BenchmarkActiveRunsIntegration measures SchedulerStore.ActiveRuns against a
// real Postgres: the read every scheduler tick starts with, before planning
// (#2324). BenchmarkStepSteadyState runs on a fake store and cannot see it.
//
// It seeds 200 running runs of one DAG with 500 tasks each, 100,000 task
// instances in mixed states with the text columns a real attempt fills, then
// reads them all once per iteration. The spec cache is warm after the first
// iteration, as it is on a leader. Run it on a database of its own, since every
// active run in the database is read:
//
//	make bench-integration
//
// It reports runs and task_instances per op next to time and memory.
func BenchmarkActiveRunsIntegration(b *testing.B) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		b.Skip("DATABASE_URL must point at a migrated database of its own")
	}
	ctx := context.Background()
	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: url})
	if err != nil {
		b.Fatalf("connect: %v", err)
	}
	b.Cleanup(pg.Close)
	const runs, tasksPerRun = 200, 500
	seedActiveRuns(b, ctx, pg, runs, tasksPerRun)
	sched := storage.NewSchedulerStore(pg)

	var got []int
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		rs, err := sched.ActiveRuns(ctx)
		if err != nil {
			b.Fatalf("ActiveRuns: %v", err)
		}
		tis := 0
		for _, r := range rs {
			tis += len(r.States)
		}
		got = []int{len(rs), tis}
	}
	b.StopTimer()
	if got[0] < runs || got[1] < runs*tasksPerRun {
		b.Fatalf("read %d runs and %d task instances, want at least %d and %d", got[0], got[1], runs, runs*tasksPerRun)
	}
	b.ReportMetric(float64(got[0]), "runs/op")
	b.ReportMetric(float64(got[1]), "task_instances/op")
}

// seedActiveRuns registers, once per database, one DAG of tasksPerRun tasks and starts runs of it,
// with task instances spread over the states of a busy run: 40% success, 20%
// running, 20% none, 10% queued, 10% failed. Settled and running attempts carry
// a pod, node and host name and a log URL, failed ones an error message, as a
// real attempt does, because their width is what the read moves.
func seedActiveRuns(b *testing.B, ctx context.Context, pg *storage.Postgres, runs, tasksPerRun int) {
	b.Helper()
	repo := storage.NewRepository(pg)
	sched := storage.NewSchedulerStore(pg)
	// One fixed DAG: -count runs the benchmark again on the same database, and
	// each run must read the same 200 runs, not add 200 more.
	const dagID = "bench_active_runs"
	var seededBefore bool
	if err := pg.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM dags WHERE dag_id = $1)`, dagID).Scan(&seededBefore); err != nil {
		b.Fatalf("checking for an earlier seed: %v", err)
	}
	if seededBefore {
		return
	}
	tasks := make([]domain.TaskSpec, tasksPerRun)
	retries := 2
	for i := range tasks {
		tasks[i] = domain.TaskSpec{TaskID: fmt.Sprintf("transform_customer_segment_%04d", i), Type: domain.TaskTypePython, Retries: &retries}
		if i > 0 {
			tasks[i].DependsOn = []string{tasks[(i-1)/2].TaskID}
		}
	}
	spec := domain.DAGSpec{SchemaVersion: "1.0", DagID: dagID, DagVersion: "v1", Image: "registry.example.com/data/etl:2026.11.06", Tasks: tasks, MaxActiveRuns: runs}
	hash, err := spec.CanonicalHash()
	if err != nil {
		b.Fatal(err)
	}
	if _, err := repo.RegisterDagVersion(ctx, "default", spec, hash); err != nil {
		b.Fatalf("register: %v", err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for r := range runs {
		if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
			RunID: fmt.Sprintf("scheduled__%s", base.Add(time.Duration(r)*time.Hour).Format(time.RFC3339)),
			State: domain.DagRunStateRunning, RunType: "scheduled", LogicalDate: base.Add(time.Duration(r) * time.Hour),
		}); err != nil {
			b.Fatalf("CreateDagRun %d: %v", r, err)
		}
	}
	active, err := sched.ActiveRuns(ctx)
	if err != nil {
		b.Fatalf("ActiveRuns: %v", err)
	}
	seeded := 0
	for _, run := range active {
		if run.DagID != dagID {
			continue
		}
		if err := sched.MaterializeTasks(ctx, run.RunID, tasks); err != nil {
			b.Fatalf("MaterializeTasks %s: %v", run.DisplayRunID, err)
		}
		seeded++
	}
	if seeded != runs {
		b.Fatalf("materialized %d runs, want %d", seeded, runs)
	}
	if _, err := pg.Pool.Exec(ctx, `
UPDATE task_instances ti SET
  state = s.state::task_state,
  try_number = 1,
  queued_at = CASE WHEN s.state <> 'none' THEN now() - interval '20 minutes' END,
  started_at = CASE WHEN s.state IN ('running', 'success', 'failed') THEN now() - interval '15 minutes' END,
  ended_at = CASE WHEN s.state IN ('success', 'failed') THEN now() - interval '5 minutes' END,
  pod_name = CASE WHEN s.state <> 'none' THEN 'lf-' || left(md5(ti.id::text), 12) || '-' || ti.task_id END,
  node_name = CASE WHEN s.state <> 'none' THEN 'ip-10-20-' || (abs(hashtext(ti.id::text)) % 250) || '-31.eu-west-1.compute.internal' END,
  hostname = CASE WHEN s.state <> 'none' THEN 'lf-' || left(md5(ti.id::text), 12) END,
  log_url = CASE WHEN s.state <> 'none' THEN 's3://dexaflow-logs/tenant=default/dag=' || $1 || '/run=' || ti.dag_run_id || '/task=' || ti.task_id || '/attempt=1.log' END,
  error_message = CASE WHEN s.state = 'failed' THEN 'Traceback (most recent call last): File "/opt/dags/etl.py", line 212, in transform: KeyError: ''customer_segment'' raised while joining the orders frame with the segments frame' END
FROM (
  SELECT id, CASE WHEN b < 40 THEN 'success' WHEN b < 60 THEN 'running' WHEN b < 80 THEN 'none' WHEN b < 90 THEN 'queued' ELSE 'failed' END AS state
  FROM (SELECT id, abs(hashtext(id::text)) % 100 AS b FROM task_instances) h
) s
WHERE ti.id = s.id AND ti.dag_run_id IN (SELECT r.id FROM dag_runs r JOIN dags d ON d.id = r.dag_id WHERE d.dag_id = $1)`, dagID); err != nil {
		b.Fatalf("spreading task instance states: %v", err)
	}
	if _, err := pg.Pool.Exec(ctx, `ANALYZE task_instances; ANALYZE dag_runs`); err != nil {
		b.Fatalf("analyze: %v", err)
	}
}
