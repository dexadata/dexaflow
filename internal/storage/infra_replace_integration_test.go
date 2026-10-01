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

type okDispatcher struct{}

func (okDispatcher) Dispatch(context.Context, string, string, string, domain.TaskSpec) (executor.Disposition, error) {
	return executor.Dispatched, nil
}

// openInfra connects to the migrated test DB (or skips) and returns a repository,
// scheduler store, and the raw pool for direct-read assertions.
func openInfra(t *testing.T) (*storage.Repository, *storage.SchedulerStore, *storage.Postgres, context.Context) {
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
	return storage.NewRepository(pg), storage.NewSchedulerStore(pg), pg, ctx
}

// seedInfraFailed registers a one-task DAG, drives the task to 'running', then
// has the agent-lost reaper fail it as infra. Returns the display run id + UUID.
func seedInfraFailed(t *testing.T, repo *storage.Repository, sched *storage.SchedulerStore, pg *storage.Postgres, ctx context.Context, dagID string) (runID, runUUID string) {
	t.Helper()
	tasks := []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython}}
	registerSpec(t, repo, ctx, dagID, tasks)
	runID = "r1"
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: runID, State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	runUUID = resolveRunUUID(t, sched, ctx, dagID)
	if err := sched.MaterializeTasks(ctx, runUUID, tasks); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if err := sched.ApplyTransition(ctx, runUUID, "t", domain.TaskStateRunning); err != nil {
		t.Fatalf("transition running: %v", err)
	}
	var tiID string
	if err := pg.Pool.QueryRow(ctx, "SELECT id::text FROM task_instances WHERE dag_run_id=$1::uuid AND task_id='t'", runUUID).Scan(&tiID); err != nil {
		t.Fatalf("select ti id: %v", err)
	}
	ok, err := sched.MarkTaskAgentLost(ctx, tiID)
	if err != nil || !ok {
		t.Fatalf("MarkTaskAgentLost ok=%v err=%v", ok, err)
	}
	// #859: infra re-placement is now backoff-gated from ended_at (readyToInfraReplace).
	// These tests exercise the reap→re-place *loop*, not the backoff timing, so
	// backdate ended_at past the backoff window to make the seeded infra failure
	// immediately re-placeable. The timing gate itself is unit-tested in
	// plan_test.go (TestPlanRunInfraReplaceRespectsBackoff).
	if _, err := pg.Pool.Exec(ctx,
		"UPDATE task_instances SET ended_at = now() - interval '1 hour' WHERE dag_run_id=$1::uuid AND task_id='t'",
		runUUID); err != nil {
		t.Fatalf("backdate ended_at: %v", err)
	}
	return runID, runUUID
}

func lastFailureKind(t *testing.T, pg *storage.Postgres, ctx context.Context, runUUID string) string {
	t.Helper()
	var kind string
	if err := pg.Pool.QueryRow(ctx, "SELECT COALESCE(last_failure_kind,'') FROM task_instances WHERE dag_run_id=$1::uuid AND task_id='t'", runUUID).Scan(&kind); err != nil {
		t.Fatalf("select last_failure_kind: %v", err)
	}
	return kind
}

// TestInfraMarkerClearedByAdminClearFailed is the ADR 0051 Phase 1 regression for
// the stale-infra bug: the admin "clear failed" action MUST clear
// last_failure_kind. Otherwise a later APP failure on the same TI is misread as
// infra (state='failed' AND last_failure_kind='infra') and re-placed off the
// retry budget — the exact invariant violation the ADR exists to prevent.
func TestInfraMarkerClearedByAdminClearFailed(t *testing.T) {
	repo, sched, pg, ctx := openInfra(t)
	dagID := fmt.Sprintf("infra_clear_%d", time.Now().UnixNano())
	runID, runUUID := seedInfraFailed(t, repo, sched, pg, ctx, dagID)

	if k := lastFailureKind(t, pg, ctx, runUUID); k != "infra" {
		t.Fatalf("precondition: reaper should stamp last_failure_kind='infra', got %q", k)
	}
	// Admin "clear failed" (onlyFailed=true) — the buggy path.
	if _, err := repo.ClearTaskInstances(ctx, "default", dagID, runID, nil, true, domain.ClearOptions{}); err != nil {
		t.Fatalf("ClearTaskInstances: %v", err)
	}
	if k := lastFailureKind(t, pg, ctx, runUUID); k != "" {
		t.Errorf("clear-failed must clear last_failure_kind (a stale %q misclassifies a later app failure as infra), got %q", "infra", k)
	}
}

