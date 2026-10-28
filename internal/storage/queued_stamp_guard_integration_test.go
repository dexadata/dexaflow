//go:build integration

package storage_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
	"github.com/dexadata/dexaflow/internal/scheduler"
	"github.com/dexadata/dexaflow/internal/storage"
)

// fastAgentDispatcher stands in for a dispatch whose agent starts at once (a
// warm worker, Lite): before Dispatch returns to the tick, the attempt is
// claimed and its RUNNING pre-flight lands, so the row is already `running`
// when the scheduler stamps `queued`.
type fastAgentDispatcher struct {
	t       *testing.T
	exec    *storage.ExecutionStore
	ran     bool
	running bool
}

func (d *fastAgentDispatcher) Dispatch(ctx context.Context, runID, dagID, _ string, task domain.TaskSpec) (executor.Disposition, error) {
	d.ran = true
	res, err := d.exec.ResolveTask(ctx, runID, task.TaskID)
	if err != nil {
		d.t.Errorf("ResolveTask: %v", err)
		return executor.Dispatched, nil
	}
	id := auth.AgentIdentity{TenantID: res.TenantID, DagID: dagID, RunID: runID, TaskID: task.TaskID,
		TryNumber: res.TryNumber, AttemptEpoch: res.AttemptEpoch, HasAttemptEpoch: true}
	if err := d.exec.ReportState(ctx, id, domain.TaskStateRunning, 0, ""); err != nil {
		d.t.Errorf("RUNNING report: %v", err)
		return executor.Dispatched, nil
	}
	d.running = true
	return executor.Dispatched, nil
}

// TestQueuedStampNeverRegressesRunningRowIntegration (#929): the dispatch's
// queued stamp runs after the task was handed to the executor, and a fast
// agent's RUNNING report can land in between. Driven through the real
// Scheduler.Step, the stamp is guarded to the scheduled slot the tick planned
// (MarkTaskInstanceQueued), so the running row stays running, keeps its
// started_at, gets no queued_at, and never becomes a dispatch-lost candidate.
func TestQueuedStampNeverRegressesRunningRowIntegration(t *testing.T) {
	pg, repo, sched, exec, ctx := openExecPG(t)
	dagID := fmt.Sprintf("queued_stamp_guard_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{{TaskID: "load", Type: domain.TaskTypePython, Entrypoint: "dag:f"}}
	registerSpec(t, repo, ctx, dagID, tasks)
	// Other tests' active runs would be advanced by this Step too.
	if _, err := pg.Pool.Exec(ctx, `UPDATE dag_runs SET state='success' WHERE state IN ('queued','running')`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateQueued, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateDagRun: %v", err)
	}
	d := &fastAgentDispatcher{t: t, exec: exec}
	s := scheduler.NewScheduler(sched, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Millisecond)
	s.SetDispatcher(d)
	s.SetLeading(true)
	// Tick 1 materializes the run, tick 2 schedules the root, tick 3 dispatches
	// it (the agent reports RUNNING inside Dispatch) and stamps queued.
	for i := 0; i < 3 && !d.ran; i++ {
		if err := s.Step(ctx); err != nil {
			t.Fatalf("Step %d: %v", i+1, err)
		}
	}
	if !d.running {
		t.Fatal("the tick never dispatched load to a running agent, so the race was not exercised")
	}
	runUUID := resolveRunUUID(t, sched, ctx, dagID)

	var state string
	var startedSet, queuedSet bool
	if err := pg.Pool.QueryRow(ctx, `SELECT state::text, started_at IS NOT NULL, queued_at IS NOT NULL
		FROM task_instances WHERE dag_run_id=$1::uuid AND task_id='load'`, runUUID).Scan(&state, &startedSet, &queuedSet); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if state != string(domain.TaskStateRunning) {
		t.Fatalf("row state after the queued stamp = %s, want running", state)
	}
	if !startedSet {
		t.Error("the running row lost its started_at")
	}
	if queuedSet {
		t.Error("the superseded queued stamp still wrote queued_at")
	}
	cands, err := sched.ListStaleQueuedCandidates(ctx, 0, executor.ReapCursor{})
	if err != nil {
		t.Fatalf("ListStaleQueuedCandidates: %v", err)
	}
	for _, c := range cands {
		if c.DagRunID == runUUID && c.TaskID == "load" {
			t.Error("a running attempt is a dispatch-lost candidate")
		}
	}

	// The store-level write the tick issues, replayed on the running row with
	// the slot the tick read, is a no-op as well.
	if ok, err := sched.MarkQueued(ctx, runUUID, "load", nil); err != nil || ok {
		t.Fatalf("MarkQueued on a running row = %v, %v; want false", ok, err)
	}
	if st := taskInstanceState(t, sched, ctx, runUUID, "load"); st != domain.TaskStateRunning {
		t.Fatalf("MarkQueued moved the running row to %s", st)
	}
}
