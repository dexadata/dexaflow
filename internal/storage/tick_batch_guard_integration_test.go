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

// TestTickBatchDoesNotRegressRunningRowIntegration (#1605, leader overlap): a
// tick whose snapshot read the row as `none` (a deposed leader inside its
// detection window) flushes none -> scheduled after another leader already
// dispatched the task and its agent reported RUNNING. The batch is guarded on
// the snapshot's state, so the running row is left alone: no second attempt is
// claimed and the running attempt's SUCCESS report still lands.
func TestTickBatchDoesNotRegressRunningRowIntegration(t *testing.T) {
	pg, repo, sched, exec, ctx := openExecPG(t)
	dagID := fmt.Sprintf("tick_batch_running_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{{TaskID: "load", Type: domain.TaskTypePython}}
	registerSpec(t, repo, ctx, dagID, tasks)
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateDagRun: %v", err)
	}
	runUUID := resolveRunUUID(t, sched, ctx, dagID)
	if err := sched.MaterializeTasks(ctx, runUUID, tasks); err != nil {
		t.Fatalf("MaterializeTasks: %v", err)
	}

	// The current leader: none -> scheduled -> claim -> queued -> agent RUNNING.
	if applied, err := sched.ApplyTransitions(ctx, runUUID, []string{"load"}, domain.TaskStateNone, domain.TaskStateScheduled); err != nil || len(applied) != 1 {
		t.Fatalf("ApplyTransitions to scheduled = %v, %v; want [load]", applied, err)
	}
	res, err := exec.ResolveTask(ctx, runUUID, "load")
	if err != nil {
		t.Fatalf("ResolveTask: %v", err)
	}
	if ok, err := sched.MarkQueued(ctx, runUUID, "load", nil); err != nil || !ok {
		t.Fatalf("MarkQueued = %v, %v; want true", ok, err)
	}
	id := auth.AgentIdentity{TenantID: res.TenantID, DagID: dagID, RunID: runUUID, TaskID: "load",
		TryNumber: res.TryNumber, AttemptEpoch: res.AttemptEpoch, HasAttemptEpoch: true}
	if err := exec.ReportState(ctx, id, domain.TaskStateRunning, 0, ""); err != nil {
		t.Fatalf("RUNNING report: %v", err)
	}

	// The deposed leader flushes the batch its stale snapshot planned.
	applied, err := sched.ApplyTransitions(ctx, runUUID, []string{"load"}, domain.TaskStateNone, domain.TaskStateScheduled)
	if err != nil {
		t.Fatalf("stale ApplyTransitions: %v", err)
	}
	if len(applied) != 0 {
		t.Errorf("stale batch reported %v as applied, want none", applied)
	}
	var state string
	if err := pg.Pool.QueryRow(ctx, `SELECT state::text FROM task_instances WHERE dag_run_id=$1::uuid AND task_id='load'`, runUUID).Scan(&state); err != nil {
		t.Fatalf("read state: %v", err)
	}
	if state != string(domain.TaskStateRunning) {
		t.Fatalf("row state after the stale batch write = %s, want running", state)
	}
	if err := exec.ReportState(ctx, id, domain.TaskStateSuccess, 0, ""); err != nil {
		t.Errorf("the running attempt's SUCCESS report was rejected: %v", err)
	}
}

// markDuringDispatch is the synchronous dispatcher of one leader's tick. While
// the tick dispatches "w", a user marks the not-yet-scheduled task "x" failed
// through the real mark-state write.
type markDuringDispatch struct {
	t     *testing.T
	repo  *storage.Repository
	dagID string
	done  bool
}

func (d *markDuringDispatch) Dispatch(ctx context.Context, _, _, _ string, task domain.TaskSpec) (executor.Disposition, error) {
	if task.TaskID == "w" && !d.done {
		d.done = true
		if err := d.repo.SetTaskInstanceState(ctx, "default", d.dagID, "r", "x", "failed"); err != nil {
			d.t.Errorf("mark failed: %v", err)
		}
	}
	return executor.Dispatched, nil
}

// TestTickBatchKeepsUserMarkIntegration (#1605, single leader): the tick reads
// its snapshot, dispatches the run's ready tasks inline, and only then flushes
// the batched none -> scheduled writes. A user's mark-failed that lands during
// the dispatch loop must survive the flush, through the real Scheduler.Step.
func TestTickBatchKeepsUserMarkIntegration(t *testing.T) {
	pg, repo, sched, _, ctx := openExecPG(t)
	dagID := fmt.Sprintf("tick_batch_mark_%d", time.Now().UnixNano())
	spec := domain.DAGSpec{SchemaVersion: "1.0", DagID: dagID, DagVersion: "v1", Image: "img:v1",
		Tasks: []domain.TaskSpec{
			{TaskID: "root", Type: domain.TaskTypePython, Entrypoint: "dag:f"},
			{TaskID: "w", Type: domain.TaskTypePython, Entrypoint: "dag:f"},
			{TaskID: "x", Type: domain.TaskTypePython, Entrypoint: "dag:f", DependsOn: []string{"root"}},
		}}
	hash, err := spec.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RegisterDagVersion(ctx, "default", spec, hash); err != nil {
		t.Fatal(err)
	}
	// Other tests' active runs would be advanced by this Step too.
	if _, err := pg.Pool.Exec(ctx, `UPDATE dag_runs SET state='success' WHERE state IN ('queued','running')`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r", State: domain.DagRunStateQueued, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	d := &markDuringDispatch{t: t, repo: repo, dagID: dagID}
	s := scheduler.NewScheduler(sched, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Millisecond)
	s.SetDispatcher(d)
	s.SetLeading(true)
	if err := s.Step(ctx); err != nil { // materializes the run
		t.Fatal(err)
	}
	runUUID := resolveRunUUID(t, sched, ctx, dagID)
	// root succeeded, w is ready to dispatch, x waits on root (still none).
	if _, err := pg.Pool.Exec(ctx, `UPDATE task_instances SET state = CASE task_id
		WHEN 'root' THEN 'success'::task_state WHEN 'w' THEN 'scheduled'::task_state ELSE 'none'::task_state END
		WHERE dag_run_id=$1::uuid`, runUUID); err != nil {
		t.Fatal(err)
	}
	if err := s.Step(ctx); err != nil { // dispatches w (the user marks x meanwhile), then flushes x
		t.Fatal(err)
	}
	if !d.done {
		t.Fatal("the tick never dispatched w, so the race was not exercised")
	}
	var x string
	if err := pg.Pool.QueryRow(ctx, `SELECT state::text FROM task_instances WHERE dag_run_id=$1::uuid AND task_id='x'`, runUUID).Scan(&x); err != nil {
		t.Fatal(err)
	}
	if x != string(domain.TaskStateFailed) {
		t.Errorf("x after the tick = %s, want the user's failed verdict", x)
	}
}
