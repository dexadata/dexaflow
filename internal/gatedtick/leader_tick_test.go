package gatedtick

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// A leader-gated tick that is still running when this replica steps down must
// stop, instead of creating and deleting warm pods (or staging volumes) next to
// the new leader's own loop (#1602). RunLeaderTick hands the cycle a context
// that ends once leading() reports false.
func TestRunLeaderTickStopsOnStepDown(t *testing.T) {
	var leading atomic.Bool
	leading.Store(true)
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		RunLeaderTick(context.Background(), "test", time.Hour, 5*time.Millisecond, leading.Load, discardLog(),
			func(ctx context.Context) error {
				close(started)
				<-ctx.Done()
				done <- ctx.Err()
				return ctx.Err()
			})
	}()
	<-started
	leading.Store(false)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cycle context ended with %v, want Canceled on step-down", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cycle still running 2s after the step-down")
	}
}

// The cycle has a deadline even while leading, so one hung database or
// apiserver call cannot hold the tick past its budget.
func TestRunLeaderTickHasDeadline(t *testing.T) {
	var leading atomic.Bool
	leading.Store(true)
	var got error
	RunLeaderTick(context.Background(), "test", 20*time.Millisecond, time.Hour, leading.Load, discardLog(),
		func(ctx context.Context) error {
			<-ctx.Done()
			got = ctx.Err()
			return got
		})
	if !errors.Is(got, context.DeadlineExceeded) {
		t.Fatalf("cycle context ended with %v, want DeadlineExceeded", got)
	}
}

// A cycle that finishes while leading is not disturbed.
func TestRunLeaderTickRunsCycleWhileLeading(t *testing.T) {
	ran := false
	RunLeaderTick(context.Background(), "test", time.Second, time.Millisecond, func() bool { return true }, discardLog(),
		func(ctx context.Context) error {
			ran = ctx.Err() == nil
			return nil
		})
	if !ran {
		t.Fatal("cycle did not run with a live context")
	}
}
