//go:build integration

package storage_test

import (
	"context"
	"os"
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
	"github.com/dexadata/dexaflow/internal/tenantctx"
)

// BenchmarkRowSecurityReadsIntegration measures what row level security
// (ADR 0104) costs on the scheduler's tick read and on two UI reads, on the
// data BenchmarkActiveRunsIntegration seeds (200 running runs, 100,000 task
// instances). Run it three ways and compare:
//
//   - as the test DSN's role with the tables off (the baseline);
//   - as a role without superuser or BYPASSRLS, after `dexaflow-migrate rls on`,
//     with BENCH_ROW_LEVEL_SECURITY=1 so the UI reads are bound to their
//     tenant;
//   - the same role and tables with BENCH_ROW_LEVEL_SECURITY unset.
//
// The scheduler read is never bound, so it measures the policy's cost on an
// unbound connection.
func BenchmarkRowSecurityReadsIntegration(b *testing.B) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		b.Skip("DATABASE_URL must point at a migrated database of its own")
	}
	ctx := context.Background()
	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: url, RowLevelSecurity: os.Getenv("BENCH_ROW_LEVEL_SECURITY") == "1"})
	if err != nil {
		b.Fatalf("connect: %v", err)
	}
	b.Cleanup(pg.Close)
	seedActiveRuns(b, ctx, pg, 200, 500)
	sched := storage.NewSchedulerStore(pg)
	repo := storage.NewRepository(pg)
	bound := tenantctx.With(ctx, "default")
	runs, _, err := repo.ListDagRuns(bound, "default", "bench_active_runs", 1, 0)
	if err != nil || len(runs) == 0 {
		b.Fatalf("ListDagRuns: %v (%d runs)", err, len(runs))
	}
	runID := runs[0].RunID

	b.Run("scheduler_active_runs", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := sched.ActiveRuns(ctx); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("ui_list_dag_runs", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, _, err := repo.ListDagRuns(bound, "default", "bench_active_runs", 50, 0); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("ui_list_task_instances", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, _, err := repo.ListTaskInstancesWithFilter(bound, "default", "bench_active_runs", runID, domain.TaskInstanceFilter{Limit: 100}); err != nil {
				b.Fatal(err)
			}
		}
	})
}
