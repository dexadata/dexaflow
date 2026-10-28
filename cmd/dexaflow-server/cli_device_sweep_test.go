package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

type fakeDeviceSweeper struct {
	calls atomic.Int64
	err   error
}

func (f *fakeDeviceSweeper) DeleteExpiredCLIDeviceAuthorizations(context.Context) (int64, error) {
	f.calls.Add(1)
	return 1, f.err
}

// Each tick sweeps the expired device sign-ins, a failed sweep is retried at
// the next tick, and the loop ends with its context.
func TestRunCLIDeviceSweepSweepsEveryTick(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ticks := make(chan time.Time)
	sweeper := &fakeDeviceSweeper{err: errors.New("database down")}
	done := make(chan struct{})
	go func() {
		runCLIDeviceSweep(ctx, ticks, sweeper, slog.New(slog.NewTextHandler(io.Discard, nil)))
		close(done)
	}()
	ticks <- time.Now()
	ticks <- time.Now()
	ticks <- time.Now()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the sweep loop did not end with its context")
	}
	if got := sweeper.calls.Load(); got != 3 {
		t.Fatalf("sweeps = %d after 3 ticks, want 3", got)
	}
}
