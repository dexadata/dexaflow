package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
)

// stoppingStore is a fakeStore with the stop seam: it records each tenant it
// was asked to stop and answers with failed, or err.
type stoppingStore struct {
	*fakeStore
	stopped []string
	failed  int64
	err     error
}

func (s *stoppingStore) FailStoppedTenantTaskInstances(_ context.Context, tenantID string) (int64, error) {
	s.stopped = append(s.stopped, tenantID)
	return s.failed, s.err
}

// TestStepDispatchesNothingOfADrainedTenant: under drain and stop the tick
// leaves every ready task of the tenant scheduled, with nothing charged, and
// does not start its queued run (#2181). Hold and other tenants dispatch as
// usual.
func TestStepDispatchesNothingOfADrainedTenant(t *testing.T) {
	for _, mode := range []domain.SuspensionMode{domain.SuspensionDrain, domain.SuspensionStop} {
		t.Run(string(mode), func(t *testing.T) {
			drained := readyRun("a", "t1", 3)
			drained.Suspension = mode
			held := readyRun("b", "t2", 2)
			held.Suspension = domain.SuspensionHold
			waiting := RunState{RunID: "q", DagID: "etl", TenantID: "t1", State: domain.DagRunStateQueued, Suspension: mode,
				Tasks: linearTasks(), States: map[string]domain.TaskState{}}
			store := newFakeStore(drained, held, waiting)
			clock := &fakeClock{now: time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)}
			disp := &slowDispatcher{clock: clock}
			s := newScheduler(store)
			s.clock = clock.Now
			s.SetDispatcher(disp)

			if err := s.Step(context.Background()); err != nil {
				t.Fatal(err)
			}
			if n := countRun(disp.runs, "a"); n != 0 {
				t.Errorf("a %s tenant must get no dispatch, got %d", mode, n)
			}
			if n := countRun(disp.runs, "b"); n != 2 {
				t.Errorf("a held tenant's ready tasks still dispatch, got %d of 2", n)
			}
			if len(store.dispatchFailures)+len(store.dispatchBackpressure)+len(store.dispatchExhausted) != 0 {
				t.Errorf("a held dispatch must charge nothing, got failures=%v backpressure=%v exhausted=%v",
					store.dispatchFailures, store.dispatchBackpressure, store.dispatchExhausted)
			}
			if _, ok := store.runStates["q"]; ok {
				t.Errorf("a queued run of a %s tenant must stay queued, got %q", mode, store.runStates["q"])
			}
			for _, id := range store.materialize {
				if id == "q" {
					t.Errorf("a queued run of a %s tenant must not be materialized", mode)
				}
			}
			if s.deferredRun != "" {
				t.Errorf("a suspension is not the tick budget: deferredRun = %q", s.deferredRun)
			}
		})
	}
}

// TestStepStopsATenantSuspendedInStopMode: the tick asks the store, once per
// tenant, to fail the task instances of a tenant suspended in stop mode that
// has one queued or running; a drained tenant, a stopped tenant with nothing
// active and a store without the seam are left alone.
func TestStepStopsATenantSuspendedInStopMode(t *testing.T) {
	running := func(runID, tenant string, mode domain.SuspensionMode, st domain.TaskState) RunState {
		return RunState{RunID: runID, DagID: "d" + runID, TenantID: tenant, State: domain.DagRunStateRunning, Suspension: mode,
			Tasks: linearTasks(), States: map[string]domain.TaskState{"a": st, "b": domain.TaskStateNone}}
	}
	store := &stoppingStore{fakeStore: newFakeStore(
		running("r1", "t1", domain.SuspensionStop, domain.TaskStateRunning),
		running("r2", "t1", domain.SuspensionStop, domain.TaskStateQueued),
		running("r3", "t2", domain.SuspensionDrain, domain.TaskStateRunning),
		running("r4", "t3", domain.SuspensionStop, domain.TaskStateSuccess),
	), failed: 2}

	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.stopped) != 1 || store.stopped[0] != "t1" {
		t.Fatalf("stopped = %v, want [t1] once", store.stopped)
	}

	// A failing stop is retried on the next tick, and does not fail this one.
	store.stopped, store.err = nil, errors.New("connection reset")
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatalf("a failed stop must not fail the tick: %v", err)
	}
	if len(store.stopped) != 1 {
		t.Fatalf("stopped = %v, want one attempt", store.stopped)
	}
}
