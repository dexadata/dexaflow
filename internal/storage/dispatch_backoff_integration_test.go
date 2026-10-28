//go:build integration

package storage_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
	"github.com/dexadata/dexaflow/internal/scheduler"
	"github.com/dexadata/dexaflow/internal/storage"
)

type failingDispatcher struct{}

func (failingDispatcher) Dispatch(context.Context, string, string, string, domain.TaskSpec) (executor.Disposition, error) {
	return executor.Rejected, context.DeadlineExceeded // any non-nil permanent error → bounded backoff path
}

// TestDispatchBackoffPersists exercises the two dispatch-backoff queries and the
// ActiveRuns read of the new columns against real Postgres (ADR 0031 Amendment
// A). It verifies the migration applied and the queries round-trip; it does not
// depend on backoff timing.
func TestDispatchBackoffPersists(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must point at a migrated database")
	}
	ctx := context.Background()
	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: url})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pg.Close()

	repo := storage.NewRepository(pg)
	store := storage.NewSchedulerStore(pg)
	sched := scheduler.NewScheduler(store, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Millisecond)
	sched.SetDispatcher(failingDispatcher{})
	sched.SetLeading(true)

	// Unique per invocation so a reused (dirty) test DB never collides on the
	// dag-run insert — the fixed "backoff-run" id had no cleanup and failed the
	// second run with "resource already exists".
	suffix := time.Now().UnixNano()
	dagID := fmt.Sprintf("dispatch_backoff_test_%d", suffix)
	spec := domain.DAGSpec{
		SchemaVersion: "1.0", DagID: dagID, DagVersion: "v1", Image: "img:v1",
		Tasks: []domain.TaskSpec{{TaskID: "a", Type: domain.TaskTypePython, Entrypoint: "dag:a"}},
	}
	hash, err := spec.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	if _, rerr := repo.RegisterDagVersion(ctx, "default", spec, hash); rerr != nil {
		t.Fatalf("register version: %v", rerr)
	}
	if _, rerr := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: fmt.Sprintf("backoff-run-%d", suffix), State: domain.DagRunStateQueued, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); rerr != nil {
		t.Fatalf("create run: %v", rerr)
	}

	// Tick: materialize -> running -> none->scheduled -> scheduled->dispatch(fail).
	for i := 0; i < 4; i++ {
		if serr := sched.Step(ctx); serr != nil {
			t.Fatalf("step %d: %v", i, serr)
		}
	}

	runs, err := store.ActiveRuns(ctx)
	if err != nil {
		t.Fatalf("active runs: %v", err)
	}
	var run *scheduler.RunState
	for i := range runs {
		if runs[i].DagID == dagID {
			run = &runs[i]
		}
	}
	if run == nil {
		t.Fatal("run not found in ActiveRuns")
	}
	// RecordDispatchFailure ran at least once: the counter and the backoff stamp
	// round-tripped through the new columns, and the task is still scheduled.
	if run.DispatchAttempts["a"] < 1 {
		t.Errorf("dispatch_attempts should be >=1 after a failed dispatch, got %d", run.DispatchAttempts["a"])
	}
	if run.NextDispatchAt["a"] == nil {
		t.Error("next_dispatch_at should be set after a failed dispatch")
	}
	if run.States["a"] != domain.TaskStateScheduled {
		t.Errorf("task should still be scheduled during backoff, got %s", run.States["a"])
	}

	// FailDispatchExhausted transitions the scheduled task to failed.
	if ferr := store.FailDispatchExhausted(ctx, run.RunID, "a", "dispatch_failed: test"); ferr != nil {
		t.Fatalf("FailDispatchExhausted: %v", ferr)
	}
	runs, _ = store.ActiveRuns(ctx)
	for i := range runs {
		if runs[i].DagID == dagID && runs[i].States["a"] != domain.TaskStateFailed {
			t.Errorf("task should be failed after FailDispatchExhausted, got %s", runs[i].States["a"])
		}
	}
}

type refusingDispatcher struct{}

