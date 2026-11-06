//go:build integration

package storage_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// countingDBTX wraps the pool and counts the task-instance list queries the
// scheduler read issues, so a test can pin how many round trips one tick costs.
type countingDBTX struct {
	pool    *pgxpool.Pool
	tiReads atomic.Int64
}

func (c *countingDBTX) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return c.pool.Exec(ctx, sql, args...)
}

func (c *countingDBTX) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if strings.Contains(sql, "-- name: ListTaskInstancePlanStateByRuns") {
		c.tiReads.Add(1)
	}
	return c.pool.Query(ctx, sql, args...)
}

func (c *countingDBTX) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return c.pool.QueryRow(ctx, sql, args...)
}

func (c *countingDBTX) CopyFrom(ctx context.Context, table pgx.Identifier, cols []string, src pgx.CopyFromSource) (int64, error) {
	return c.pool.CopyFrom(ctx, table, cols, src)
}

// TestActiveRunsLoadsTaskInstancesInOneQueryIntegration pins that a scheduler
// read loads every active run's task instances in ONE query, not one per run,
// and that each run still projects exactly the rows its own per-run query
// returns.
func TestActiveRunsLoadsTaskInstancesInOneQueryIntegration(t *testing.T) {
	repo, sched, pg, ctx := openInfra(t)
	stamp := time.Now().UnixNano()
	tasks := []domain.TaskSpec{
		{TaskID: "a", Type: domain.TaskTypePython},
		{TaskID: "b", Type: domain.TaskTypePython, DependsOn: []string{"a"}, Retries: retriesPtr(2)},
		{TaskID: "c", Type: domain.TaskTypePython, DependsOn: []string{"a"}},
	}
	// Two DAGs, two runs each, every run in a different task-state mix, so a
	// grouping bug (rows landing in the wrong run) shows up as a state mismatch.
	runStates := [][]domain.TaskState{
		{domain.TaskStateSuccess, domain.TaskStateRunning, domain.TaskStateNone},
		{domain.TaskStateRunning, domain.TaskStateNone, domain.TaskStateNone},
		{domain.TaskStateSuccess, domain.TaskStateFailed, domain.TaskStateQueued},
		{domain.TaskStateNone, domain.TaskStateNone, domain.TaskStateNone},
	}
	var runUUIDs []string
	for d := range 2 {
		dagID := fmt.Sprintf("ti_batch_%d_%d", stamp, d)
		registerSpec(t, repo, ctx, dagID, tasks)
		for r := range 2 {
			if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
				RunID: fmt.Sprintf("r%d", r), State: domain.DagRunStateRunning, RunType: "manual",
				LogicalDate: time.Now().UTC().Add(time.Duration(r) * time.Hour),
			}); err != nil {
				t.Fatalf("create run: %v", err)
			}
		}
		for _, run := range mustActive(t, sched, ctx) {
			if run.DagID != dagID {
				continue
			}
			if err := sched.MaterializeTasks(ctx, run.RunID, tasks); err != nil {
				t.Fatalf("materialize: %v", err)
			}
			states := runStates[len(runUUIDs)]
			for i, st := range states {
				if st == domain.TaskStateNone {
					continue
				}
				if err := sched.ApplyTransition(ctx, run.RunID, tasks[i].TaskID, st); err != nil {
					t.Fatalf("transition: %v", err)
				}
			}
			runUUIDs = append(runUUIDs, run.RunID)
		}
	}
	if len(runUUIDs) != 4 {
		t.Fatalf("expected 4 runs, got %d", len(runUUIDs))
	}

	counter := &countingDBTX{pool: pg.Pool}
	counted := storage.NewSchedulerStore(&storage.Postgres{Pool: pg.Pool, Queries: queries.New(counter)})
	runs, err := counted.ActiveRuns(ctx)
	if err != nil {
		t.Fatalf("ActiveRuns: %v", err)
	}
	if got := counter.tiReads.Load(); got != 1 {
		t.Errorf("ActiveRuns issued %d task-instance queries for %d active runs, want 1", got, len(runs))
	}

	tenant, err := pg.Queries.GetDefaultTenant(ctx)
	if err != nil {
		t.Fatalf("default tenant: %v", err)
	}
	byID := make(map[string]int, len(runs))
	for i, r := range runs {
		byID[r.RunID] = i
	}
	for _, id := range runUUIDs {
		i, ok := byID[id]
		if !ok {
			t.Fatalf("run %s missing from ActiveRuns", id)
		}
		got := runs[i]
		var key pgtype.UUID
		if err := key.Scan(id); err != nil {
			t.Fatal(err)
		}
		want, err := pg.Queries.ListTaskInstancesByRun(ctx, queries.ListTaskInstancesByRunParams{TenantID: tenant.ID, DagRunID: key})
		if err != nil {
			t.Fatalf("per-run query: %v", err)
		}
		if len(got.States) != len(want) {
			t.Fatalf("run %s: %d states, want %d", id, len(got.States), len(want))
		}
		for _, ti := range want {
			if got.States[ti.TaskID] != domain.TaskState(ti.State) {
				t.Errorf("run %s task %s: state %s, want %s", id, ti.TaskID, got.States[ti.TaskID], ti.State)
			}
			if got.Tries[ti.TaskID] != int(ti.TryNumber) || got.MaxTries[ti.TaskID] != int(ti.MaxTries) {
				t.Errorf("run %s task %s: tries %d/%d, want %d/%d", id, ti.TaskID,
					got.Tries[ti.TaskID], got.MaxTries[ti.TaskID], ti.TryNumber, ti.MaxTries)
			}
		}
	}
}

