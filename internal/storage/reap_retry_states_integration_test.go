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
	"github.com/dexadata/dexaflow/internal/executor"
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
		_ = sched.SetRunState(ctx, runUUID, domain.DagRunStateFailed) //nolint:errcheck // best-effort test cleanup
	})
	return runUUID
}

// orphanThreshold mirrors the executor's default orphan-run threshold.
const orphanThreshold = 5 * time.Minute

// TestReleaseToNoneCountsAsActivityIntegration pins the release window. The
// retry release, the reschedule re-dispatch and the infra re-place each send a
// parked task instance back to `none` and clear its per-attempt timestamps; the
// planner only moves it on to `scheduled` on the NEXT tick. Without a release
// stamp, a run whose other activity is older than the threshold looks orphaned
// for that one tick, and a reaper pass landing in it fails a healthy run. The
// release itself must count as activity: the run is either not a candidate, or
// its last activity is fresh, and an atomic reap is a no-op.
func TestReleaseToNoneCountsAsActivityIntegration(t *testing.T) {
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

	cases := []struct {
		name    string
		parked  domain.TaskState
		prepare string
		release func(runUUID string) error
	}{
		{
			name:   "retry release",
			parked: domain.TaskStateUpForRetry,
			release: func(runUUID string) error {
				ok, rerr := sched.ResetForRetry(ctx, runUUID, "extract")
				if rerr == nil && !ok {
					rerr = fmt.Errorf("retry release did not fire")
				}
				return rerr
			},
		},
		{
			name:   "reschedule re-dispatch",
			parked: domain.TaskStateUpForReschedule,
			release: func(runUUID string) error {
				return sched.RedispatchReschedule(ctx, runUUID, "extract")
			},
		},
		{
			name:    "infra re-place",
			parked:  domain.TaskStateFailed,
			prepare: `UPDATE task_instances SET last_failure_kind = 'infra' WHERE dag_run_id = $1 AND task_id = 'extract'`,
			release: func(runUUID string) error {
				ok, rerr := sched.ResetForInfraReplace(ctx, runUUID, "extract")
				if rerr == nil && !ok {
					rerr = fmt.Errorf("infra re-place did not fire")
				}
				return rerr
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runUUID := newBackdatedRun(t, repo, sched, pg, ctx, "reap_release", tasks, tc.parked)
			if tc.prepare != "" {
				if _, perr := pg.Exec(ctx, tc.prepare, runUUID); perr != nil {
					t.Fatalf("prepare: %v", perr)
				}
			}
			if rerr := tc.release(runUUID); rerr != nil {
				t.Fatalf("release: %v", rerr)
			}

			cands, lerr := sched.ListReapCandidates(ctx)
			if lerr != nil {
				t.Fatalf("ListReapCandidates: %v", lerr)
			}
			now := time.Now().UTC()
			if c := findCandidate(cands, runUUID); c != nil && executor.IsOrphaned(*c, orphanThreshold, now) {
				t.Errorf("run right after its %s must not look orphaned; last_activity=%s (%s ago)",
					tc.name, c.LastActivity, now.Sub(c.LastActivity))
			}
			reaped, rerr := sched.ReapRun(ctx, runUUID, now.Add(-orphanThreshold))
			if rerr != nil {
				t.Fatalf("ReapRun: %v", rerr)
			}
			if reaped {
				t.Errorf("ReapRun must be a no-op right after the %s", tc.name)
			}
		})
	}
}

// TestReapRunRechecksPredicateIntegration pins that ReapRun re-checks the orphan
// predicate atomically instead of trusting the list snapshot. Each case lists a
// genuinely stuck run as a candidate, then changes it before the reap: a
// retriable failure moving to up_for_retry (the scheduler's next tick), a task
// being scheduled, or fresh activity landing. ReapRun must report a no-op and
// leave the run and its task instances untouched. The control reaps a run that
// is still stuck, so the re-check does not blind the reaper.
func TestReapRunRechecksPredicateIntegration(t *testing.T) {
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

	cases := []struct {
		name      string
		change    func(runUUID string) error
		wantState domain.TaskState
		wantTask  string
	}{
		{
			name: "failed task moves to up_for_retry",
			change: func(runUUID string) error {
				return sched.ApplyTransition(ctx, runUUID, "extract", domain.TaskStateUpForRetry)
			},
			wantTask: "extract", wantState: domain.TaskStateUpForRetry,
		},
		{
			name: "downstream gets scheduled",
			change: func(runUUID string) error {
				return sched.ApplyTransition(ctx, runUUID, "load", domain.TaskStateScheduled)
			},
			wantTask: "load", wantState: domain.TaskStateScheduled,
		},
		{
			name: "fresh activity lands",
			change: func(runUUID string) error {
				_, xerr := pg.Exec(ctx, `UPDATE task_instances SET ended_at = now() WHERE dag_run_id = $1 AND task_id = 'extract'`, runUUID)
				return xerr
			},
			wantTask: "extract", wantState: domain.TaskStateFailed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runUUID := newBackdatedRun(t, repo, sched, pg, ctx, "reap_toctou", tasks, domain.TaskStateFailed)
			cands, lerr := sched.ListReapCandidates(ctx)
			if lerr != nil {
				t.Fatalf("ListReapCandidates: %v", lerr)
			}
			c := findCandidate(cands, runUUID)
			if c == nil || !executor.IsOrphaned(*c, orphanThreshold, time.Now().UTC()) {
				t.Fatalf("setup: the stuck run must be an orphaned candidate at list time, got %+v", c)
			}
			if cerr := tc.change(runUUID); cerr != nil {
				t.Fatalf("change: %v", cerr)
			}
			reaped, rerr := sched.ReapRun(ctx, runUUID, time.Now().UTC().Add(-orphanThreshold))
			if rerr != nil {
				t.Fatalf("ReapRun: %v", rerr)
			}
			if reaped {
				t.Errorf("ReapRun must be a no-op once the run is no longer orphaned (%s)", tc.name)
			}
			assertRunAndTask(t, pg, ctx, runUUID, tc.wantTask, tc.wantState)
		})
	}

	t.Run("still stuck is reaped", func(t *testing.T) {
		runUUID := newBackdatedRun(t, repo, sched, pg, ctx, "reap_toctou_ctrl", tasks, domain.TaskStateFailed)
		reaped, rerr := sched.ReapRun(ctx, runUUID, time.Now().UTC().Add(-orphanThreshold))
		if rerr != nil || !reaped {
			t.Fatalf("a run still stuck at reap time must be reaped, got reaped=%v err=%v", reaped, rerr)
		}
	})
}

// assertRunAndTask checks that the run is still running and the task kept the
// state the pre-reap change gave it.
func assertRunAndTask(t *testing.T, pg *pgxpool.Pool, ctx context.Context, runUUID, taskID string, want domain.TaskState) {
	t.Helper()
	var runState, tiState string
	if err := pg.QueryRow(ctx, `SELECT state::text FROM dag_runs WHERE id = $1`, runUUID).Scan(&runState); err != nil {
		t.Fatalf("read run: %v", err)
	}
	if err := pg.QueryRow(ctx, `SELECT state::text FROM task_instances WHERE dag_run_id = $1 AND task_id = $2`, runUUID, taskID).Scan(&tiState); err != nil {
		t.Fatalf("read TI: %v", err)
	}
	if runState != string(domain.DagRunStateRunning) {
		t.Errorf("run state = %s, want running", runState)
	}
	if tiState != string(want) {
		t.Errorf("%s state = %s, want %s", taskID, tiState, want)
	}
}
