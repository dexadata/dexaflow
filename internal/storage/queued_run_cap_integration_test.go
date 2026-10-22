//go:build integration

package storage_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/scheduler"
	"github.com/dexadata/dexaflow/internal/storage"
)

// finishedRun creates a run of dagID at the given logical date whose one task
// succeeded and which itself succeeded, ready for a clear to re-queue it. It
// returns the run's UUID.
func finishedRun(t *testing.T, ctx context.Context, repo *storage.Repository, store *storage.SchedulerStore, dagID, runID string, logical time.Time) string {
	t.Helper()
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: runID, State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: logical,
	}); err != nil {
		t.Fatalf("create run %s: %v", runID, err)
	}
	runUUID := activeRuns(t, ctx, store, dagID)[runID].RunID
	if err := store.MaterializeTasks(ctx, runUUID, []domain.TaskSpec{{TaskID: "a", Type: domain.TaskTypePython}}); err != nil {
		t.Fatalf("materialize %s: %v", runID, err)
	}
	if err := store.ApplyTransition(ctx, runUUID, "a", domain.TaskStateSuccess); err != nil {
		t.Fatalf("finish task of %s: %v", runID, err)
	}
	if _, err := store.SetRunState(ctx, runUUID, domain.DagRunStateRunning, domain.DagRunStateSuccess); err != nil {
		t.Fatalf("finish %s: %v", runID, err)
	}
	return runUUID
}

func dagRunStateOf(t *testing.T, ctx context.Context, repo *storage.Repository, dagID, runID string) domain.DagRunState {
	t.Helper()
	run, err := repo.GetDagRun(ctx, "default", dagID, runID)
	if err != nil {
		t.Fatalf("get run %s: %v", runID, err)
	}
	return run.State
}

// TestUnpauseStartsClearedRunsUnderMaxActiveRunsIntegration drives the real
// tick over the case of #1766: three runs of a paused DAG with
// max_active_runs=1 are cleared, so all three wait in queued. Once the DAG is
// unpaused, only the run with the oldest logical date moves back to running
// and dispatches its task; the other two stay queued with nothing dispatched,
// and the next one starts only after the first finishes, as Airflow does.
func TestUnpauseStartsClearedRunsUnderMaxActiveRunsIntegration(t *testing.T) {
	repo, store, ctx := openRepo(t)
	sched := scheduler.NewScheduler(store, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Millisecond)
	sched.SetDispatcher(noopDispatcher{})
	sched.SetLeading(true)
	dagID := fmt.Sprintf("queued_cap_%d", time.Now().UnixNano())
	spec := domain.DAGSpec{
		SchemaVersion: "1.0", DagID: dagID, DagVersion: "v1", Image: "img:v1", MaxActiveRuns: 1,
		Tasks: []domain.TaskSpec{{TaskID: "a", Type: domain.TaskTypePython, Entrypoint: "dag:a"}},
	}
	hash, err := spec.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RegisterDagVersion(ctx, "default", spec, hash); err != nil {
		t.Fatalf("register: %v", err)
	}
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// Created newest first, so the store's queued_at order is not the start order.
	finishedRun(t, ctx, repo, store, dagID, "r3", day.AddDate(0, 0, 2))
	finishedRun(t, ctx, repo, store, dagID, "r2", day.AddDate(0, 0, 1))
	oldest := finishedRun(t, ctx, repo, store, dagID, "r1", day)
	setPaused(t, ctx, repo, dagID, true)
	for _, id := range []string{"r3", "r2", "r1"} {
		if _, err := repo.ClearTaskInstances(ctx, "default", dagID, id, []string{"a"}, false, domain.ClearOptions{ResetDagRun: true}); err != nil {
			t.Fatalf("clear %s: %v", id, err)
		}
	}
	if got := activeRuns(t, ctx, store, dagID)["r1"].MaxActiveRuns; got != 1 {
		t.Fatalf("the tick reads the DAG's max_active_runs: got %d, want 1", got)
	}

	step := func(n int) {
		t.Helper()
		for i := 0; i < n; i++ {
			if err := sched.Step(ctx); err != nil {
				t.Fatalf("step: %v", err)
			}
		}
	}
	setPaused(t, ctx, repo, dagID, false)
	step(3)
	got := map[string]domain.DagRunState{}
	for _, id := range []string{"r1", "r2", "r3"} {
		got[id] = dagRunStateOf(t, ctx, repo, dagID, id)
	}
	if got["r1"] != domain.DagRunStateRunning || got["r2"] != domain.DagRunStateQueued || got["r3"] != domain.DagRunStateQueued {
		t.Fatalf("after unpause with max_active_runs=1: run states %v, want r1 running, r2 and r3 queued", got)
	}
	if st := taskStates(t, ctx, repo, dagID, "r1")["a"]; st != domain.TaskStateQueued {
		t.Fatalf("the started run dispatches its task: a = %q, want queued", st)
	}
	for _, id := range []string{"r2", "r3"} {
		if st := taskStates(t, ctx, repo, dagID, id)["a"]; st != domain.TaskStateNone {
			t.Fatalf("a run left queued dispatches nothing: %s task a = %q, want none", id, st)
		}
	}

	if err := store.ApplyTransition(ctx, oldest, "a", domain.TaskStateSuccess); err != nil {
		t.Fatalf("finish r1's task: %v", err)
	}
	step(2)
	if st := dagRunStateOf(t, ctx, repo, dagID, "r1"); st != domain.DagRunStateSuccess {
		t.Fatalf("r1 finishes: state %q, want success", st)
	}
	if st2, st3 := dagRunStateOf(t, ctx, repo, dagID, "r2"), dagRunStateOf(t, ctx, repo, dagID, "r3"); st2 != domain.DagRunStateRunning || st3 != domain.DagRunStateQueued {
		t.Fatalf("after r1 finishes the next oldest starts: r2 %q, r3 %q, want running and queued", st2, st3)
	}
}

