package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/config"
)

// A change signal (a warm worker lost or claimed) runs the reconcile at once on
// the leader, without waiting for the tick.
func TestRunGatedTickerOrKickRunsOnKickWhileLeading(t *testing.T) {
	ticks := make(chan time.Time)
	kicks := make(chan struct{})
	ran := make(chan struct{}, 8)
	var leading atomic.Bool
	leading.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go runGatedTickerOrKick(ctx, "test", ticks, kicks, 0, leading.Load, discardLog(), func() { ran <- struct{}{} })

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
func TestRunGatedTickerOrKickRateLimitsKicks(t *testing.T) {
	ticks := make(chan time.Time)
	kicks := make(chan struct{}, 1)
	ran := make(chan time.Time, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const gap = 300 * time.Millisecond

	go runGatedTickerOrKick(ctx, "test", ticks, kicks, gap, nil, discardLog(), func() { ran <- time.Now() })

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
func TestRunGatedTickerOrKickSkipsKickWhileFollower(t *testing.T) {
	ticks := make(chan time.Time)
	kicks := make(chan struct{})
	ran := make(chan struct{}, 8)
	var leading atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go runGatedTickerOrKick(ctx, "test", ticks, kicks, 0, leading.Load, discardLog(), func() { ran <- struct{}{} })

	kicks <- struct{}{}
	kicks <- struct{}{}
	select {
	case <-ran:
		t.Error("a follower ran the cycle on a kick")
	default:
	}
}

// TestWarmPoolEventRefillDefaultAndEnv locks the gate: off by default (today's
// polling refill), reachable from both env prefixes.
func TestWarmPoolEventRefillDefaultAndEnv(t *testing.T) {
	c, err := config.LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if c.Execution.WarmPoolEventRefill {
		t.Error("execution.warm_pool_event_refill default = true, want false")
	}
	for _, name := range []string{"DEXAFLOW_EXECUTION_WARM_POOL_EVENT_REFILL", "LEOFLOW_EXECUTION_WARM_POOL_EVENT_REFILL"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "true")
			c, err := config.LoadServer("", nil)
			if err != nil {
				t.Fatalf("LoadServer: %v", err)
			}
			if !c.Execution.WarmPoolEventRefill {
				t.Errorf("%s=true did not bind", name)
			}
		})
	}
}
