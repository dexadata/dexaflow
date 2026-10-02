package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
)

// TestLaunchQueuedGuardsTheQueuedWrite: with buffered dispatch the worker can
// finish (fail the task, re-offer it, or see the agent report running) before
// the scheduler writes `queued`. The write is conditioned on the row still being
// the scheduled slot this tick planned (its next_dispatch_at as read), so it can
// never overwrite what the worker or the agent already recorded.
func TestLaunchQueuedGuardsTheQueuedWrite(t *testing.T) {
	slot := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	store := newFakeStore(RunState{
		RunID: "r1", DagID: "etl", State: domain.DagRunStateRunning, Tasks: linearTasks(),
		States:         map[string]domain.TaskState{"a": domain.TaskStateScheduled, "b": domain.TaskStateNone},
		NextDispatchAt: map[string]*time.Time{"a": &slot},
		Now:            slot.Add(time.Minute),
	})
	s := newScheduler(store)
	s.SetDispatcher(&fakeDispatcher{})

	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, ok := store.queuedExpect["a"]
	if !ok {
		t.Fatal("the queued write did not go through the guarded MarkQueued")
	}
	if got == nil || !got.Equal(slot) {
		t.Errorf("queued write conditioned on next_dispatch_at %v, want %v", got, slot)
	}
}

// TestLaunchQueuedSupersededWriteIsNotAnError: a guarded write that finds the
// row already moved on is the guard working, not a tick failure.
func TestLaunchQueuedSupersededWriteIsNotAnError(t *testing.T) {
	store := runWithScheduledRoot()
	store.queuedSuperseded = map[string]bool{"a": true}
	s := newScheduler(store)
	s.SetDispatcher(&fakeDispatcher{})

	if err := s.Step(context.Background()); err != nil {
		t.Fatalf("Step: %v", err)
	}
	if hasTransition(store.transitions, "a", domain.TaskStateQueued) {
		t.Error("a superseded queued write must not be recorded as a transition")
	}
}

// fakeAsyncStore is the AsyncDispatchStore the buffered worker's failure
// handler drives.
type fakeAsyncStore struct {
	attempts  int
	active    bool
	readErr   error
	requeued  []asyncRequeue
	failed    []string
	failNotes []string
}

type asyncRequeue struct {
	taskID  string
	counted bool
	nextAt  time.Time
}

func (f *fakeAsyncStore) DispatchAttempts(_ context.Context, _, _ string) (int, bool, error) {
	return f.attempts, f.active, f.readErr
}

func (f *fakeAsyncStore) RequeueDispatch(_ context.Context, _, taskID string, countAttempt bool, nextAt time.Time) (bool, error) {
	f.requeued = append(f.requeued, asyncRequeue{taskID, countAttempt, nextAt})
	return true, nil
}

func (f *fakeAsyncStore) MarkTaskDispatchFailed(_ context.Context, _, taskID, reason string) error {
	f.failed = append(f.failed, taskID)
	f.failNotes = append(f.failNotes, reason)
	return nil
}

// TestAsyncDispatchFailureMirrorsTheSyncPath: a buffered dispatch that fails in
// the worker is handled like a synchronous one. Cluster backpressure (quota
// 403, 429) is re-offered without spending the dispatch budget; any other error
// (a 5xx, a timeout, a rejection) is re-offered with a counted attempt and a
// growing backoff, and fails the task as dispatch_failed only once the budget
// is spent. Before, every such error failed the task at once.
func TestAsyncDispatchFailureMirrorsTheSyncPath(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	cause := errors.New("creating pod: exceeded quota: compute")

	t.Run("backpressure", func(t *testing.T) {
		st := &fakeAsyncStore{attempts: 2, active: true}
		h := NewAsyncDispatchFailures(st, quietLogger())
		h.now = func() time.Time { return now }
		if err := h.HandleDispatchFailure(context.Background(), "r1", "a", executor.Backpressure, cause); err != nil {
			t.Fatal(err)
		}
		if len(st.failed) != 0 || len(st.requeued) != 1 {
			t.Fatalf("failed=%v requeued=%v, want one uncounted re-offer", st.failed, st.requeued)
		}
		if r := st.requeued[0]; r.counted || !r.nextAt.Equal(now.Add(dispatchBackoff(3))) {
			t.Errorf("re-offer = %+v, want uncounted at now+%v", r, dispatchBackoff(3))
		}
	})

	t.Run("transient error within budget", func(t *testing.T) {
		st := &fakeAsyncStore{attempts: 1, active: true}
		h := NewAsyncDispatchFailures(st, quietLogger())
		h.now = func() time.Time { return now }
		if err := h.HandleDispatchFailure(context.Background(), "r1", "a", executor.Rejected, errors.New("503 service unavailable")); err != nil {
			t.Fatal(err)
		}
		if len(st.failed) != 0 || len(st.requeued) != 1 {
			t.Fatalf("failed=%v requeued=%v, want one counted re-offer", st.failed, st.requeued)
		}
		if r := st.requeued[0]; !r.counted || !r.nextAt.Equal(now.Add(dispatchBackoff(2))) {
			t.Errorf("re-offer = %+v, want counted at now+%v", r, dispatchBackoff(2))
		}
	})

	t.Run("budget spent", func(t *testing.T) {
		st := &fakeAsyncStore{attempts: dispatchMaxAttempts - 1, active: true}
		h := NewAsyncDispatchFailures(st, quietLogger())
		if err := h.HandleDispatchFailure(context.Background(), "r1", "a", executor.Rejected, errors.New("image not found")); err != nil {
			t.Fatal(err)
		}
		if len(st.requeued) != 0 || len(st.failed) != 1 {
			t.Fatalf("failed=%v requeued=%v, want the task failed", st.failed, st.requeued)
		}
		if !strings.HasPrefix(st.failNotes[0], "dispatch_failed after") {
			t.Errorf("fail reason = %q, want the dispatch_failed reason", st.failNotes[0])
		}
	})

	t.Run("row already moved on", func(t *testing.T) {
		st := &fakeAsyncStore{active: false}
		h := NewAsyncDispatchFailures(st, quietLogger())
		if err := h.HandleDispatchFailure(context.Background(), "r1", "a", executor.Rejected, errors.New("boom")); err != nil {
			t.Fatal(err)
		}
		if len(st.requeued) != 0 || len(st.failed) != 0 {
			t.Errorf("a TI no longer scheduled or queued must be left alone, got failed=%v requeued=%v", st.failed, st.requeued)
		}
	})
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
