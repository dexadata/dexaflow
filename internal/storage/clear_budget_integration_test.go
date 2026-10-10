//go:build integration

package storage_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
)

// clearMode is one of the three ways ClearTaskInstances resets a task instance.
// Each one is its own SQL statement, so every per-attempt reset has to be proven
// on all three: a column reset in two of them is a bug in the third.
type clearMode struct {
	name       string
	taskIDs    []string
	onlyFailed bool
}

// clearModes lists the three reset statements behind ClearTaskInstances:
// ResetAllFailedTaskInstances, ResetFailedTaskInstance and ResetTaskInstanceToNone.
var clearModes = []clearMode{
	{name: "all failed in the run", taskIDs: nil, onlyFailed: true},
	{name: "named task, only failed", taskIDs: []string{"t"}, onlyFailed: true},
	{name: "named task, any state", taskIDs: []string{"t"}, onlyFailed: false},
}

func intPtr(n int) *int { return &n }

// registerClearSpec registers spec as a new version of its DAG and fails the test
// when the version is not created.
func registerClearSpec(t *testing.T, repo *storage.Repository, ctx context.Context, spec domain.DAGSpec) {
	t.Helper()
	if spec.SchemaVersion == "" {
		spec.SchemaVersion = "1.0"
	}
	hash, err := spec.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	if created, rerr := repo.RegisterDagVersion(ctx, "default", spec, hash); rerr != nil || !created {
		t.Fatalf("register %s %s: created=%v err=%v", spec.DagID, spec.DagVersion, created, rerr)
	}
}

// seedClearRun registers spec, creates run r1 pinned to it, materializes
// materialized (the task list as the scheduler hands it over, defaults already
// applied) and fails task "t". Every other task is marked success. It returns
// the run UUID.
func seedClearRun(t *testing.T, repo *storage.Repository, sched *storage.SchedulerStore, ctx context.Context, spec domain.DAGSpec, materialized []domain.TaskSpec) string {
	t.Helper()
	registerClearSpec(t, repo, ctx, spec)
	if _, err := repo.CreateDagRun(ctx, "default", spec.DagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	runUUID := resolveRunUUID(t, sched, ctx, spec.DagID)
	if err := sched.MaterializeTasks(ctx, runUUID, materialized); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	for _, task := range materialized {
		state := domain.TaskStateSuccess
		if task.TaskID == "t" {
			state = domain.TaskStateFailed
		}
		if err := sched.ApplyTransition(ctx, runUUID, task.TaskID, state); err != nil {
			t.Fatalf("transition %s to %s: %v", task.TaskID, state, err)
		}
	}
	return runUUID
}

// tryBudget reads task "t"'s try_number and max_tries.
func tryBudget(t *testing.T, pg *storage.Postgres, ctx context.Context, runUUID string) (tryNumber, maxTries int) {
	t.Helper()
	if err := pg.Pool.QueryRow(ctx,
		"SELECT try_number, max_tries FROM task_instances WHERE dag_run_id=$1::uuid AND task_id='t'", runUUID).
		Scan(&tryNumber, &maxTries); err != nil {
		t.Fatalf("select try budget: %v", err)
	}
	return tryNumber, maxTries
}

// TestClearRestoresRetryBudget is D1 of #1131. max_tries used to be written once,
// at materialization, while every clear bumps try_number. One clear therefore
// left try_number at or past max_tries, so the scheduler's retriable check
// (Tries < MaxTries) was false for the rest of the task's life: a single clear
// cost every retry, and the UI rendered "try 4 of 3".
//
// Apache Airflow's clear_task_instances restores the budget from the task:
// max_tries = try_number + task.retries. Dexaflow counts max_tries as retries + 1
// and bumps try_number at clear time rather than at scheduling, so the same rule
// reads max_tries = (new try_number) + retries here: the cleared attempt plus a
// full set of retries.
func TestClearRestoresRetryBudget(t *testing.T) {
	for _, mode := range clearModes {
		t.Run(mode.name, func(t *testing.T) {
			repo, sched, pg, ctx := openInfra(t)
			dagID := fmt.Sprintf("clear_budget_%d", time.Now().UnixNano())
			tasks := []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython, Retries: intPtr(2)}}
			runUUID := seedClearRun(t, repo, sched, ctx,
				domain.DAGSpec{DagID: dagID, DagVersion: "v1", Image: "img:v1", Tasks: tasks}, tasks)

			if _, err := repo.ClearTaskInstances(ctx, "default", dagID, "r1", mode.taskIDs, mode.onlyFailed, domain.ClearOptions{ResetDagRun: true}); err != nil {
				t.Fatalf("ClearTaskInstances: %v", err)
			}
			try, maxTries := tryBudget(t, pg, ctx, runUUID)
			if try != 2 {
				t.Fatalf("try_number after clear = %d, want 2", try)
			}
			if maxTries != try+2 {
				t.Errorf("max_tries after clear = %d, want try_number + retries = %d; without it the clear spends every retry", maxTries, try+2)
			}
		})
	}
}

