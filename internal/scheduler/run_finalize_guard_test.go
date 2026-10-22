package scheduler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
)

// TestFinalizeGuardsOnTheSnapshotRunState (#2047): the run-level finalize
// write is conditioned on the run state the tick's snapshot read, so a run a
// clear re-opened after the snapshot is not overwritten.
func TestFinalizeGuardsOnTheSnapshotRunState(t *testing.T) {
	store := newFakeStore(RunState{
		RunID: "r1", DagID: "etl", State: domain.DagRunStateRunning, Tasks: linearTasks(),
		States: map[string]domain.TaskState{"a": domain.TaskStateSuccess, "b": domain.TaskStateSuccess},
	})
	s := newScheduler(store)

	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	from, ok := store.finalizeFrom["r1"]
	if !ok {
		t.Fatal("the finished run was not finalized")
	}
	if from != domain.DagRunStateRunning {
		t.Errorf("the finalize write was guarded on %q, want the snapshot's %q", from, domain.DagRunStateRunning)
	}
}

// TestFinalizeSupersededIsNotAnErrorAndDoesNotAlert (#2047): a finalize that
// finds the run already moved on (a clear re-opened it, a user marked it) is
// the guard working, not a tick failure. It is metered as
// finalize_run_superseded, and the run is not alerted on: the failure the
// tick saw is no longer the run's state.
func TestFinalizeSupersededIsNotAnErrorAndDoesNotAlert(t *testing.T) {
	store := newFakeStore(exhaustedFailedRun("r1"))
	store.finalizeSuperseded = map[string]bool{"r1": true}
	rec := &fakeRecorder{}
	al := &recordingAlerter{ch: make(chan RunState, 1)}
	s := newScheduler(store)
	s.SetRecorder(rec)
	s.SetAlerter(al)

	if err := s.Step(context.Background()); err != nil {
		t.Fatalf("Step: %v", err)
	}
	if got := strings.Count(strings.Join(rec.decisions, ","), "finalize_run_superseded"); got != 1 {
		t.Errorf("a finalize that found the run moved on is metered once: %d finalize_run_superseded decisions, want 1 (%v)", got, rec.decisions)
	}
	if got := store.attemptsFor("r1"); got != 0 {
		t.Errorf("a superseded finalize claimed %d alert attempts, want 0", got)
	}
	select {
	case <-al.ch:
		t.Error("a superseded finalize must not alert")
	case <-time.After(150 * time.Millisecond):
	}
}
