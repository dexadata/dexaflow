//go:build integration

// Package storage_test: a user's run verdict settles the run's task instances.
//
// PATCH dagRuns state=failed or state=success used to update dag_runs.state
// only (#2425). The run left the scheduler snapshot, so its running attempt
// stopped counting toward max_active_runs, max_active_tasks and pools, while it
// kept running and its unstarted tasks stayed none forever. The verdict now
// moves every unfinished task instance as Airflow 3 does, in the same
// transaction as the run, and the attempt's next heartbeat matches no live row,
// so the agent is told to stop.
package storage_test

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/agentrpc"
	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/scheduler"
)

// verdictSpec is a DAG capped at one run and one task, with four independent
// tasks so one run can hold a task in each unfinished state at once.
func verdictSpec(dagID string) domain.DAGSpec {
	task := func(id string) domain.TaskSpec {
		return domain.TaskSpec{TaskID: id, Type: domain.TaskTypePython, Entrypoint: "dag:" + id}
	}
	return domain.DAGSpec{
		SchemaVersion: "1.0", DagID: dagID, DagVersion: "v1", Image: "img:v1",
		MaxActiveRuns: 1, MaxActiveTasks: 1,
		Tasks: []domain.TaskSpec{task("run"), task("wait"), task("todo"), task("done")},
	}
}

// seedVerdictRun registers the DAG and creates run r1 running with task "run"
// running, "wait" queued, "todo" not started and "done" succeeded. It returns
// the run's UUID.
func seedVerdictRun(t *testing.T, dagID string) (string, func(string) map[string]domain.TaskState) {
	t.Helper()
	repo, store, ctx := openRepo(t)
	spec := verdictSpec(dagID)
	hash, _ := spec.CanonicalHash()
	if _, err := repo.RegisterDagVersion(ctx, "default", spec, hash); err != nil {
		t.Fatal(err)
	}
	createRun(t, ctx, repo, dagID, "r1", domain.DagRunStateRunning)
	r1 := activeRuns(t, ctx, store, dagID)["r1"].RunID
	if err := store.MaterializeTasks(ctx, r1, spec.Tasks); err != nil {
		t.Fatal(err)
	}
	for task, path := range map[string][]domain.TaskState{
		"run":  {domain.TaskStateQueued, domain.TaskStateRunning},
		"wait": {domain.TaskStateQueued},
		"done": {domain.TaskStateQueued, domain.TaskStateRunning, domain.TaskStateSuccess},
	} {
		for _, st := range path {
			if err := store.ApplyTransition(ctx, r1, task, st); err != nil {
				t.Fatalf("seeding %s to %s: %v", task, st, err)
			}
		}
	}
	states := func(runID string) map[string]domain.TaskState {
		t.Helper()
		tis, _, err := repo.ListTaskInstances(ctx, "default", dagID, runID, 100, 0)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]domain.TaskState{}
		for _, ti := range tis {
			out[ti.TaskID] = ti.State
		}
		return out
	}
	return r1, states
}

// TestMarkRunFailedSettlesItsTaskInstances: as in Airflow 3, the running
// attempt fails and what had not started is skipped; a finished task keeps its
// state.
func TestMarkRunFailedSettlesItsTaskInstances(t *testing.T) {
	repo, _, ctx := openRepo(t)
	dagID := fmt.Sprintf("verdict_failed_%d", time.Now().UnixNano())
	_, states := seedVerdictRun(t, dagID)

	if err := repo.SetDagRunState(ctx, "default", dagID, "r1", string(domain.DagRunStateFailed)); err != nil {
		t.Fatal(err)
	}
	want := map[string]domain.TaskState{
		"run": domain.TaskStateFailed, "wait": domain.TaskStateSkipped,
		"todo": domain.TaskStateSkipped, "done": domain.TaskStateSuccess,
	}
	if got := states("r1"); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("after mark failed: task states = %v, want %v", got, want)
	}
}