func (refusingDispatcher) Dispatch(context.Context, string, string, string, domain.TaskSpec) (executor.Disposition, error) {
	return executor.Refused, fmt.Errorf(`task "a" is size 80, above executor.unit.max_size 64`)
}

// TestDispatchRefusedSpendsTheRetryBudget: a Refused dispatch (ADR 0066 §3)
// fails the task for good against real Postgres. The row is failed with the
// refusal as its reason and its retry budget is spent, so the planner never
// moves it to up_for_retry and the run finalizes failed instead of refusing the
// task once per retry.
func TestDispatchRefusedSpendsTheRetryBudget(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must point at a migrated database")
	}
	ctx := context.Background()
	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: url})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pg.Close()

	repo := storage.NewRepository(pg)
	store := storage.NewSchedulerStore(pg)
	sched := scheduler.NewScheduler(store, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Millisecond)
	sched.SetDispatcher(refusingDispatcher{})
	sched.SetLeading(true)

	suffix := time.Now().UnixNano()
	dagID := fmt.Sprintf("dispatch_refused_test_%d", suffix)
	runID := fmt.Sprintf("refused-run-%d", suffix)
	retries := 2
	spec := domain.DAGSpec{
		SchemaVersion: "1.0", DagID: dagID, DagVersion: "v1", Image: "img:v1",
		Tasks: []domain.TaskSpec{{TaskID: "a", Type: domain.TaskTypePython, Entrypoint: "dag:a", Retries: &retries}},
	}
	hash, err := spec.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	if _, rerr := repo.RegisterDagVersion(ctx, "default", spec, hash); rerr != nil {
		t.Fatalf("register version: %v", rerr)
	}
	if _, rerr := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: runID, State: domain.DagRunStateQueued, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); rerr != nil {
		t.Fatalf("create run: %v", rerr)
	}

	// Tick: materialize -> running -> none->scheduled -> scheduled->dispatch(refused),
	// then a few more ticks in which a retriable failure would go to up_for_retry
	// and back to scheduled.
	for i := 0; i < 7; i++ {
		if serr := sched.Step(ctx); serr != nil {
			t.Fatalf("step %d: %v", i, serr)
		}
	}
	var state, msg string
	var try, maxTries int
	if qerr := pg.Pool.QueryRow(ctx,
		`SELECT ti.state::text, COALESCE(ti.error_message, ''), ti.try_number, ti.max_tries
		 FROM task_instances ti JOIN dag_runs dr ON dr.id = ti.dag_run_id
		 WHERE dr.run_id = $1 AND ti.task_id = 'a'`, runID,
	).Scan(&state, &msg, &try, &maxTries); qerr != nil {
		t.Fatalf("read task: %v", qerr)
	}
	if try != 1 || maxTries != 1 {
		t.Errorf("a refusal should spend the retry budget without counting a try: try=%d max_tries=%d, want 1 and 1",
			try, maxTries)
	}
	if state != string(domain.TaskStateFailed) {
		t.Errorf("a refused task must not be retried, state = %s", state)
	}
	if msg != `dispatch refused: task "a" is size 80, above executor.unit.max_size 64` {
		t.Errorf("error_message = %q, want the refusal", msg)
	}
	var runState string
	if qerr := pg.Pool.QueryRow(ctx, `SELECT state::text FROM dag_runs WHERE run_id = $1`, runID).Scan(&runState); qerr != nil {
		t.Fatalf("read run: %v", qerr)
	}
	if runState != string(domain.DagRunStateFailed) {
		t.Errorf("run state = %s, want failed", runState)
	}
}