// TestClearRetryBudgetHonorsDefaultArgs: a task with no retries of its own takes
// the DAG's default_args.retries, exactly as materialization does, so a clear
// restores the same budget the run started with.
func TestClearRetryBudgetHonorsDefaultArgs(t *testing.T) {
	repo, sched, pg, ctx := openInfra(t)
	dagID := fmt.Sprintf("clear_budget_defaults_%d", time.Now().UnixNano())
	spec := domain.DAGSpec{
		DagID: dagID, DagVersion: "v1", Image: "img:v1",
		DefaultArgs: &domain.DefaultArgs{Retries: 1},
		Tasks:       []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython}},
	}
	// The scheduler materializes with the defaults filled in.
	runUUID := seedClearRun(t, repo, sched, ctx, spec,
		[]domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython, Retries: intPtr(1)}})

	if _, err := repo.ClearTaskInstances(ctx, "default", dagID, "r1", []string{"t"}, true, domain.ClearOptions{ResetDagRun: true}); err != nil {
		t.Fatalf("ClearTaskInstances: %v", err)
	}
	if try, maxTries := tryBudget(t, pg, ctx, runUUID); maxTries != try+1 {
		t.Errorf("max_tries = %d, want try_number %d + default_args.retries 1", maxTries, try)
	}
}

// TestClearRetryBudgetFollowsTheVersionThatWillRun: the budget is read from the
// version the re-run executes. A pinned clear keeps the run's own version, so a
// later deploy that changed retries does not leak in; run_on_latest_version
// re-binds the run, so the new version's retries apply. Airflow resolves the task
// the same way (the run's DAG version, or the latest one on request).
func TestClearRetryBudgetFollowsTheVersionThatWillRun(t *testing.T) {
	cases := []struct {
		name   string
		latest bool
		want   int // max_tries for try_number 2
	}{
		{name: "pinned keeps the run's version", latest: false, want: 2 + 2},
		{name: "run_on_latest_version takes the current version", latest: true, want: 2 + 5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo, sched, pg, ctx := openInfra(t)
			dagID := fmt.Sprintf("clear_budget_version_%d", time.Now().UnixNano())
			v1 := []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython, Retries: intPtr(2)}}
			runUUID := seedClearRun(t, repo, sched, ctx,
				domain.DAGSpec{DagID: dagID, DagVersion: "v1", Image: "img:v1", Tasks: v1}, v1)
			registerClearSpec(t, repo, ctx, domain.DAGSpec{
				DagID: dagID, DagVersion: "v2", Image: "img:v2",
				Tasks: []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython, Retries: intPtr(5)}},
			})

			opts := domain.ClearOptions{ResetDagRun: true, RunOnLatestVersion: c.latest}
			if _, err := repo.ClearTaskInstances(ctx, "default", dagID, "r1", []string{"t"}, true, opts); err != nil {
				t.Fatalf("ClearTaskInstances: %v", err)
			}
			if _, maxTries := tryBudget(t, pg, ctx, runUUID); maxTries != c.want {
				t.Errorf("max_tries = %d, want %d", maxTries, c.want)
			}
		})
	}
}

// TestClearRetryBudgetForATaskTheVersionNoLongerHas: when the version the re-run
// executes has no such task, there is no retries value to restore from. Airflow
// then keeps max_tries but never below the attempts already made; here that is
// never below the cleared attempt, so the UI cannot show an attempt past its
// budget.
func TestClearRetryBudgetForATaskTheVersionNoLongerHas(t *testing.T) {
	repo, sched, pg, ctx := openInfra(t)
	dagID := fmt.Sprintf("clear_budget_gone_%d", time.Now().UnixNano())
	v1 := []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython, Retries: intPtr(2)}}
	runUUID := seedClearRun(t, repo, sched, ctx,
		domain.DAGSpec{DagID: dagID, DagVersion: "v1", Image: "img:v1", Tasks: v1}, v1)
	// Several earlier clears already pushed try_number past the original budget.
	if _, err := pg.Pool.Exec(ctx,
		"UPDATE task_instances SET try_number = 5 WHERE dag_run_id=$1::uuid AND task_id='t'", runUUID); err != nil {
		t.Fatal(err)
	}
	registerClearSpec(t, repo, ctx, domain.DAGSpec{
		DagID: dagID, DagVersion: "v2", Image: "img:v2",
		Tasks: []domain.TaskSpec{{TaskID: "other", Type: domain.TaskTypePython}},
	})

	opts := domain.ClearOptions{ResetDagRun: true, RunOnLatestVersion: true}
	if _, err := repo.ClearTaskInstances(ctx, "default", dagID, "r1", []string{"t"}, true, opts); err != nil {
		t.Fatalf("ClearTaskInstances: %v", err)
	}
	try, maxTries := tryBudget(t, pg, ctx, runUUID)
	if try != 6 || maxTries != 6 {
		t.Errorf("try_number/max_tries = %d/%d, want 6/6 (the cleared attempt is inside the budget, no retries invented)", try, maxTries)
	}
}

