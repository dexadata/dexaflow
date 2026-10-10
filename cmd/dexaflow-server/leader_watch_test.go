package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeChecker is a leadershipChecker with a fixed result, counting calls.
type fakeChecker struct {
	mu    sync.Mutex
	held  bool
	err   error
	calls int
}

func (f *fakeChecker) HoldsLock(context.Context) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.held, f.err
}

func (f *fakeChecker) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// TestWatchLeadershipStepsDownOnLostLock: a definitive "not held" cancels the
// run immediately (the split-brain guard).
func TestWatchLeadershipStepsDownOnLostLock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	chk := &fakeChecker{held: false}
	done := make(chan struct{})
	go func() { watchLeadership(ctx, chk, time.Millisecond, cancel, discardLog(), nil); close(done) }()

	select {
	case <-ctx.Done(): // stepped down
	case <-time.After(2 * time.Second):
		t.Fatal("did not step down when the lock was lost")
	}
	<-done
}

// TestWatchLeadershipToleratesTransientErrors: a single check error does not
// churn leadership; it steps down only after maxLeaderCheckFailures.
func TestWatchLeadershipToleratesTransientErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	chk := &fakeChecker{err: errors.New("connection blip")}
	start := time.Now()
	done := make(chan struct{})
	go func() { watchLeadership(ctx, chk, 10*time.Millisecond, cancel, discardLog(), nil); close(done) }()

	select {
	case <-ctx.Done():
		if elapsed := time.Since(start); elapsed < 25*time.Millisecond {
			t.Errorf("stepped down after %v; should tolerate %d errors first", elapsed, maxLeaderCheckFailures)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not step down after repeated check errors")
	}
	<-done
	if got := chk.callCount(); got < maxLeaderCheckFailures {
		t.Errorf("expected >= %d checks before stepping down, got %d", maxLeaderCheckFailures, got)
	}
}

// TestWatchLeadershipFiresOnStepDownWithReason: when leadership is lost the
// callback fires EXACTLY ONCE with the structured reason, BEFORE cancel().
// Operators alert on the per-reason counter, so a miscount here would mask
// leader-churn alerts (#311).
func TestWatchLeadershipFiresOnStepDownWithReason(t *testing.T) {
	cases := []struct {
		name       string
		chk        *fakeChecker
		wantReason string
		interval   time.Duration
	}{
		{name: "lock_released", chk: &fakeChecker{held: false}, wantReason: "lock_released", interval: time.Millisecond},
		{name: "check_timeout", chk: &fakeChecker{err: errors.New("blip")}, wantReason: "check_timeout", interval: 5 * time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var (
				mu              sync.Mutex
				reasons         []string
				cancelledBefore bool
			)
			tracker := func(reason string) {
				mu.Lock()
				reasons = append(reasons, reason)
				// Contract: onStepDown fires BEFORE cancel() — the scheduler
				// must have time to flip its steppingDown flag before the
				// reapers see ctx-canceled. Capture ctx.Err() inside the
				// callback to assert that ordering.
				cancelledBefore = ctx.Err() != nil
				mu.Unlock()
			}
			done := make(chan struct{})
			go func() {
				watchLeadership(ctx, tc.chk, tc.interval, cancel, discardLog(), tracker)
				close(done)
			}()
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
				t.Fatal("did not step down")
			}
			<-done
			mu.Lock()
			defer mu.Unlock()
			if len(reasons) != 1 || reasons[0] != tc.wantReason {
				t.Errorf("reasons = %v, want exactly [%q]", reasons, tc.wantReason)
			}
			if cancelledBefore {
				t.Error("onStepDown fired AFTER cancel(); must fire before so the scheduler tags the cancel-fanout as expected (#311)")
			}
		})
	}
}

// TestWatchLeadershipStaysWhileHolding: while the lock is held, the watchdog
// never cancels the run.
func TestWatchLeadershipStaysWhileHolding(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	chk := &fakeChecker{held: true}
	done := make(chan struct{})
	go func() { watchLeadership(ctx, chk, time.Millisecond, cancel, discardLog(), nil); close(done) }()

	time.Sleep(40 * time.Millisecond)
	select {
	case <-ctx.Done():
		t.Fatal("must not step down while still holding the lock")
	default:
	}
	cancel() // simulate shutdown; the watchdog should return
	<-done
}
