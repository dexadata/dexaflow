package main

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/dexadata/dexaflow/internal/agentrpc"
)

// fakeRedispatchStore records RequeueForRedispatch calls so the reason-gating
// decision (ADR 0058 N1d-c, H2) can be asserted without a database.
type fakeRedispatchStore struct {
	mu    sync.Mutex
	calls []redispatchCall
	err   error
}

type redispatchCall struct {
	runID     string
	taskID    string
	tryNumber int
}

func (f *fakeRedispatchStore) RequeueForRedispatch(_ context.Context, runID, taskID string, tryNumber int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, redispatchCall{runID, taskID, tryNumber})
	return f.err
}

func (f *fakeRedispatchStore) recorded() []redispatchCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]redispatchCall(nil), f.calls...)
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestReclaimShouldRequeueGating pins the pure decision: only the reasons where
// the worker demonstrably will NOT run the attempt (WorkerGone, Refused) may be
// fast-re-placed; LeaseExpired must not, because a slow-but-alive worker will
// still ack-then-run and re-placing it could double-run.
func TestReclaimShouldRequeueGating(t *testing.T) {
	cases := []struct {
		reason agentrpc.ReclaimReason
		want   bool
	}{
		{agentrpc.ReclaimWorkerGone, true},
		{agentrpc.ReclaimRefused, true},
		{agentrpc.ReclaimLeaseExpired, false},
	}
	for _, c := range cases {
		if got := reclaimShouldRequeue(c.reason); got != c.want {
			t.Errorf("reclaimShouldRequeue(%v) = %v, want %v", c.reason, got, c.want)
		}
	}
}

// TestHandleReclaimReplacesOnlyDoubleRunSafeReasons drives handleReclaim end to
// end against the fake store: WorkerGone and Refused re-place the exact attempt;
// LeaseExpired does not call the store at all.
func TestHandleReclaimReplacesOnlyDoubleRunSafeReasons(t *testing.T) {
	t.Run("WorkerGone requeues", func(t *testing.T) {
		store := &fakeRedispatchStore{}
		handleReclaim(context.Background(), store, discardLogger(), agentrpc.ReclaimEvent{
			Reason: agentrpc.ReclaimWorkerGone, RunID: "run-1", TaskID: "extract", TryNumber: 2,
		})
		got := store.recorded()
		if len(got) != 1 {
			t.Fatalf("WorkerGone: RequeueForRedispatch calls = %d, want 1", len(got))
		}
		if got[0] != (redispatchCall{"run-1", "extract", 2}) {
			t.Fatalf("WorkerGone: requeued %+v, want run-1/extract/2", got[0])
		}
	})

	t.Run("Refused requeues", func(t *testing.T) {
		store := &fakeRedispatchStore{}
		handleReclaim(context.Background(), store, discardLogger(), agentrpc.ReclaimEvent{
			Reason: agentrpc.ReclaimRefused, RunID: "run-2", TaskID: "load", TryNumber: 1,
		})
		if got := store.recorded(); len(got) != 1 || got[0] != (redispatchCall{"run-2", "load", 1}) {
			t.Fatalf("Refused: requeued %+v, want exactly run-2/load/1", got)
		}
	})

	t.Run("LeaseExpired does NOT requeue", func(t *testing.T) {
		store := &fakeRedispatchStore{}
		handleReclaim(context.Background(), store, discardLogger(), agentrpc.ReclaimEvent{
			Reason: agentrpc.ReclaimLeaseExpired, RunID: "run-3", TaskID: "transform", TryNumber: 5,
		})
		if got := store.recorded(); len(got) != 0 {
			t.Fatalf("LeaseExpired: RequeueForRedispatch calls = %d, want 0 (recovered by the reaper, not fast-re-placed)", len(got))
		}
	})
}

// TestHandleReclaimBestEffortOnStoreError: a store error is swallowed (logged),
// never propagated — the registry's emit path must not crash on a DB blip.
func TestHandleReclaimBestEffortOnStoreError(t *testing.T) {
	store := &fakeRedispatchStore{err: context.DeadlineExceeded}
	// Must not panic; there is no error to return, so the assertion is simply
	// that the call completes and the store was still attempted.
	handleReclaim(context.Background(), store, discardLogger(), agentrpc.ReclaimEvent{
		Reason: agentrpc.ReclaimWorkerGone, RunID: "run-9", TaskID: "x", TryNumber: 1,
	})
	if len(store.recorded()) != 1 {
		t.Fatal("expected the re-placement to be attempted even though it errored")
	}
}
