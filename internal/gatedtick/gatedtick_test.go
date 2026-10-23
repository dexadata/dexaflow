package gatedtick

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// Run must run its cycle only while leading() is true, so at replicaCount>1 a
// follower's reconciler/GC does not sweep and delete pods the leader owns.
//
// These two tests hold `leading` STABLE for the whole run rather than toggling it
// mid-flight. Toggling it between two ticks (the previous form) raced: after the
// follower tick's send rendezvoused, the main goroutine's Store(true) ran
// concurrently with the ticker goroutine's gate() evaluation for that same tick,
// so the follower tick could observe `true` and wrongly run the cycle, a real
// flake reproducible under load (`-count`). With a stable gate the outcome is
// fully determined by the send/receive ordering, no sleeps and no race.

// A follower (leading=false) never runs the cycle, no matter how many ticks.
func TestRunSkipsWhileFollower(t *testing.T) {
	ticks := make(chan time.Time)
	ran := make(chan struct{}, 8)
	var leading atomic.Bool // stays false
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go Run(ctx, "test", ticks, leading.Load, discardLog(), func() {
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
func TestRunRunsWhileLeading(t *testing.T) {
	ticks := make(chan time.Time)
	ran := make(chan struct{}, 8)
	var leading atomic.Bool
	leading.Store(true) // stays true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go Run(ctx, "test", ticks, leading.Load, discardLog(), func() {
		ran <- struct{}{}
	})

	ticks <- time.Now()
	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("leader tick did not run the cycle")
	}
}

// A nil gate means "always run" (single-replica / no election).
func TestRunNilGateAlwaysRuns(t *testing.T) {
	ticks := make(chan time.Time)
	ran := make(chan struct{}, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go Run(ctx, "test", ticks, nil, discardLog(), func() { ran <- struct{}{} })

	ticks <- time.Now()
	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("a nil gate should always run the cycle")
	}
}

// A change signal (a warm worker lost or claimed) runs the reconcile at once on
// the leader, without waiting for the tick.
func TestRunOrKickRunsOnKickWhileLeading(t *testing.T) {
	ticks := make(chan time.Time)
	kicks := make(chan struct{})
	ran := make(chan struct{}, 8)
	var leading atomic.Bool
	leading.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go RunOrKick(ctx, "test", ticks, kicks, 0, leading.Load, discardLog(), func() { ran <- struct{}{} })

	kicks <- struct{}{}
	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("a kick did not run the cycle on the leader")
	}
	ticks <- time.Now()
	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("the periodic tick no longer runs the cycle")
	}
}

// Kicks arrive in bursts (a deploy drains many workers at once); after a run,
// the next kicked run waits out minKickGap so a burst costs one reconcile, not
// one per event. The periodic tick is not delayed by it.
func TestRunOrKickRateLimitsKicks(t *testing.T) {
	ticks := make(chan time.Time)
	kicks := make(chan struct{}, 1)
	ran := make(chan time.Time, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const gap = 300 * time.Millisecond

	go RunOrKick(ctx, "test", ticks, kicks, gap, nil, discardLog(), func() { ran <- time.Now() })

	kicks <- struct{}{}
	var first time.Time
	select {
	case first = <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("the first kick did not run the cycle")
	}
	kicks <- struct{}{}
	select {
	case second := <-ran:
		if d := second.Sub(first); d < gap-20*time.Millisecond {
			t.Errorf("second kicked run came %v after the first, want at least %v", d, gap)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the second kick never ran the cycle")
	}
}

// A follower ignores kicks exactly as it ignores ticks: only the leader mutates
// the warm fleet.
func TestRunOrKickSkipsKickWhileFollower(t *testing.T) {
	ticks := make(chan time.Time)
	kicks := make(chan struct{})
	ran := make(chan struct{}, 8)
	var leading atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go RunOrKick(ctx, "test", ticks, kicks, 0, leading.Load, discardLog(), func() { ran <- struct{}{} })

	kicks <- struct{}{}
	kicks <- struct{}{}
	select {
	case <-ran:
		t.Error("a follower ran the cycle on a kick")
	default:
	}
}
