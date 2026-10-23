package scheduler

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

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
	go func() { watchLeadership(ctx, chk, time.Millisecond, cancel, discardLogger(), nil); close(done) }()

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
	go func() { watchLeadership(ctx, chk, 10*time.Millisecond, cancel, discardLogger(), nil); close(done) }()

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
				// Contract: onStepDown fires BEFORE cancel(): the scheduler
				// must have time to flip its steppingDown flag before the
				// reapers see ctx-canceled. Capture ctx.Err() inside the
				// callback to assert that ordering.
				cancelledBefore = ctx.Err() != nil
				mu.Unlock()
			}
			done := make(chan struct{})
			go func() {
				watchLeadership(ctx, tc.chk, tc.interval, cancel, discardLogger(), tracker)
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
	go func() { watchLeadership(ctx, chk, time.Millisecond, cancel, discardLogger(), nil); close(done) }()

	time.Sleep(40 * time.Millisecond)
	select {
	case <-ctx.Done():
		t.Fatal("must not step down while still holding the lock")
	default:
	}
	cancel() // simulate shutdown; the watchdog should return
	<-done
}

// hangingLock is a leader lock whose every query blocks until its context is
// done, the way a query does on a database session that is black-holed (the
// host vanished behind a failover or a partition, with no TCP reset).
type hangingLock struct {
	mu    sync.Mutex
	calls int
}

func (h *hangingLock) wait(ctx context.Context) error {
	h.mu.Lock()
	h.calls++
	h.mu.Unlock()
	<-ctx.Done()
	return ctx.Err()
}

func (h *hangingLock) HoldsLock(ctx context.Context) (bool, error) { return false, h.wait(ctx) }

func (h *hangingLock) TryAcquire(ctx context.Context) (bool, error) { return false, h.wait(ctx) }

// TestWatchLeadershipStepsDownWhenChecksHang: a check that never returns is a
// failed check, not a pass. Each check runs under a deadline, so a leader whose
// session is black-holed steps down after maxLeaderCheckFailures intervals
// instead of leading on a lock it can no longer confirm (#1599, R-12).
func TestWatchLeadershipStepsDownWhenChecksHang(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	chk := &hangingLock{}
	var (
		mu     sync.Mutex
		reason string
	)
	done := make(chan struct{})
	go func() {
		watchLeadership(ctx, chk, 10*time.Millisecond, cancel, discardLogger(), func(r string) {
			mu.Lock()
			reason = r
			mu.Unlock()
		})
		close(done)
	}()

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("leader never stepped down while every leadership check hung")
	}
	<-done
	mu.Lock()
	defer mu.Unlock()
	if reason != "check_timeout" {
		t.Errorf("step-down reason = %q, want check_timeout", reason)
	}
	if chk.calls < maxLeaderCheckFailures {
		t.Errorf("stepped down after %d checks, want %d", chk.calls, maxLeaderCheckFailures)
	}
}

// TestTryLeadershipGivesUpOnAHungSession: a follower's campaign attempt is
// bounded too, so a black-holed session cannot stall the campaign loop
// forever (#1599, R-12).
func TestTryLeadershipGivesUpOnAHungSession(t *testing.T) {
	start := time.Now()
	ok, err := tryLeadership(context.Background(), &hangingLock{}, 20*time.Millisecond)
	if err == nil || ok {
		t.Fatalf("tryLeadership on a hung session = %v, %v; want an error", ok, err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error %v does not report the deadline", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("tryLeadership took %s with a 20ms deadline", elapsed)
	}
}
