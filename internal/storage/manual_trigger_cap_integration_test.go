//go:build integration

package storage_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/scheduler"
	"github.com/dexadata/dexaflow/internal/storage"
)

// registerCapped registers a one-task DAG with the given max_active_runs and,
// when schedule is set, a daily catchup schedule from 2026-01-01, then
// unpauses it so the tick starts its runs.
func registerCapped(t *testing.T, ctx context.Context, repo *storage.Repository, dagID string, maxActive int, scheduled bool) {
	t.Helper()
	spec := domain.DAGSpec{
		SchemaVersion: "1.0", DagID: dagID, DagVersion: "v1", Image: "img:v1", MaxActiveRuns: maxActive,
		Tasks: []domain.TaskSpec{{TaskID: "a", Type: domain.TaskTypePython, Entrypoint: "dag:a"}},
	}
	if scheduled {
		daily := "@daily"
		spec.Schedule, spec.ScheduleTZ = &daily, "UTC"
		spec.StartDate, spec.Catchup = "2026-01-01T00:00:00Z", true
	}
	hash, err := spec.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RegisterDagVersion(ctx, "default", spec, hash); err != nil {
		t.Fatalf("register: %v", err)
	}
	setPaused(t, ctx, repo, dagID, false)
}

// triggerQueued creates a manual run in queued, as the trigger endpoint does,
// and fails the test when the repository refuses it.
func triggerQueued(t *testing.T, ctx context.Context, repo *storage.Repository, dagID, runID string, logical time.Time) {
	t.Helper()
	run, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: runID, State: domain.DagRunStateQueued, RunType: "manual", LogicalDate: logical,
	})
	if err != nil {
		t.Fatalf("trigger %s at the max_active_runs cap: %v, want the run accepted as queued", runID, err)
	}
	if run.State != domain.DagRunStateQueued {
		t.Fatalf("trigger %s: state %q, want queued", runID, run.State)
	}
}

// runStateCounts counts the active runs of dagID by state, and the scheduled
// ones among them (every run whose id is not one of the manual "m" ids).
func runStateCounts(t *testing.T, ctx context.Context, store *storage.SchedulerStore, dagID string) (running, queued, scheduled int) {
	t.Helper()
	for _, r := range activeRuns(t, ctx, store, dagID) {
		switch r.State {
		case domain.DagRunStateRunning:
			running++
		case domain.DagRunStateQueued:
			queued++
		case domain.DagRunStateSuccess, domain.DagRunStateFailed:
		}
		if !strings.HasPrefix(r.DisplayRunID, "m") {
			scheduled++
		}
	}
	return running, queued, scheduled
}

func newCapScheduler(store *storage.SchedulerStore) *scheduler.Scheduler {
	sched := scheduler.NewScheduler(store, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Millisecond)
	sched.SetDispatcher(noopDispatcher{})
	sched.SetLeading(true)
	return sched
}

// TestManualTriggerAtMaxActiveRunsQueuesIntegration (#2426): with
// max_active_runs=1 and one queued run, a second manual trigger is accepted as
// queued, as Airflow does, instead of answering 409. The tick starts only the
// oldest, and the second starts once the first finishes.
func TestManualTriggerAtMaxActiveRunsQueuesIntegration(t *testing.T) {
	repo, store, ctx := openRepo(t)
	sched := newCapScheduler(store)
	dagID := fmt.Sprintf("manual_cap_%d", time.Now().UnixNano())
	registerCapped(t, ctx, repo, dagID, 1, false)
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	triggerQueued(t, ctx, repo, dagID, "r1", day)
	triggerQueued(t, ctx, repo, dagID, "r2", day.Add(time.Hour))

	step := func(n int) {
		t.Helper()
		for i := 0; i < n; i++ {
			if err := sched.Step(ctx); err != nil {
				t.Fatalf("step: %v", err)
			}
		}
	}
	step(3)
	if s1, s2 := dagRunStateOf(t, ctx, repo, dagID, "r1"), dagRunStateOf(t, ctx, repo, dagID, "r2"); s1 != domain.DagRunStateRunning || s2 != domain.DagRunStateQueued {
		t.Fatalf("max_active_runs=1 with two triggers: r1 %q, r2 %q, want running and queued", s1, s2)
	}
	if st := taskStates(t, ctx, repo, dagID, "r2")["a"]; st == domain.TaskStateQueued || st == domain.TaskStateRunning {
		t.Fatalf("the run left queued dispatches nothing: r2 task a = %q", st)
	}
	if err := store.ApplyTransition(ctx, activeRuns(t, ctx, store, dagID)["r1"].RunID, "a", domain.TaskStateSuccess); err != nil {
		t.Fatalf("finish r1's task: %v", err)
	}
	step(3)
	if s1, s2 := dagRunStateOf(t, ctx, repo, dagID, "r1"), dagRunStateOf(t, ctx, repo, dagID, "r2"); s1 != domain.DagRunStateSuccess || s2 != domain.DagRunStateRunning {
		t.Fatalf("after r1 finishes: r1 %q, r2 %q, want success and running", s1, s2)
	}
}