// TestClearResetsInfraAttempts is D2 of #1131. infra_attempts bounds the
// off-budget infra re-placements (ADR 0051) and nothing ever zeroed it, so a
// task that had used them up was re-runnable by clear but with no infra
// tolerance at all: the next lost agent was terminal. A clear starts a fresh
// attempt, and like dispatch_attempts the counter has to start over with it.
func TestClearResetsInfraAttempts(t *testing.T) {
	for _, mode := range clearModes {
		t.Run(mode.name, func(t *testing.T) {
			repo, sched, pg, ctx := openInfra(t)
			dagID := fmt.Sprintf("clear_infra_%d", time.Now().UnixNano())
			tasks := []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython}}
			runUUID := seedClearRun(t, repo, sched, ctx,
				domain.DAGSpec{DagID: dagID, DagVersion: "v1", Image: "img:v1", Tasks: tasks}, tasks)
			if _, err := pg.Pool.Exec(ctx,
				"UPDATE task_instances SET infra_attempts = 6 WHERE dag_run_id=$1::uuid AND task_id='t'", runUUID); err != nil {
				t.Fatal(err)
			}

			if _, err := repo.ClearTaskInstances(ctx, "default", dagID, "r1", mode.taskIDs, mode.onlyFailed, domain.ClearOptions{ResetDagRun: true}); err != nil {
				t.Fatalf("ClearTaskInstances: %v", err)
			}
			var infra int
			if err := pg.Pool.QueryRow(ctx,
				"SELECT infra_attempts FROM task_instances WHERE dag_run_id=$1::uuid AND task_id='t'", runUUID).Scan(&infra); err != nil {
				t.Fatal(err)
			}
			if infra != 0 {
				t.Errorf("infra_attempts after clear = %d, want 0; the cleared task would have no infra tolerance", infra)
			}
		})
	}
}

