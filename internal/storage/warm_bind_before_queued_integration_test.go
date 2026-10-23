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
)

// noTaskPods is a PodManager fake for warm attempts: no attempt has a task pod
// (a warm attempt runs in a shared warm pod), so every presence read is Absent.
type noTaskPods struct{}

func (noTaskPods) DeleteTaskPod(context.Context, executor.Attempt) (executor.Teardown, error) {
	return executor.Teardown{}, nil
}
func (noTaskPods) DeleteRunPods(context.Context, string) (executor.Teardown, error) {
	return executor.Teardown{}, nil
}
func (noTaskPods) TaskPodPresence(context.Context, executor.Attempt) (executor.PodPresence, error) {
	return executor.PodPresenceAbsent, nil
}
func (noTaskPods) TaskPodPending(context.Context, executor.Attempt) (bool, error) { return false, nil }

// TestWarmAckBeforeQueuedKeepsBindingIntegration (#1600) drives the synchronous
// dispatch order with warm pools on: Dispatch claims the epoch and the warm
// placer hands the attempt to a worker, and only after Dispatch returns does the
// tick write scheduled -> queued (MarkQueued). The worker can ack (bind) and
// report RUNNING before that write. The bind must still apply on the scheduled
// row, so the running attempt counts as busy and the pod-lost reaper, which has
// no task pod to find for a warm attempt, leaves it running.
func TestWarmAckBeforeQueuedKeepsBindingIntegration(t *testing.T) {
	pg, repo, sched, exec, ctx := openExecPG(t)
	dagID := fmt.Sprintf("warm_bind_scheduled_%d", time.Now().UnixNano())
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
	if applied, err := sched.ApplyTransitions(ctx, runUUID, []string{"load"}, domain.TaskStateNone, domain.TaskStateScheduled); err != nil || len(applied) != 1 {
		t.Fatalf("ApplyTransitions scheduled: applied=%v err=%v", applied, err)
	}

	// Dispatch: ResolveTask claims the epoch, the warm placer assigns the attempt.
	res, err := exec.ResolveTask(ctx, runUUID, "load")
	if err != nil {
		t.Fatalf("ResolveTask: %v", err)
	}
	// The worker acks started=true while the row is still scheduled.
	pod := fmt.Sprintf("dexaflow-warm-%d", time.Now().UnixNano())
	if err := exec.BindWarmAttempt(ctx, runUUID, "load", res.TryNumber, res.AttemptEpoch, pod); err != nil {
		t.Fatalf("BindWarmAttempt: %v", err)
	}
	if got := warmWorkerID(t, pg, ctx, runUUID, "load", res.TryNumber); got == nil || *got != pod {
		t.Fatalf("warm_worker_id after a bind on the scheduled row = %v, want %q", got, pod)
	}
	// The child reports RUNNING, then the tick's MarkQueued finds the row moved on.
	id := auth.AgentIdentity{TenantID: res.TenantID, DagID: dagID, RunID: runUUID, TaskID: "load",
		TryNumber: res.TryNumber, AttemptEpoch: res.AttemptEpoch, HasAttemptEpoch: true}
	if err := exec.ReportState(ctx, id, domain.TaskStateRunning, 0, ""); err != nil {
		t.Fatalf("ReportState running: %v", err)
	}
	if _, err := sched.MarkQueued(ctx, runUUID, "load", nil); err != nil {
		t.Fatalf("MarkQueued: %v", err)
	}

	busy, err := sched.ListBusyWarmWorkerPods(ctx)
	if err != nil {
		t.Fatalf("ListBusyWarmWorkerPods: %v", err)
	}
	if !busy[pod] {
		t.Fatalf("worker %s running the attempt is not listed busy", pod)
	}

	// Pod-lost reaper with no grace (production: 60 s).
	cfg := executor.DefaultReaperConfig()
	cfg.PodLostGrace = 0
	cfg.SettlingGrace = 0
	r := executor.NewReaper(sched, noTaskPods{}, nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), cfg, nil)
	time.Sleep(10 * time.Millisecond)
	if err := r.ReapOnce(ctx); err != nil {
		t.Fatalf("ReapOnce: %v", err)
	}
	var state, msg string
	if err := pg.Pool.QueryRow(ctx,
		`SELECT state::text, COALESCE(error_message,'') FROM task_instances WHERE dag_run_id=$1::uuid AND task_id='load'`,
		runUUID).Scan(&state, &msg); err != nil {
		t.Fatalf("reading state: %v", err)
	}
	if state != "running" {
		t.Fatalf("live warm attempt after the pod-lost reaper: state=%s error=%q, want running", state, msg)
	}
}