// markingStore marks one run success right after the tick's snapshot, as a
// user's PATCH landing between the read and the start would.
type markingStore struct {
	*storage.SchedulerStore
	mark func()
}

func (m markingStore) ActiveRuns(ctx context.Context) ([]scheduler.RunState, error) {
	runs, err := m.SchedulerStore.ActiveRuns(ctx)
	m.mark()
	return runs, err
}

// TestQueuedRunMarkedBeforeItsStartStaysMarkedIntegration: the tick's
// queued -> running write is guarded on the run still being queued (#1766). A
// run marked success after the tick read it as queued stays success, and none
// of its tasks is dispatched.
func TestQueuedRunMarkedBeforeItsStartStaysMarkedIntegration(t *testing.T) {
	repo, store, ctx := openRepo(t)
	dagID := fmt.Sprintf("queued_marked_%d", time.Now().UnixNano())
	registerSpec(t, repo, ctx, dagID, []domain.TaskSpec{{TaskID: "a", Type: domain.TaskTypePython, Entrypoint: "dag:a"}})
	createRun(t, ctx, repo, dagID, "r1", domain.DagRunStateQueued)
	marked := false
	wrapped := markingStore{SchedulerStore: store, mark: func() {
		if marked {
			return
		}
		marked = true
		if err := repo.SetDagRunState(ctx, "default", dagID, "r1", string(domain.DagRunStateSuccess)); err != nil {
			t.Errorf("mark r1 success: %v", err)
		}
	}}
	sched := scheduler.NewScheduler(wrapped, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Millisecond)
	sched.SetDispatcher(noopDispatcher{})
	sched.SetLeading(true)
	for i := 0; i < 3; i++ {
		if err := sched.Step(ctx); err != nil {
			t.Fatalf("step: %v", err)
		}
	}
	if st := dagRunStateOf(t, ctx, repo, dagID, "r1"); st != domain.DagRunStateSuccess {
		t.Fatalf("a run marked success before its start: state %q, want success", st)
	}
	for task, st := range taskStates(t, ctx, repo, dagID, "r1") {
		if st != domain.TaskStateNone {
			t.Errorf("nothing of a marked run is dispatched: task %s = %q", task, st)
		}
	}
}

// TestPatchQueuedResetsStartedAtIntegration: setting a run back to queued
// through the API clears its started_at, as a clear does, so the start the
// scheduler stamps next is the run's new one.
func TestPatchQueuedResetsStartedAtIntegration(t *testing.T) {
	repo, store, ctx := openRepo(t)
	dagID := fmt.Sprintf("patch_queued_%d", time.Now().UnixNano())
	registerSpec(t, repo, ctx, dagID, []domain.TaskSpec{{TaskID: "a", Type: domain.TaskTypePython}})
	createRun(t, ctx, repo, dagID, "r1", domain.DagRunStateQueued)
	if _, err := store.SetRunState(ctx, activeRuns(t, ctx, store, dagID)["r1"].RunID, domain.DagRunStateQueued, domain.DagRunStateRunning); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetDagRunState(ctx, "default", dagID, "r1", string(domain.DagRunStateFailed)); err != nil {
		t.Fatal(err)
	}
	before, err := repo.GetDagRun(ctx, "default", dagID, "r1")
	if err != nil {
		t.Fatal(err)
	}
	if before.StartedAt == nil {
		t.Fatal("fixture: a run created running has a start date")
	}
	if err := repo.SetDagRunState(ctx, "default", dagID, "r1", string(domain.DagRunStateQueued)); err != nil {
		t.Fatal(err)
	}
	after, err := repo.GetDagRun(ctx, "default", dagID, "r1")
	if err != nil {
		t.Fatal(err)
	}
	if after.State != domain.DagRunStateQueued || after.StartedAt != nil || after.EndedAt != nil {
		t.Errorf("after PATCH queued: state %q start %v end %v, want queued with no start or end", after.State, after.StartedAt, after.EndedAt)
	}
}

