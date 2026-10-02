package scheduler

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/dexadata/dexaflow/internal/dispatch"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
)

// blockingInner holds every dispatch until release is closed, signaling on
// started each time a worker picks a request up. It lets a test fill the
// buffered dispatcher deterministically: one request in flight, the rest queued.
type blockingInner struct {
	started chan struct{}
	release chan struct{}
}

func (b *blockingInner) Dispatch(context.Context, string, string, string, domain.TaskSpec) (executor.Disposition, error) {
	b.started <- struct{}{}
	<-b.release
	return executor.Dispatched, nil
}

// fullBufferedDispatcher returns a BufferedDispatcher whose single worker is
// busy and whose single buffer slot is taken, so the next Dispatch hits
// ErrAtCapacity. The cleanup releases the worker and drains the pool.
func fullBufferedDispatcher(t *testing.T) *dispatch.BufferedDispatcher {
	t.Helper()
	inner := &blockingInner{started: make(chan struct{}, 4), release: make(chan struct{})}
	b := dispatch.NewBuffered(inner, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil,
		dispatch.BufferConfig{BufferSize: 1, Workers: 1})
	t.Cleanup(func() {
		close(inner.release)
		_ = b.Close()
	})
	ctx := context.Background()
	if _, err := b.Dispatch(ctx, "warm", "etl", "", domain.TaskSpec{TaskID: "in-flight"}); err != nil {
		t.Fatalf("priming the in-flight dispatch: %v", err)
	}
	<-inner.started
	if _, err := b.Dispatch(ctx, "warm", "etl", "", domain.TaskSpec{TaskID: "queued"}); err != nil {
		t.Fatalf("priming the buffer slot: %v", err)
	}
	return b
}

// A full dispatch buffer is the scheduler's own backpressure signal, not a
// failed dispatch (review item S2). The task must stay scheduled for a later
// tick with nothing written: no dispatch-attempt increment, no backoff, and
// never dispatch_failed, even when the task's attempt counter already sits one
// short of the budget.
func TestStepFullDispatchBufferIsNotADispatchFailure(t *testing.T) {
	store := newFakeStore(RunState{
		RunID: "r1", DagID: "etl", State: domain.DagRunStateRunning, Tasks: linearTasks(),
		States:           map[string]domain.TaskState{"a": domain.TaskStateScheduled, "b": domain.TaskStateNone},
		DispatchAttempts: map[string]int{"a": dispatchMaxAttempts - 1},
	})
	s := newScheduler(store)
	s.SetDispatcher(fullBufferedDispatcher(t))

	if err := s.Step(context.Background()); err != nil {
		t.Fatalf("a full dispatch buffer must not abort the step: %v", err)
	}
	if len(store.dispatchExhausted) != 0 {
		t.Errorf("a full buffer must never fail the task as dispatch_failed, got %v", store.dispatchExhausted)
	}
	if len(store.dispatchFailures) != 0 {
		t.Errorf("a full buffer must not count against the dispatch budget, got %v", store.dispatchFailures)
	}
	if len(store.dispatchBackpressure) != 0 {
		t.Errorf("a full buffer must not back the task off, got %v", store.dispatchBackpressure)
	}
	if len(store.transitions) != 0 {
		t.Errorf("a deferred dispatch must leave the task scheduled, got %v", store.transitions)
	}
}
