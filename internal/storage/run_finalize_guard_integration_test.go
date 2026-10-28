//go:build integration

package storage_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
	"github.com/dexadata/dexaflow/internal/scheduler"
	"github.com/dexadata/dexaflow/internal/storage"
)

// clearDuringDispatch is the synchronous dispatcher of one tick. While the
// tick dispatches "w" of an earlier run, a user clears task "t" of a later run
// the same tick already read as finished, through the real clear.
type clearDuringDispatch struct {
	t     *testing.T
	repo  *storage.Repository
	dagID string
	done  bool
}

func (d *clearDuringDispatch) Dispatch(ctx context.Context, _, _, _ string, task domain.TaskSpec) (executor.Disposition, error) {
	if task.TaskID == "w" && !d.done {
		d.done = true
		if _, err := d.repo.ClearTaskInstances(ctx, "default", d.dagID, "rb", []string{"t"}, false,
			domain.ClearOptions{ResetDagRun: true}); err != nil {
			d.t.Errorf("clear: %v", err)
		}
	}
	return executor.Dispatched, nil
}

// TestTickFinalizeKeepsClearedRunIntegration (#2047): the tick reads every
// active run, then advances them in turn. A clear that re-opens a run after
// the read and before that run's finalize must survive it: the run stays
// queued with its task reset, not success over a task instance in none.
func TestTickFinalizeKeepsClearedRunIntegration(t *testing.T) {
	pg, repo, sched, _, ctx := openExecPG(t)
	suffix := time.Now().UnixNano()
	dagA := fmt.Sprintf("finalize_guard_a_%d", suffix)
	dagB := fmt.Sprintf("finalize_guard_b_%d", suffix)
	registerSpec(t, repo, ctx, dagA, []domain.TaskSpec{{TaskID: "w", Type: domain.TaskTypePython, Entrypoint: "dag:f"}})
	registerSpec(t, repo, ctx, dagB, []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython, Entrypoint: "dag:f"}})
	// Other tests' active runs would be advanced by this Step too.
	if _, err := pg.Pool.Exec(ctx, `UPDATE dag_runs SET state='success' WHERE state IN ('queued','running')`); err != nil {
		t.Fatal(err)
	}

	// Run A comes first in the tick and has a task to dispatch.
	if _, err := repo.CreateDagRun(ctx, "default", dagA, domain.DagRun{
		RunID: "ra", State: domain.DagRunStateQueued, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	d := &clearDuringDispatch{t: t, repo: repo, dagID: dagB}
	s := newTestScheduler(sched)
	s.SetDispatcher(d)
	if err := s.Step(ctx); err != nil { // materializes and starts run A
		t.Fatal(err)
	}
	runA := resolveRunUUID(t, sched, ctx, dagA)
	if _, err := pg.Pool.Exec(ctx, `UPDATE task_instances SET state='scheduled' WHERE dag_run_id=$1::uuid`, runA); err != nil {
		t.Fatal(err)
	}

	// Run B comes second: running, its only task already succeeded, so this
	// tick finalizes it.
	if _, err := repo.CreateDagRun(ctx, "default", dagB, domain.DagRun{
		RunID: "rb", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runB := resolveRunUUID(t, sched, ctx, dagB)
	if err := sched.MaterializeTasks(ctx, runB, []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython}}); err != nil {
		t.Fatal(err)
	}
	if err := sched.ApplyTransition(ctx, runB, "t", domain.TaskStateSuccess); err != nil {
		t.Fatal(err)
	}

	if err := s.Step(ctx); err != nil { // dispatches w (the user clears run B meanwhile), then finalizes B
		t.Fatal(err)
	}
	if !d.done {
		t.Fatal("the tick never dispatched w, so the race was not exercised")
	}
	var runState, tiState string
	if err := pg.Pool.QueryRow(ctx, `SELECT state::text FROM dag_runs WHERE id=$1::uuid`, runB).Scan(&runState); err != nil {
		t.Fatal(err)
	}
	if err := pg.Pool.QueryRow(ctx, `SELECT state::text FROM task_instances WHERE dag_run_id=$1::uuid AND task_id='t'`, runB).Scan(&tiState); err != nil {
		t.Fatal(err)
	}
	if runState != string(domain.DagRunStateQueued) || tiState != string(domain.TaskStateNone) {
		t.Errorf("run B after the tick = %s with t %s, want the clear's queued with t none", runState, tiState)
	}
}

// newTestScheduler is a leading scheduler over the real store, logging nowhere.
func newTestScheduler(store *storage.SchedulerStore) *scheduler.Scheduler {
	s := scheduler.NewScheduler(store, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Millisecond)
	s.SetLeading(true)
	return s
}