// TestRunActionAuditKeepsRequestedStateIntegration: a run-level audit entry
// carries its extra metadata, such as the state a PATCH asked for when it
// stored another (#1766), next to the run id.
func TestRunActionAuditKeepsRequestedStateIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	dagID := fmt.Sprintf("audit_requested_%d", time.Now().UnixNano())
	registerSpec(t, repo, ctx, dagID, []domain.TaskSpec{{TaskID: "a", Type: domain.TaskTypePython}})
	if err := repo.RecordRunActionAudit(ctx, "default", "", "dagrun.mark.queued", dagID, "r1",
		map[string]string{"requested_state": "running"}); err != nil {
		t.Fatal(err)
	}
	entries, _, err := repo.ListAuditLogs(ctx, "default", dagID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		var extra map[string]any
		if e.Action != "dagrun.mark.queued" || json.Unmarshal([]byte(e.Extra), &extra) != nil {
			continue
		}
		if extra["requested_state"] == "running" && extra["run_id"] == "r1" {
			return
		}
	}
	t.Errorf("no dagrun.mark.queued entry with requested_state running and run_id r1: %+v", entries)
}

// TestStartQueuedRunStampsTheStartSoTheReaperSparesItIntegration: a run can
// wait in queued under the max_active_runs start cap for longer than the orphan
// reaper's quiet window. Its task instances are all none until the tick after
// the start plans them, so the started_at that StartQueuedRun stamps is the
// only fresh activity the reaper sees, and it must keep the reaper from
// failing a run that has just started. Once that start is old too, the same
// run is reaped, so the window the test uses does reap a quiet run.
func TestStartQueuedRunStampsTheStartSoTheReaperSparesItIntegration(t *testing.T) {
	repo, store, pg, ctx := openInfra(t)
	dagID := fmt.Sprintf("queued_start_stamp_%d", time.Now().UnixNano())
	registerSpec(t, repo, ctx, dagID, []domain.TaskSpec{{TaskID: "a", Type: domain.TaskTypePython, Entrypoint: "dag:a"}})
	createRun(t, ctx, repo, dagID, "r1", domain.DagRunStateQueued)
	run := activeRuns(t, ctx, store, dagID)["r1"]
	if _, err := pg.Pool.Exec(ctx, `UPDATE dag_runs SET queued_at = now() - interval '1 hour', started_at = NULL
		WHERE id = $1::uuid`, run.RunID); err != nil {
		t.Fatal(err)
	}
	if err := store.MaterializeTasks(ctx, run.RunID, run.Tasks); err != nil {
		t.Fatal(err)
	}
	started, err := store.StartQueuedRun(ctx, run.RunID, run.TenantID)
	if err != nil || !started {
		t.Fatalf("start: started=%v err=%v", started, err)
	}
	quietBefore := time.Now().Add(-time.Minute)
	reaped, err := store.ReapRun(ctx, run.RunID, quietBefore)
	if err != nil {
		t.Fatal(err)
	}
	if reaped {
		t.Fatal("a run started a moment ago was reaped as an orphan: its start was not stamped")
	}
	if st := dagRunStateOf(t, ctx, repo, dagID, "r1"); st != domain.DagRunStateRunning {
		t.Fatalf("the started run: state %q, want running", st)
	}
	if _, err := pg.Pool.Exec(ctx, `UPDATE dag_runs SET started_at = now() - interval '1 hour' WHERE id = $1::uuid`, run.RunID); err != nil {
		t.Fatal(err)
	}
	if reaped, err := store.ReapRun(ctx, run.RunID, quietBefore); err != nil || !reaped {
		t.Errorf("control: a run quiet for an hour is reaped: reaped=%v err=%v", reaped, err)
	}
}
