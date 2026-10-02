package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// TestMaintenanceCycleReconcilesBeforeReaping: one maintenance cycle runs the
// reconciler's sweep and THEN the reapers, in that order, every time. The
// ordering is the structural fix for the drill race — a reaper that judged a
// task pod "gone" before the reconciler had recovered the pod's durable outcome
// — and it holds by construction rather than by two independent timers lining
// up.
func TestMaintenanceCycleReconcilesBeforeReaping(t *testing.T) {
	var order []string
	reconcile := func(context.Context) error { order = append(order, "reconcile"); return nil }
	reap := func(context.Context) error { order = append(order, "reap"); return nil }

	for i := 0; i < 3; i++ {
		maintenanceCycle(context.Background(), time.Second, reconcile, reap, discardLog())
	}
	want := []string{"reconcile", "reap", "reconcile", "reap", "reconcile", "reap"}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}

// TestMaintenanceCycleReapsWhenReconcileFails: a reconcile error (the apiserver
// LIST failed) is logged, not fatal to the cycle — the reapers still run. They
// are independent backstops with their own fail-closed liveness reads, and the
// reaper's settling gate, not this loop, decides whether a leader that has never
// completed a sweep may act.
func TestMaintenanceCycleReapsWhenReconcileFails(t *testing.T) {
	reaped := false
	reconcile := func(context.Context) error { return errors.New("apiserver down") }
	reap := func(context.Context) error { reaped = true; return nil }

	maintenanceCycle(context.Background(), time.Second, reconcile, reap, discardLog())
	if !reaped {
		t.Fatal("the reapers must run even when the reconcile sweep failed")
	}
}

// TestMaintenanceLoopHonorsLeaderGate: the maintenance cycle runs on the
// leader-gated ticker, so a follower tick neither reconciles nor reaps —
// reaping writes state and must stay single-writer across the fleet — and a
// leader tick does both. Same stable-gate construction as the gated-ticker
// tests: two sends on an unbuffered channel prove the first tick was fully
// processed.
func TestMaintenanceLoopHonorsLeaderGate(t *testing.T) {
	newLoop := func(leading *atomic.Bool) (ticks chan time.Time, ran chan string, stop func()) {
		ticks = make(chan time.Time)
		ran = make(chan string, 8)
		ctx, cancel := context.WithCancel(context.Background())
		go runGatedTicker(ctx, "maintenance", ticks, leading.Load, discardLog(), func() {
			maintenanceCycle(ctx, time.Second,
				func(context.Context) error { ran <- "reconcile"; return nil },
				func(context.Context) error { ran <- "reap"; return nil },
				discardLog())
		})
		return ticks, ran, cancel
	}

	t.Run("follower", func(t *testing.T) {
		var leading atomic.Bool // stays false
		ticks, ran, stop := newLoop(&leading)
		defer stop()
		ticks <- time.Now()
		ticks <- time.Now()
		select {
		case got := <-ran:
			t.Errorf("a follower tick ran %q; the maintenance cycle must be leader-only", got)
		default:
		}
	})
	t.Run("leader", func(t *testing.T) {
		var leading atomic.Bool
		leading.Store(true)
		ticks, ran, stop := newLoop(&leading)
		defer stop()
		ticks <- time.Now()
		var got []string
		for len(got) < 2 {
			select {
			case s := <-ran:
				got = append(got, s)
			case <-time.After(2 * time.Second):
				t.Fatalf("leader tick did not run the whole cycle, got %v", got)
			}
		}
		if got[0] != "reconcile" || got[1] != "reap" {
			t.Errorf("cycle order = %v, want [reconcile reap]", got)
		}
	})
}

// TestMaintenanceCycleBoundsEachPhase: each phase of a maintenance cycle runs
// under its own time budget. The reapers used to run inside the scheduler tick,
// which is bounded by the step timeout; moving them into this loop must not
// lose that bound. The two phases share one goroutine, so an unbounded reconcile
// (a LIST that hangs on a slow apiserver) would starve the reap indefinitely
// and an unbounded reap (one live LIST per running task instance, lock-blocked
// UPDATEs) would starve the very reconcile it depends on. Each phase gets a
// fresh budget: a reconcile that spends all of its own must still leave the
// reap running with a LIVE context — the reaper's destructive gate reads
// ctx.Err() and a canceled context would silently disable reaping.
func TestMaintenanceCycleBoundsEachPhase(t *testing.T) {
	const budget = 50 * time.Millisecond
	block := func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }

	t.Run("hung reconcile still leaves the reap a live context", func(t *testing.T) {
		var reapCtxErr error
		reaped := false
		reap := func(ctx context.Context) error { reaped = true; reapCtxErr = ctx.Err(); return nil }
		start := time.Now()
		maintenanceCycle(context.Background(), budget, block, reap, discardLog())
		if d := time.Since(start); d > 10*budget {
			t.Fatalf("cycle took %v with a hung reconcile; the phase budget (%v) must bound it", d, budget)
		}
		if !reaped {
			t.Fatal("the reap must run after a reconcile that exhausted its budget")
		}
		if reapCtxErr != nil {
			t.Fatalf("the reap started with a dead context (%v); each phase needs its own budget", reapCtxErr)
		}
	})
	t.Run("hung reap is bounded too", func(t *testing.T) {
		start := time.Now()
		maintenanceCycle(context.Background(), budget, func(context.Context) error { return nil }, block, discardLog())
		if d := time.Since(start); d > 10*budget {
			t.Fatalf("cycle took %v with a hung reap; the phase budget (%v) must bound it", d, budget)
		}
	})
	t.Run("a phase within budget sees no deadline", func(t *testing.T) {
		var gotErr error
		maintenanceCycle(context.Background(), time.Second,
			func(ctx context.Context) error { gotErr = ctx.Err(); return nil },
			func(context.Context) error { return nil }, discardLog())
		if gotErr != nil {
			t.Fatalf("reconcile saw %v on a fresh budget", gotErr)
		}
	})
}