// The tick reads through the hand-written sized scan (#2325); it must return
// exactly what the sqlc-generated query returns for the same runs, NULL
// columns included, so its column list cannot drift from the query's.
func TestPlanStateSizedScanMatchesGeneratedIntegration(t *testing.T) {
	repo, sched, pg, ctx := openInfra(t)
	dagID := fmt.Sprintf("ti_sized_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{
		{TaskID: "a", Type: domain.TaskTypePython},
		{TaskID: "b", Type: domain.TaskTypePython, DependsOn: []string{"a"}},
		{TaskID: "c", Type: domain.TaskTypePython, DependsOn: []string{"a"}},
	}
	registerSpec(t, repo, ctx, dagID, tasks)
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r0", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	var ids []pgtype.UUID
	for _, run := range mustActive(t, sched, ctx) {
		if run.DagID != dagID {
			continue
		}
		if err := sched.MaterializeTasks(ctx, run.RunID, tasks); err != nil {
			t.Fatalf("materialize: %v", err)
		}
		// One settled row with an end time, one with an infra mark, one
		// untouched: every nullable column is both set and NULL somewhere.
		if err := sched.ApplyTransition(ctx, run.RunID, "a", domain.TaskStateSuccess); err != nil {
			t.Fatalf("transition: %v", err)
		}
		var id pgtype.UUID
		if err := id.Scan(run.RunID); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if _, err := pg.Pool.Exec(ctx, `UPDATE task_instances SET state = 'failed', last_failure_kind = 'infra',
		infra_attempts = 1, next_dispatch_at = now(), dispatch_attempts = 2 WHERE dag_run_id = $1 AND task_id = 'b'`, ids[0]); err != nil {
		t.Fatalf("mark infra: %v", err)
	}
	want, err := pg.Queries.ListTaskInstancePlanStateByRuns(ctx, ids)
	if err != nil {
		t.Fatalf("generated: %v", err)
	}
	for _, hint := range []int{0, 1, 100} {
		got, err := pg.Queries.ListTaskInstancePlanStateByRunsSized(ctx, ids, hint)
		if err != nil {
			t.Fatalf("sized (hint %d): %v", hint, err)
		}
		byTask := func(rows []queries.ListTaskInstancePlanStateByRunsRow) map[string]queries.ListTaskInstancePlanStateByRunsRow {
			m := map[string]queries.ListTaskInstancePlanStateByRunsRow{}
			for _, r := range rows {
				m[r.TaskID] = r
			}
			return m
		}
		w, g := byTask(want), byTask(got)
		if len(w) != 3 || len(g) != len(w) {
			t.Fatalf("hint %d: got %d rows, want %d (and 3)", hint, len(g), len(w))
		}
		for task, wr := range w {
			gr := g[task]
			if !reflect.DeepEqual(gr, wr) {
				t.Errorf("hint %d task %s:\n got  %+v\n want %+v", hint, task, gr, wr)
			}
		}
	}
}