// TestDispatchFailedDoesNotFallBackToTheRetryBudget: a task failed as
// dispatch_failed (its dispatch budget spent, on the synchronous path or in a
// buffered dispatch worker) is terminal. It must not move to up_for_retry and
// spend one of the user's retries on what was an infrastructure failure (ADR
// 0031 Amendment A: a dispatch failure never consumes try_number; ADR 0051:
// dispatch_failed is an infra-terminal state with no fallback to the retry
// budget).
func TestDispatchFailedDoesNotFallBackToTheRetryBudget(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must point at a migrated database")
	}
	ctx := context.Background()
	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: url})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pg.Close()

	for _, tc := range []struct {
		name string
		fail func(s *storage.SchedulerStore, runID string) error
	}{
		{"sync_exhausted", func(s *storage.SchedulerStore, runID string) error {
			return s.FailDispatchExhausted(ctx, runID, "a", "dispatch_failed after 6 attempts: test")
		}},
		{"buffered_exhausted", func(s *storage.SchedulerStore, runID string) error {
			return s.MarkTaskDispatchFailed(ctx, runID, "a", "dispatch_failed after 6 attempts: test")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := storage.NewRepository(pg)
			store := storage.NewSchedulerStore(pg)
			sched := scheduler.NewScheduler(store, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Millisecond)
			sched.SetDispatcher(failingDispatcher{})
			sched.SetLeading(true)

			suffix := time.Now().UnixNano()
			dagID := fmt.Sprintf("dispatch_failed_retry_%s_%d", tc.name, suffix)
			runID := fmt.Sprintf("dispatch-failed-run-%d", suffix)
			retries := 2
			spec := domain.DAGSpec{
				SchemaVersion: "1.0", DagID: dagID, DagVersion: "v1", Image: "img:v1",
				Tasks: []domain.TaskSpec{{TaskID: "a", Type: domain.TaskTypePython, Entrypoint: "dag:a", Retries: &retries}},
			}
			hash, err := spec.CanonicalHash()
			if err != nil {
				t.Fatal(err)
			}
			if _, rerr := repo.RegisterDagVersion(ctx, "default", spec, hash); rerr != nil {
				t.Fatalf("register version: %v", rerr)
			}
			if _, rerr := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
				RunID: runID, State: domain.DagRunStateQueued, RunType: "manual", LogicalDate: time.Now().UTC(),
			}); rerr != nil {
				t.Fatalf("create run: %v", rerr)
			}
			// materialize -> running -> none->scheduled -> dispatch fails once.
			for i := 0; i < 4; i++ {
				if serr := sched.Step(ctx); serr != nil {
					t.Fatalf("step %d: %v", i, serr)
				}
			}
			var runUUID string
			if qerr := pg.Pool.QueryRow(ctx, `SELECT id::text FROM dag_runs WHERE run_id = $1`, runID).Scan(&runUUID); qerr != nil {
				t.Fatalf("read run id: %v", qerr)
			}
			if ferr := tc.fail(store, runUUID); ferr != nil {
				t.Fatalf("failing the dispatch: %v", ferr)
			}
			// Ticks in which a retriable failure would go up_for_retry -> none ->
			// scheduled on a new try.
			for i := 0; i < 5; i++ {
				if serr := sched.Step(ctx); serr != nil {
					t.Fatalf("step after fail %d: %v", i, serr)
				}
			}
			var state string
			var try int
			if qerr := pg.Pool.QueryRow(ctx,
				`SELECT ti.state::text, ti.try_number FROM task_instances ti
				 WHERE ti.dag_run_id = $1::uuid AND ti.task_id = 'a'
				 ORDER BY ti.try_number DESC LIMIT 1`, runUUID,
			).Scan(&state, &try); qerr != nil {
				t.Fatalf("read task: %v", qerr)
			}
			if state != string(domain.TaskStateFailed) || try != 1 {
				t.Errorf("dispatch_failed must be terminal without spending a retry: state=%s try=%d, want failed and 1", state, try)
			}
			var runState string
			if qerr := pg.Pool.QueryRow(ctx, `SELECT state::text FROM dag_runs WHERE id = $1::uuid`, runUUID).Scan(&runState); qerr != nil {
				t.Fatalf("read run: %v", qerr)
			}
			if runState != string(domain.DagRunStateFailed) {
				t.Errorf("run state = %s, want failed", runState)
			}
		})
	}
}
