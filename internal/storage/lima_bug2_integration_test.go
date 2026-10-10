//go:build integration

package storage_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
	"github.com/dexadata/dexaflow/internal/scheduler"
)

// recordingDispatcher captures every Dispatch() call so the test can assert
// the scheduler actually hands the task off to the executor layer for both
// scheduled and manual runs.
type recordingDispatcher struct {
	mu    sync.Mutex
	calls []dispatchCall
	err   error // optional: error to return from every Dispatch
}

type dispatchCall struct {
	runID, dagID string
	taskID       string
	at           time.Time
}

func (d *recordingDispatcher) Dispatch(_ context.Context, runID, dagID, _ string, task domain.TaskSpec) (executor.Disposition, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, dispatchCall{runID: runID, dagID: dagID, taskID: task.TaskID, at: time.Now()})
	if d.err != nil {
		return executor.Rejected, d.err
	}
	return executor.Dispatched, nil
}

func (d *recordingDispatcher) callCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.calls)
}

// callsFor returns only the dispatch calls for the given dagID — needed because
// the integration test DB is shared and leaked state from other tests can put
// unrelated TIs into queued, polluting a count-based assertion.
func (d *recordingDispatcher) callsFor(dagID string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, c := range d.calls {
		if c.dagID == dagID {
			n++
		}
	}
	return n
}

// TestLimaBug2_ManualTriggerReachesDispatcherIntegration reproduces the
// "manual trigger never dispatches" symptom from Lima 2026-05-31:
//
//	21:21:35 POST /api/v2/dags/leoflow/dagRuns (manual)
//	21:21:37 TI c4395b92 queued_at stamped (so it reached `queued` state)
//	... then SILENCE for 13 minutes ...
//	21:34:57 WARN task queued past dispatch threshold; failing as dispatch_lost
//
// Hypothesis being tested: did the scheduler ever call dispatcher.Dispatch()
// for this manual run? If YES → the bug is in the executor (subprocess silently
// failed). If NO → the bug is in the scheduler skipping manual run_type.
//
// This test creates a manual run, ticks the scheduler twice (first tick
// materializes + flips run to running; second tick plans transitions and
// launches queued tasks), and asserts the recording dispatcher saw at least
// one Dispatch call.
func TestLimaBug2_ManualTriggerReachesDispatcherIntegration(t *testing.T) {
	repo, sched, ctx := openRepo(t)

	dagID := fmt.Sprintf("lima_bug2_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{{TaskID: "hello", Type: domain.TaskTypePython, Entrypoint: "dag:hello"}}
	registerSpec(t, repo, ctx, dagID, tasks)

	// Replicate exactly what createDagRunHandler does for a POST /dagRuns
	// (state=queued, run_type=manual, queued_at=now). This is the ONLY path
	// that produces a manual run, so faithfulness here is the test contract.
	// Use the EXACT runID shape createDagRunHandler produces:
	// "manual__" + logical.Format(time.RFC3339). RFC3339 contains ":" — if any
	// downstream layer mishandles colons in run_id, this is the case that surfaces it.
	now := time.Now().UTC()
	runID := "manual__" + now.Format(time.RFC3339)
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID:       runID,
		LogicalDate: now,
		State:       domain.DagRunStateQueued,
		RunType:     "manual",
		QueuedAt:    now,
	}); err != nil {
		t.Fatalf("CreateDagRun (manual): %v", err)
	}

	disp := &recordingDispatcher{}
	s := scheduler.NewScheduler(sched, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Millisecond)
	s.SetLeading(true)
	s.SetDispatcher(disp)

	// Tick 1: advance() sees the queued run with no TIs yet → materializes
	// + flips run to running. dispatcher.Dispatch() NOT expected this tick.
	if err := s.Step(ctx); err != nil {
		t.Fatalf("Step tick 1: %v", err)
	}
	if disp.callsFor(dagID) > 0 {
		t.Errorf("after tick 1, dispatcher was called %d times — should only be after TI is queued", disp.callsFor(dagID))
	}

	// Tick 2: advance() now plans transitions for the materialized TIs.
	// PlanRun should move our root task from `none` → `scheduled` → `queued`
	// in one shot (no upstream gates), then launchQueued must call Dispatch.
	if err := s.Step(ctx); err != nil {
		t.Fatalf("Step tick 2: %v", err)
	}

	// Some PlanRun implementations might need a 3rd tick to reach `queued`.
	// Give it ONE more chance before failing — but log whether it was needed.
	calls := disp.callsFor(dagID)
	if calls == 0 {
		if err := s.Step(ctx); err != nil {
			t.Fatalf("Step tick 3 (fallback): %v", err)
		}
		calls = disp.callsFor(dagID)
		if calls > 0 {
			t.Logf("note: manual trigger needed 3 ticks (not 2) to reach Dispatch — fine, just slower than scheduled path")
		}
	}

	if calls == 0 {
		t.Fatalf("Bug 2: after 3 scheduler ticks, dispatcher.Dispatch() was NEVER called for the manual run — the scheduler is dropping manual run_type somewhere in advance/launchQueued")
	}
	if calls != 1 {
		t.Errorf("dispatcher called %d times for a single-task manual run; want exactly 1 (verify launchQueued is idempotent)", calls)
	}
}
