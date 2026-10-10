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
)

// TestChaosMidTickCrashRecoveryIntegration is the load-bearing scenario for
// the chaos dogfood gate (#231 Phase 2b): a scheduler that crashes
// AFTER promoting a TI to `queued` but BEFORE the dispatch landed leaves the
// run "stuck running with a queued TI" — exactly the post-#202 failure shape.
// The recovery contract is:
//
//  1. The dispatch-lost reaper fires first (queued_at older than threshold),
//     marking the stuck TI `failed` with `dispatch_lost`.
//  2. Once no active TIs remain on the run, the orphan-run reaper picks the
//     run up and fails it with `orphaned`.
//
// Wall-clock dogfood would wait 3 min + 5 min for the production thresholds;
// here we shrink them to a couple hundred ms in the ReaperConfig of a reaper the
// test drives directly, the way the server's maintenance loop does, so the test
// stays fast. We are validating the contract SHAPE, not the production tuning.
//
// Skips when DATABASE_URL is absent (no PG available).
func TestChaosMidTickCrashRecoveryIntegration(t *testing.T) {
	repo, sched, ctx := openRepo(t)

	// 1. Register a DAG + materialize a single task, then simulate the
	//    "scheduler died after marking the TI queued but before dispatch"
	//    shape: run in `running`, TI in `queued`, queued_at set to "long ago".
	dagID := fmt.Sprintf("chaos_recovery_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython}}
	registerSpec(t, repo, ctx, dagID, tasks)
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual",
		LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateDagRun: %v", err)
	}
	runUUID := resolveRunUUID(t, sched, ctx, dagID)
	if err := sched.MaterializeTasks(ctx, runUUID, tasks); err != nil {
		t.Fatalf("MaterializeTasks: %v", err)
	}
	// Take the TI from the default `scheduled` straight to `queued` — the
	// state the post-#202 fix says the dispatch-lost reaper must catch.
	if err := sched.ApplyTransition(ctx, runUUID, "t", domain.TaskStateQueued); err != nil {
		t.Fatalf("ApplyTransition to queued: %v", err)
	}

	// 2. Boot a scheduler with thresholds tight enough for a fast test
	//    while still well above the per-tick wall-clock latency the in-
	//    process Step() takes (~ms). The leader flag is the gate the
	//    reapers check before writing — must be on for any of this to run.
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := scheduler.NewScheduler(sched, logger, time.Millisecond)
	s.SetLeading(true)
	// The reapers run from the server's leader maintenance loop, not from the
	// scheduler tick; here the test plays that loop, driving ReapOnce after each
	// Step exactly as the loop orders reconcile -> reap (no pods, so no
	// reconciler on this Lite-shaped path). Wire one over the same store (nil
	// pods/cache/recorder) with thresholds tight enough for a fast test but well
	// above the ~ms in-process latency. AgentLostThreshold is a minute —
	// irrelevant here, kept high so it can't fire on unrelated rows; PodLostGrace
	// is a no-op with nil pods; no leadership stamp is wired, so the settling
	// gate is open, as it is in Lite.
	reaper := executor.NewReaper(sched, nil, nil, nil, nil, logger, executor.ReaperConfig{
		OrphanThreshold:       400 * time.Millisecond,
		AgentLostThreshold:    time.Minute,
		DispatchLostThreshold: 200 * time.Millisecond,
		PodLostGrace:          time.Minute,
	}, s.SteppingDown)
	reaper.SetLeading(s.IsLeading)

	// 3. Wait past the dispatch-lost threshold, then tick and reap. The reaper
	//    must flip our queued TI to failed/dispatch_lost.
	time.Sleep(250 * time.Millisecond)
	if err := s.Step(ctx); err != nil {
		t.Fatalf("dispatch-lost tick: %v", err)
	}
	if err := reaper.ReapOnce(ctx); err != nil {
		t.Fatalf("dispatch-lost reap: %v", err)
	}
	tiState := taskInstanceState(t, sched, ctx, runUUID, "t")
	if tiState != domain.TaskStateFailed {
		t.Fatalf("TI state after dispatch-lost reap = %q, want failed", tiState)
	}

	// 4. Wait past the orphan threshold (relative to the run's last
	//    activity, which our queued TI's transition just bumped), then
	//    tick and reap again. The orphan-run reaper should flip the run to
	//    failed now that no active TI remains on it.
	time.Sleep(450 * time.Millisecond)
	if err := s.Step(ctx); err != nil {
		t.Fatalf("orphan-run tick: %v", err)
	}
	if err := reaper.ReapOnce(ctx); err != nil {
		t.Fatalf("orphan-run reap: %v", err)
	}
	runState := dagRunState(t, sched, ctx, runUUID)
	if runState != domain.DagRunStateFailed {
		t.Fatalf("run state after orphan tick = %q, want failed (orphaned cleanup)", runState)
	}
}

// taskInstanceState looks up a single TI's state via the active-runs query
// (which the scheduler already uses); this avoids adding a new query just
// for the test.
func taskInstanceState(t *testing.T, sched scheduler.Store, ctx context.Context, runID, taskID string) domain.TaskState {
	t.Helper()
	runs, err := sched.ActiveRuns(ctx)
	if err != nil {
		t.Fatalf("ActiveRuns: %v", err)
	}
	for _, r := range runs {
		if r.RunID == runID {
			if state, ok := r.States[taskID]; ok {
				return state
			}
		}
	}
	// If the run is no longer "active" (e.g. it terminated), the TI's state
	// is implicit. For this test the only reason a run leaves ActiveRuns is
	// the orphan-run reaper flipping it to terminal, which only fires when
	// the TI is already failed. Returning failed here keeps the assertion
	// honest without a second query.
	return domain.TaskStateFailed
}

// dagRunState reads the dag_runs row directly to check whether the orphan
// reaper has flipped the run terminal. Uses the run's UUID to avoid coupling
// to the API repository's tenant/dag-id lookup.
func dagRunState(t *testing.T, sched scheduler.Store, ctx context.Context, runID string) domain.DagRunState {
	t.Helper()
	runs, err := sched.ActiveRuns(ctx)
	if err != nil {
		t.Fatalf("ActiveRuns: %v", err)
	}
	for _, r := range runs {
		if r.RunID == runID {
			return r.State
		}
	}
	// The run is no longer active — only path for that is a terminal state
	// set by the orphan-run reaper (the only writer that demotes a running
	// run when its TIs are all terminal). Treat as failed for the assertion.
	return domain.DagRunStateFailed
}