// TestCatchupWithManualTriggersStaysUnderCapIntegration (#2426): a catchup DAG
// with max_active_runs=2 takes three manual triggers past its cap. Scheduled
// creation still stops while the DAG has two runs queued or running, and no
// more than two runs are ever running, while the triggers drain and while the
// catchup resumes after them.
func TestCatchupWithManualTriggersStaysUnderCapIntegration(t *testing.T) {
	repo, store, ctx := openRepo(t)
	sched := newCapScheduler(store)
	dagID := fmt.Sprintf("catchup_cap_%d", time.Now().UnixNano())
	registerCapped(t, ctx, repo, dagID, 2, true)
	// Paused while the triggers land, so the schedule does not race them.
	setPaused(t, ctx, repo, dagID, true)
	base := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	for i, id := range []string{"m1", "m2", "m3"} {
		triggerQueued(t, ctx, repo, dagID, id, base.Add(time.Duration(i)*time.Hour))
	}
	setPaused(t, ctx, repo, dagID, false)

	sawScheduled := false
	for round := 0; round < 12; round++ {
		if err := sched.Step(ctx); err != nil {
			t.Fatalf("step: %v", err)
		}
		running, queued, scheduled := runStateCounts(t, ctx, store, dagID)
		if running > 2 {
			t.Fatalf("round %d: %d runs running, want at most max_active_runs=2", round, running)
		}
		manualLeft := 0
		for _, id := range []string{"m1", "m2", "m3"} {
			if st := dagRunStateOf(t, ctx, repo, dagID, id); st == domain.DagRunStateQueued || st == domain.DagRunStateRunning {
				manualLeft++
			}
		}
		if manualLeft >= 2 && scheduled > 0 {
			t.Fatalf("round %d: %d scheduled runs created while %d manual runs fill the cap", round, scheduled, manualLeft)
		}
		if scheduled > 0 {
			sawScheduled = true
			if running+queued > 2 && manualLeft == 0 {
				t.Fatalf("round %d: catchup created past the cap: %d running, %d queued", round, running, queued)
			}
		}
		// Finish every running run's task, so the next tick frees its slot.
		for _, r := range activeRuns(t, ctx, store, dagID) {
			if r.State != domain.DagRunStateRunning {
				continue
			}
			if st := taskStates(t, ctx, repo, dagID, r.DisplayRunID)["a"]; st == domain.TaskStateQueued || st == domain.TaskStateRunning {
				if err := store.ApplyTransition(ctx, r.RunID, "a", domain.TaskStateSuccess); err != nil {
					t.Fatalf("finish %s: %v", r.DisplayRunID, err)
				}
			}
		}
	}
	for _, id := range []string{"m1", "m2", "m3"} {
		if st := dagRunStateOf(t, ctx, repo, dagID, id); st != domain.DagRunStateSuccess {
			t.Errorf("manual run %s: state %q, want success after the triggers drain", id, st)
		}
	}
	if !sawScheduled {
		t.Error("the catchup never resumed once the manual runs finished")
	}
}