// TestPinnedClearResetsAlertBookkeeping is #1140. ReopenDagRunKeepingVersion (the
// default, pinned clear) resets the on-failure alert bookkeeping along with the
// run state, so a genuine re-failure pages again and a spent alert budget is not
// carried into an episode that has not been attempted. Every other alert test
// drives the run_on_latest_version branch, so stripping the reset from this
// query left the suite green.
func TestPinnedClearResetsAlertBookkeeping(t *testing.T) {
	repo, sched, pg, ctx := openInfra(t)
	dagID := fmt.Sprintf("clear_alert_pinned_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython}}
	runUUID := seedClearRun(t, repo, sched, ctx,
		domain.DAGSpec{DagID: dagID, DagVersion: "v1", Image: "img:v1", Tasks: tasks}, tasks)
	if _, err := pg.Pool.Exec(ctx, `
		UPDATE dag_runs SET alerted_at = now(), alert_attempts = 3,
		       next_alert_attempt_at = now() + interval '1 hour'
		WHERE id = $1::uuid`, runUUID); err != nil {
		t.Fatal(err)
	}

	opts := domain.ClearOptions{ResetDagRun: true, RunOnLatestVersion: false}
	if _, err := repo.ClearTaskInstances(ctx, "default", dagID, "r1", []string{"t"}, true, opts); err != nil {
		t.Fatalf("ClearTaskInstances: %v", err)
	}
	var alerted, nextAttempt bool
	var attempts int
	if err := pg.Pool.QueryRow(ctx, `
		SELECT alerted_at IS NOT NULL, alert_attempts, next_alert_attempt_at IS NOT NULL
		FROM dag_runs WHERE id = $1::uuid`, runUUID).Scan(&alerted, &attempts, &nextAttempt); err != nil {
		t.Fatal(err)
	}
	if alerted || attempts != 0 || nextAttempt {
		t.Errorf("after a pinned clear alerted_at set=%v alert_attempts=%d next_alert_attempt_at set=%v, want unset/0/unset",
			alerted, attempts, nextAttempt)
	}
}

// TestClearedAttemptSpecCarriesTheRestoredBudget pins the agent side of #1131.
// The runtime fires on_failure_callback only when try_number >= max_tries
// (#424), and the agent stamps max_tries from the TaskSpec. Read from the DAG
// spec (retries + 1), the budget stays at 3 while a clear moves try_number to 2
// and max_tries to 4: every failed retry of the cleared task would fire the
// callback as if it were final. The spec has to carry the row's max_tries.
func TestClearedAttemptSpecCarriesTheRestoredBudget(t *testing.T) {
	repo, sched, pg, ctx := openInfra(t)
	exec := storage.NewExecutionStore(pg)
	dagID := fmt.Sprintf("clear_spec_budget_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython, Retries: intPtr(2)}}
	runUUID := seedClearRun(t, repo, sched, ctx,
		domain.DAGSpec{DagID: dagID, DagVersion: "v1", Image: "img:v1", Tasks: tasks}, tasks)

	if _, err := repo.ClearTaskInstances(ctx, "default", dagID, "r1", []string{"t"}, true, domain.ClearOptions{ResetDagRun: true}); err != nil {
		t.Fatalf("ClearTaskInstances: %v", err)
	}
	try, maxTries := tryBudget(t, pg, ctx, runUUID)
	spec, err := exec.TaskSpec(ctx, auth.AgentIdentity{RunID: runUUID, TaskID: "t", TryNumber: try})
	if err != nil {
		t.Fatalf("TaskSpec: %v", err)
	}
	if spec.MaxTries != maxTries {
		t.Errorf("TaskSpec.MaxTries after clear = %d, want the row's max_tries %d (try %d); the runtime would treat a retriable failure as final", spec.MaxTries, maxTries, try)
	}
}

// TestClearPoolSlotsFollowTheVersionThatWillRun (#1499): a cleared task
// instance takes the pool_slots of the version its re-run executes, the size
// the admission gate charges it, so the pools API keeps agreeing with the gate
// after a run_on_latest_version clear re-binds the run to a version that
// resized the task. A pinned clear keeps the run's own size, and a task the
// executing version no longer declares keeps the size it had. Proven on all
// three reset statements.
func TestClearPoolSlotsFollowTheVersionThatWillRun(t *testing.T) {
	cases := []struct {
		name   string
		latest bool
		v2     []domain.TaskSpec
		want   int
	}{
		{name: "pinned keeps the run's version", latest: false,
			v2: []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython, PoolSlots: 4}}, want: 2},
		{name: "run_on_latest_version takes the current version", latest: true,
			v2: []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython, PoolSlots: 4}}, want: 4},
		{name: "a task the current version dropped keeps its size", latest: true,
			v2: []domain.TaskSpec{{TaskID: "other", Type: domain.TaskTypePython, PoolSlots: 4}}, want: 2},
	}
	for _, c := range cases {
		for _, mode := range clearModes {
			t.Run(c.name+"/"+mode.name, func(t *testing.T) {
				repo, sched, pg, ctx := openInfra(t)
				dagID := fmt.Sprintf("clear_pool_slots_%d", time.Now().UnixNano())
				v1 := []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython, PoolSlots: 2}}
				runUUID := seedClearRun(t, repo, sched, ctx,
					domain.DAGSpec{DagID: dagID, DagVersion: "v1", Image: "img:v1", Tasks: v1}, v1)
				if got := taskPoolSlots(t, pg, ctx, runUUID); got != 2 {
					t.Fatalf("precondition: materialized pool_slots = %d, want 2", got)
				}
				registerClearSpec(t, repo, ctx, domain.DAGSpec{DagID: dagID, DagVersion: "v2", Image: "img:v2", Tasks: c.v2})

				opts := domain.ClearOptions{ResetDagRun: true, RunOnLatestVersion: c.latest}
				if _, err := repo.ClearTaskInstances(ctx, "default", dagID, "r1", mode.taskIDs, mode.onlyFailed, opts); err != nil {
					t.Fatalf("ClearTaskInstances: %v", err)
				}
				if got := taskPoolSlots(t, pg, ctx, runUUID); got != c.want {
					t.Errorf("pool_slots = %d, want %d", got, c.want)
				}
			})
		}
	}
}

// taskPoolSlots reads task "t"'s pool_slots.
func taskPoolSlots(t *testing.T, pg *storage.Postgres, ctx context.Context, runUUID string) int {
	t.Helper()
	var n int
	if err := pg.Pool.QueryRow(ctx,
		"SELECT pool_slots FROM task_instances WHERE dag_run_id=$1::uuid AND task_id='t'", runUUID).Scan(&n); err != nil {
		t.Fatalf("select pool_slots: %v", err)
	}
	return n
}