// TestMarkRunSuccessSettlesItsTaskInstances: every unfinished task instance
// becomes success.
func TestMarkRunSuccessSettlesItsTaskInstances(t *testing.T) {
	repo, _, ctx := openRepo(t)
	dagID := fmt.Sprintf("verdict_success_%d", time.Now().UnixNano())
	_, states := seedVerdictRun(t, dagID)

	if err := repo.SetDagRunState(ctx, "default", dagID, "r1", string(domain.DagRunStateSuccess)); err != nil {
		t.Fatal(err)
	}
	for task, st := range states("r1") {
		if st != domain.TaskStateSuccess {
			t.Errorf("after mark success: %s = %s, want success", task, st)
		}
	}
}

// TestMarkRunFailedStopsTheRunningAttempt: the running attempt's next
// heartbeat matches no live row, the signal the agent RPC turns into
// should_terminate.
func TestMarkRunFailedStopsTheRunningAttempt(t *testing.T) {
	repo, _, exec, ctx := openExec(t)
	dagID := fmt.Sprintf("verdict_stop_%d", time.Now().UnixNano())
	r1, _ := seedVerdictRun(t, dagID)

	id := auth.AgentIdentity{TenantID: defaultTenantID(), DagID: dagID, RunID: r1, TaskID: "run", TryNumber: 1}
	if err := exec.RecordHeartbeat(ctx, id); err != nil {
		t.Fatalf("heartbeat before the verdict = %v, want nil", err)
	}
	if err := repo.SetDagRunState(ctx, "default", dagID, "r1", string(domain.DagRunStateFailed)); err != nil {
		t.Fatal(err)
	}
	if err := exec.RecordHeartbeat(ctx, id); !errors.Is(err, agentrpc.ErrStaleReport) {
		t.Fatalf("heartbeat after mark failed = %v, want ErrStaleReport (should_terminate)", err)
	}
}

// TestMarkRunQueuedLeavesTaskInstancesAlone: re-queueing a run is not a
// verdict; it keeps today's behavior.
func TestMarkRunQueuedLeavesTaskInstancesAlone(t *testing.T) {
	repo, _, ctx := openRepo(t)
	dagID := fmt.Sprintf("verdict_queued_%d", time.Now().UnixNano())
	_, states := seedVerdictRun(t, dagID)
	before := states("r1")

	if err := repo.SetDagRunState(ctx, "default", dagID, "r1", string(domain.DagRunStateQueued)); err != nil {
		t.Fatal(err)
	}
	if got := states("r1"); fmt.Sprint(got) != fmt.Sprint(before) {
		t.Fatalf("after mark queued: task states = %v, want unchanged %v", got, before)
	}
}

// TestMarkRunFailedDoesNotLeaveTwoAttemptsUnderTheCaps is the scenario of
// #2425 through the real store and tick: on a DAG capped at one run and one
// task, the next run starts only once the marked run's attempt was told to
// stop, never beside a run whose task still counts as running.
func TestMarkRunFailedDoesNotLeaveTwoAttemptsUnderTheCaps(t *testing.T) {
	repo, store, ctx := openRepo(t)
	sched := scheduler.NewScheduler(store, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Millisecond)
	sched.SetDispatcher(noopDispatcher{})
	sched.SetLeading(true)
	dagID := fmt.Sprintf("verdict_caps_%d", time.Now().UnixNano())
	_, states := seedVerdictRun(t, dagID)

	if err := repo.SetDagRunState(ctx, "default", dagID, "r1", string(domain.DagRunStateFailed)); err != nil {
		t.Fatal(err)
	}
	createRun(t, ctx, repo, dagID, "r2", domain.DagRunStateQueued)
	for i := 0; i < 3; i++ {
		if err := sched.Step(ctx); err != nil {
			t.Fatal(err)
		}
	}
	r1 := states("r1")
	for task, st := range r1 {
		if st == domain.TaskStateRunning || st == domain.TaskStateQueued {
			t.Fatalf("r1.%s = %s after the run was marked failed and r2 ticked: two attempts under max_active_tasks=1", task, st)
		}
	}
}
