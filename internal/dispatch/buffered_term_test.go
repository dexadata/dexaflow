package dispatch_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/dispatch"
	"github.com/dexadata/dexaflow/internal/domain"
)

// TestBuffered_DropsDispatchesOfASupersededTerm: a worker that dequeues a
// dispatch after another replica took a newer lease term drops it before the
// pod create, and never reports it failed: the task stays queued for the new
// leader to re-place (ADR 0106, point 5). Dispatches made while the term is
// current go through.
func TestBuffered_DropsDispatchesOfASupersededTerm(t *testing.T) {
	inner := &recordingInner{}
	sink := &recordingSink{}
	d := dispatch.NewBuffered(inner, sink, discardLogger(), nil, dispatch.BufferConfig{BufferSize: 8, Workers: 1})
	var current atomic.Bool
	current.Store(true)
	var checks atomic.Int64
	d.SetTermCheck(func(context.Context) bool {
		checks.Add(1)
		return current.Load()
	})

	if _, err := d.Dispatch(context.Background(), "r1", "etl", "", domain.TaskSpec{TaskID: "before"}); err != nil {
		t.Fatal(err)
	}
	waitCalls(t, inner, 1)
	current.Store(false)
	for _, id := range []string{"after1", "after2"} {
		if _, err := d.Dispatch(context.Background(), "r1", "etl", "", domain.TaskSpec{TaskID: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if got := inner.seen(); len(got) != 1 || got[0] != "before" {
		t.Fatalf("inner dispatched %v, want only [before]", got)
	}
	if checks.Load() != 3 {
		t.Errorf("term checked %d times, want once per dequeued dispatch (3)", checks.Load())
	}
	if f := sink.snapshot(); len(f) != 0 {
		t.Errorf("dropped dispatches were reported failed: %v", f)
	}
}

func waitCalls(t *testing.T, inner *recordingInner, n int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && inner.callCount.Load() < n {
		time.Sleep(5 * time.Millisecond)
	}
	if inner.callCount.Load() < n {
		t.Fatalf("inner saw %d calls, want %d", inner.callCount.Load(), n)
	}
}