// TestInfraReplacePreservesRetryBudget: the infra re-place resets a failed+infra
// TI to 'none' PRESERVING try_number and bumping infra_attempts, clearing the
// marker.
func TestInfraReplacePreservesRetryBudget(t *testing.T) {
	repo, sched, pg, ctx := openInfra(t)
	dagID := fmt.Sprintf("infra_replace_%d", time.Now().UnixNano())
	_, runUUID := seedInfraFailed(t, repo, sched, pg, ctx, dagID)

	var tryBefore int
	if err := pg.Pool.QueryRow(ctx, "SELECT try_number FROM task_instances WHERE dag_run_id=$1::uuid AND task_id='t'", runUUID).Scan(&tryBefore); err != nil {
		t.Fatal(err)
	}
	applied, err := sched.ResetForInfraReplace(ctx, runUUID, "t")
	if err != nil || !applied {
		t.Fatalf("ResetForInfraReplace applied=%v err=%v", applied, err)
	}
	var tryAfter, infraAttempts int
	var state string
	if err := pg.Pool.QueryRow(ctx, "SELECT try_number, infra_attempts, state::text FROM task_instances WHERE dag_run_id=$1::uuid AND task_id='t'", runUUID).Scan(&tryAfter, &infraAttempts, &state); err != nil {
		t.Fatal(err)
	}
	if tryAfter != tryBefore {
		t.Errorf("infra re-place must preserve try_number: before=%d after=%d", tryBefore, tryAfter)
	}
	if infraAttempts != 1 {
		t.Errorf("infra_attempts should be 1 after one re-place, got %d", infraAttempts)
	}
	if state != "none" {
		t.Errorf("infra re-place should reset to none, got %q", state)
	}
	if k := lastFailureKind(t, pg, ctx, runUUID); k != "" {
		t.Errorf("infra re-place must clear last_failure_kind, got %q", k)
	}
}

// TestInfraReplaceIsAtMostOnce: two concurrent re-place attempts on the same
// failed+infra TI (a scheduler tick racing a stale duplicate) must result in
// EXACTLY ONE applied — the guarded UPDATE + row lock makes the loser match zero
// rows, so infra_attempts is bumped once, never double-counted.
func TestInfraReplaceIsAtMostOnce(t *testing.T) {
	repo, sched, pg, ctx := openInfra(t)
	dagID := fmt.Sprintf("infra_cas_%d", time.Now().UnixNano())
	_, runUUID := seedInfraFailed(t, repo, sched, pg, ctx, dagID)

	type res struct {
		applied bool
		err     error
	}
	out := make(chan res, 2)
	for i := 0; i < 2; i++ {
		go func() {
			applied, err := sched.ResetForInfraReplace(ctx, runUUID, "t")
			out <- res{applied, err}
		}()
	}
	wins := 0
	for i := 0; i < 2; i++ {
		r := <-out
		if r.err != nil {
			t.Fatalf("ResetForInfraReplace error: %v", r.err)
		}
		if r.applied {
			wins++
		}
	}
	if wins != 1 {
		t.Errorf("exactly one concurrent re-place must apply (at-most-once), got %d", wins)
	}
	var infraAttempts int
	if err := pg.Pool.QueryRow(ctx, "SELECT infra_attempts FROM task_instances WHERE dag_run_id=$1::uuid AND task_id='t'", runUUID).Scan(&infraAttempts); err != nil {
		t.Fatal(err)
	}
	if infraAttempts != 1 {
		t.Errorf("infra_attempts must be bumped exactly once under concurrency, got %d", infraAttempts)
	}
}

// TestChaosInfraReplaceReRunsWithoutFinalizing is the reap→re-place→re-run loop
// end-to-end (ADR 0051 Phase 1): a running task reaped as infra is re-placed and
// re-dispatched by the scheduler — the task recovers and the run stays ACTIVE,
// instead of the run finalizing as failed. Contrast the dispatch-lost chaos path,
// where a run with no recovery finalizes.
func TestChaosInfraReplaceReRunsWithoutFinalizing(t *testing.T) {
	repo, sched, pg, ctx := openInfra(t)
	dagID := fmt.Sprintf("infra_chaos_%d", time.Now().UnixNano())
	_, runUUID := seedInfraFailed(t, repo, sched, pg, ctx, dagID)

	var tryBefore int
	if err := pg.Pool.QueryRow(ctx, "SELECT try_number FROM task_instances WHERE dag_run_id=$1::uuid AND task_id='t'", runUUID).Scan(&tryBefore); err != nil {
		t.Fatal(err)
	}

	scr := scheduler.NewScheduler(sched, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Millisecond)
	scr.SetDispatcher(okDispatcher{})
	scr.SetLeading(true)
	// A few ticks: re-place (failed→none), then none→scheduled→queued→dispatch.
	for i := 0; i < 5; i++ {
		if err := scr.Step(ctx); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}

	runs, err := sched.ActiveRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, r := range runs {
		if r.DagID != dagID {
			continue
		}
		found = true
		if r.States["t"] == domain.TaskStateFailed {
			t.Errorf("infra-reaped task should be re-placed and re-running, not left failed; state=%s", r.States["t"])
		}
		if r.InfraAttempts["t"] < 1 {
			t.Errorf("infra_attempts should be >=1 after the re-place, got %d", r.InfraAttempts["t"])
		}
	}
	if !found {
		t.Error("run must stay ACTIVE after an infra re-place (not finalize) — but it is not in ActiveRuns")
	}

	var tryAfter int
	if err := pg.Pool.QueryRow(ctx, "SELECT try_number FROM task_instances WHERE dag_run_id=$1::uuid AND task_id='t'", runUUID).Scan(&tryAfter); err != nil {
		t.Fatal(err)
	}
	if tryAfter != tryBefore {
		t.Errorf("infra recovery must not consume the retry budget: try_number before=%d after=%d", tryBefore, tryAfter)
	}
}
