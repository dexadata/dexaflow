package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// runGatedTicker must run its cycle only while leading() is true, so at
// replicaCount>1 a follower's reconciler/GC does not sweep and delete pods the
// leader owns.
//
// These two tests hold `leading` STABLE for the whole run rather than toggling it
// mid-flight. Toggling it between two ticks (the previous form) raced: after the
// follower tick's send rendezvoused, the main goroutine's Store(true) ran
// concurrently with the ticker goroutine's gate() evaluation for that same tick,
// so the follower tick could observe `true` and wrongly run the cycle — a real
// flake reproducible under load (`-count`). With a stable gate the outcome is
// fully determined by the send/receive ordering, no sleeps and no race.

// A follower (leading=false) never runs the cycle, no matter how many ticks.
func TestRunGatedTickerSkipsWhileFollower(t *testing.T) {
	ticks := make(chan time.Time)
	ran := make(chan struct{}, 8)
	var leading atomic.Bool // stays false
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go runGatedTicker(ctx, "test", ticks, leading.Load, discardLog(), func() {
		ran <- struct{}{}
	})

	// Two ticks on an unbuffered channel: the second send completes only after the
	// ticker looped back to receive it, which means the FIRST tick was already
	// fully processed (gate evaluated, cycle skipped). So once the second send
	// returns, the first follower tick is guaranteed done and contributed nothing.
	ticks <- time.Now()
	ticks <- time.Now()

	select {
	case <-ran:
		t.Error("a follower tick ran the cycle; it must be skipped when not leading")
	default:
	}
}

// A leader (leading=true) runs the cycle on its tick.
func TestRunGatedTickerRunsWhileLeading(t *testing.T) {
	ticks := make(chan time.Time)
	ran := make(chan struct{}, 8)
	var leading atomic.Bool
	leading.Store(true) // stays true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go runGatedTicker(ctx, "test", ticks, leading.Load, discardLog(), func() {
		ran <- struct{}{}
	})

	ticks <- time.Now()
	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("leader tick did not run the cycle")
	}
}

// A nil gate means "always run" — single-replica / no election.
func TestRunGatedTickerNilGateAlwaysRuns(t *testing.T) {
	ticks := make(chan time.Time)
	ran := make(chan struct{}, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go runGatedTicker(ctx, "test", ticks, nil, discardLog(), func() { ran <- struct{}{} })

	ticks <- time.Now()
	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("a nil gate should always run the cycle")
	}
}
