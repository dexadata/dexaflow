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

	go runGatedTickerOrKick(ctx, "test", ticks, kicks, leading.Load, discardLog(), func() { ran <- struct{}{} })

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

// A follower ignores kicks exactly as it ignores ticks: only the leader mutates
// the warm fleet.
func TestRunGatedTickerOrKickSkipsKickWhileFollower(t *testing.T) {
	ticks := make(chan time.Time)
	kicks := make(chan struct{})
	ran := make(chan struct{}, 8)
	var leading atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go runGatedTickerOrKick(ctx, "test", ticks, kicks, leading.Load, discardLog(), func() { ran <- struct{}{} })

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
