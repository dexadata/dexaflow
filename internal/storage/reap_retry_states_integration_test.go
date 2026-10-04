//go:build integration

package storage_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
)

// TestListReapCandidatesIgnoresParkedTaskInstancesIntegration pins that a run
// whose only pending task instance is parked waiting for the scheduler to bring
// it back (up_for_retry during its retry_delay, up_for_reschedule between
// sensor pokes, or the reserved deferred state) is still progressing and MUST
// NOT be an orphan candidate, however long ago its last observable activity
// was. Neither parked state stamps a fresh timestamp on entry (up_for_retry
// keeps the failed attempt's ended_at, up_for_reschedule keeps the poke's
// started_at), so a retry_delay or poke_interval of 5 minutes or more used to
// push the run past the reaper threshold and fail it as orphaned.
//
// The control is a truly stuck run (every TI settled or never started, the
// finalizer missed it) with the same 10-minute-old activity: it stays a
// candidate, so the fix narrows the reaper without blinding it.
func TestListReapCandidatesIgnoresParkedTaskInstancesIntegration(t *testing.T) {
	repo, sched, ctx := openRepo(t)
	pg, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pg.Close)

	tasks := []domain.TaskSpec{
		{TaskID: "extract", Type: domain.TaskTypePython},
		{TaskID: "load", Type: domain.TaskTypePython, DependsOn: []string{"extract"}},
	}
	parked := map[domain.TaskState]string{
		domain.TaskStateUpForRetry:      "",
		domain.TaskStateUpForReschedule: "",
		domain.TaskState("deferred"):    "",
	}
	for state := range parked {
		parked[state] = newBackdatedRun(t, repo, sched, pg, ctx, fmt.Sprintf("reap_parked_%s", state), tasks, state)
	}
	stuck := newBackdatedRun(t, repo, sched, pg, ctx, "reap_stuck_ctrl", tasks, domain.TaskStateSuccess)

	cands, err := sched.ListReapCandidates(ctx)
	if err != nil {
		t.Fatalf("ListReapCandidates: %v", err)
	}
	for state, runUUID := range parked {
		if c := findCandidate(cands, runUUID); c != nil {
			t.Errorf("run whose only pending TI is %s for 10 minutes must NOT be an orphan candidate; got %+v", state, *c)
		}
	}
	c := findCandidate(cands, stuck)
	if c == nil {
		t.Fatalf("truly stuck run (all TIs settled or never started) must stay a candidate; got %+v", cands)
	}
	if age := time.Since(c.LastActivity); age < 9*time.Minute {
		t.Errorf("stuck run last activity should be ~10m old, got %s", age)
	}
}

// newBackdatedRun registers a fresh DAG, creates one running run for it, moves
// its first task to state, and backdates every timestamp of the run and its task
// instances by 10 minutes. queued_at is pushed far into the past so the run
// sorts ahead of leftovers from other tests under the query's LIMIT. A cleanup
// moves the run to a terminal state so it never lingers as a candidate.
func newBackdatedRun(t *testing.T, repo *storage.Repository, sched *storage.SchedulerStore, pg *pgxpool.Pool,
	ctx context.Context, prefix string, tasks []domain.TaskSpec, state domain.TaskState,
) string {
	t.Helper()
	dagID := fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	registerSpec(t, repo, ctx, dagID, tasks)
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create run %s: %v", dagID, err)
	}
	runUUID := resolveRunUUID(t, sched, ctx, dagID)
	if err := sched.MaterializeTasks(ctx, runUUID, tasks); err != nil {
		t.Fatalf("MaterializeTasks %s: %v", dagID, err)
	}
	if err := sched.ApplyTransition(ctx, runUUID, "extract", state); err != nil {
		t.Fatalf("transition %s to %s: %v", dagID, state, err)
	}
	if _, err := pg.Exec(ctx, `
		UPDATE task_instances
		SET started_at = now() - interval '12 minutes',
		    ended_at = now() - interval '10 minutes'
		WHERE dag_run_id = $1 AND task_id = 'extract'`, runUUID); err != nil {
		t.Fatalf("backdate TI %s: %v", dagID, err)
	}
	if _, err := pg.Exec(ctx, `
		UPDATE dag_runs
		SET queued_at = '2000-01-01T00:00:00Z', started_at = now() - interval '13 minutes'
		WHERE id = $1`, runUUID); err != nil {
		t.Fatalf("backdate run %s: %v", dagID, err)
	}
	t.Cleanup(func() {
		_ = sched.SetRunState(context.Background(), runUUID, domain.DagRunStateFailed) //nolint:errcheck // best-effort test cleanup
	})
	return runUUID
}
