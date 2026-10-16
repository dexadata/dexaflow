package scheduler

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
)

// unavailableRun is a run whose task "a" is due for dispatch at now.
func unavailableRun(now time.Time, attempts int) RunState {
	return RunState{
		RunID: "r1", DagID: "etl", State: domain.DagRunStateRunning, Tasks: linearTasks(), Now: now,
		States:           map[string]domain.TaskState{"a": domain.TaskStateScheduled, "b": domain.TaskStateNone},
		DispatchAttempts: map[string]int{"a": attempts},
	}
}

var errAPIDown = errors.New("creating pod for task a: the server is currently unable to handle the request")

// TestStepUnavailableAPIIsOffTheAttemptBudget: an unavailable Kubernetes API
// (#1606) re-offers the task without counting an attempt, even with the
// attempt counter one short of the budget, where a Rejected failure fails the
// task as dispatch_failed (TestStepPermanentErrorStillExhausts).
func TestStepUnavailableAPIIsOffTheAttemptBudget(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	store := newFakeStore(unavailableRun(now, dispatchMaxAttempts-1))
	s := newScheduler(store)
	s.SetDispatcher(&fakeDispatcher{disp: executor.Unavailable, err: errAPIDown})

	if err := s.Step(context.Background()); err != nil {
		t.Fatalf("a dispatch failure must not abort the step: %v", err)
	}
	if len(store.dispatchExhausted) != 0 {
		t.Errorf("an unavailable API inside its window must not fail the task, got %v", store.dispatchExhausted)
	}
	if len(store.dispatchFailures) != 0 {
		t.Errorf("an unavailable API must not take the attempt-counting path, got %v", store.dispatchFailures)
	}
	if len(store.dispatchUnavailable) != 1 || store.dispatchUnavailable[0].taskID != "a" {
		t.Errorf("the task should be re-offered on the unavailable path, got %v", store.dispatchUnavailable)
	}
	if len(store.dispatchBackpressure) != 0 {
		t.Errorf("an unavailable API is not cluster backpressure (unbounded), got %v", store.dispatchBackpressure)
	}
}

// TestStepUnavailableAPIWindow drives one task through an outage tick by tick,
// with the clock moving to each next_dispatch_at the store wrote. An outage
// shorter than dispatchUnavailableWindow ends with the task dispatched; one
// that lasts the window fails it as dispatch_failed, with the outage in the
// reason.
func TestStepUnavailableAPIWindow(t *testing.T) {
	start := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name       string
		outage     time.Duration
		wantFailed bool
	}{
		{"shorter_than_window", dispatchUnavailableWindow - 2*dispatchUnavailableMaxDelay, false},
		{"longer_than_window", dispatchUnavailableWindow + time.Hour, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore(unavailableRun(start, 0))
			d := &fakeDispatcher{disp: executor.Unavailable, err: errAPIDown}
			s := newScheduler(store)
			s.SetDispatcher(d)

			now := start
			holds := 0
			for tick := 0; tick < 100; tick++ {
				if now.Sub(start) >= tc.outage {
					d.disp, d.err = executor.Dispatched, nil // the API is back
				}
				store.runs[0].Now = now
				if err := s.Step(context.Background()); err != nil {
					t.Fatalf("tick %d: %v", tick, err)
				}
				if len(store.dispatchExhausted) > 0 || hasTransition(store.transitions, "a", domain.TaskStateQueued) {
					break
				}
				holds++
				// Every re-offer holds the task: move the clock to the hold the
				// fake computed exactly as the SQL does.
				since := store.unavailableSince["a"]
				now = now.Add(min(max(now.Sub(since), dispatchBackoffBase), dispatchUnavailableMaxDelay))
			}
			if len(store.dispatchFailures) != 0 {
				t.Errorf("no unavailable failure may count a dispatch attempt, got %d", len(store.dispatchFailures))
			}
			failed := len(store.dispatchExhausted) == 1
			if failed != tc.wantFailed {
				t.Fatalf("dispatch_failed = %v after %s of outage (%d holds), want %v",
					failed, now.Sub(start), holds, tc.wantFailed)
			}
			if tc.wantFailed {
				if outage := now.Sub(start); outage < dispatchUnavailableWindow || outage > dispatchUnavailableWindow+dispatchUnavailableMaxDelay {
					t.Errorf("the task failed after %s, want within one hold past the %s window", outage, dispatchUnavailableWindow)
				}
				if hasTransition(store.transitions, "a", domain.TaskStateQueued) {
					t.Error("a task failed as dispatch_failed must not also be queued")
				}
				return
			}
			if !hasTransition(store.transitions, "a", domain.TaskStateQueued) {
				t.Errorf("the task must be dispatched once the API is back, transitions %v", store.transitions)
			}
			// The holds double from the base: an outage of ~13 minutes is ridden
			// out in a couple of dozen re-offers, not one every few seconds.
			if holds > 20 {
				t.Errorf("the task was re-offered %d times in a %s outage, want the hold to grow", holds, tc.outage)
			}
		})
	}
}

// TestAsyncDispatchUnavailableWindow: a buffered dispatch worker applies the
// same window as the synchronous path.
func TestAsyncDispatchUnavailableWindow(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

	t.Run("inside_the_window", func(t *testing.T) {
		st := &fakeAsyncStore{attempts: dispatchMaxAttempts - 1, active: true, unavailableSince: now.Add(-dispatchUnavailableWindow + time.Second)}
		h := NewAsyncDispatchFailures(st, nil)
		h.now = func() time.Time { return now }
		if err := h.HandleDispatchFailure(context.Background(), "r1", "a", executor.Unavailable, errAPIDown); err != nil {
			t.Fatal(err)
		}
		if len(st.failed) != 0 || len(st.requeued) != 0 {
			t.Errorf("inside the window the task is held off budget: failed=%v counted requeues=%v", st.failed, st.requeued)
		}
		if len(st.unavailable) != 1 || !st.unavailable[0].nextAt.Equal(now.Add(dispatchUnavailableMaxDelay)) {
			t.Errorf("unavailable re-offers = %+v, want one held for %s", st.unavailable, dispatchUnavailableMaxDelay)
		}
	})

	t.Run("past_the_window", func(t *testing.T) {
		st := &fakeAsyncStore{active: true, unavailableSince: now.Add(-dispatchUnavailableWindow)}
		h := NewAsyncDispatchFailures(st, nil)
		h.now = func() time.Time { return now }
		if err := h.HandleDispatchFailure(context.Background(), "r1", "a", executor.Unavailable, errAPIDown); err != nil {
			t.Fatal(err)
		}
		if len(st.failed) != 1 {
			t.Fatalf("past the window the task fails as dispatch_failed, failed=%v", st.failed)
		}
		if !strings.HasPrefix(st.failNotes[0], "dispatch_failed: Kubernetes API unavailable for 15m0s") {
			t.Errorf("reason = %q, want the outage length", st.failNotes[0])
		}
	})

	t.Run("moved_on", func(t *testing.T) {
		st := &fakeAsyncStore{active: false}
		h := NewAsyncDispatchFailures(st, nil)
		if err := h.HandleDispatchFailure(context.Background(), "r1", "a", executor.Unavailable, errAPIDown); err != nil {
			t.Fatal(err)
		}
		if len(st.failed)+len(st.unavailable)+len(st.requeued) != 0 {
			t.Errorf("a task that moved on is left alone: %+v", st)
		}
	})
}
