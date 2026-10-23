// Package gatedtick runs the control plane's periodic background cycles: a
// ticker gated on scheduler leadership (the pod reconciler, the staging GC, the
// warm-pool reconcile, the retention janitor), the leader-bounded context one
// cycle of such a loop runs under, and the panic isolation every periodic loop
// shares.
package gatedtick

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"
)

// SafeCycle runs one iteration of a periodic background loop, recovering any
// panic so a single bad cycle logs and is retried next tick instead of crashing
// the goroutine (and, since an unrecovered panic in any goroutine aborts the
// program, the whole control plane). It changes no behavior on the happy path.
func SafeCycle(name string, logger *slog.Logger, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			logger.Error("recovered panic in background cycle", "cycle", name, "panic", r, "stack", string(debug.Stack()))
		}
	}()
	fn()
}

// Start runs fn every interval on a background goroutine, but only while
// leading() reports this instance holds leadership. It stops when ctx is
// done. The pod reconciler and staging GC mutate cluster state, so at
// replicaCount>1 they must run on the leader alone (ADR 0009/0031); a nil gate
// means "always run" (single-replica / no election).
func Start(ctx context.Context, name string, interval time.Duration, leading func() bool, logger *slog.Logger, fn func()) {
	t := time.NewTicker(interval)
	go func() {
		defer t.Stop()
		Run(ctx, name, t.C, leading, logger, fn)
	}()
}

// Run is the loop of Start with the tick source injected, so the leadership
// gate is testable without a real timer. Each cycle is panic-isolated by
// SafeCycle. A tick that arrives while not leading is dropped, not queued.
func Run(ctx context.Context, name string, ticks <-chan time.Time, leading func() bool, logger *slog.Logger, fn func()) {
	RunOrKick(ctx, name, ticks, nil, 0, leading, logger, fn)
}

// RunOrKick is Run that also runs the cycle on a signal from kicks
// (event-driven warm-pool refill), under the same leadership gate. A nil kicks
// channel never fires, which is exactly Run. A kicked run starts no sooner
// than minKickGap after the previous run, so a burst of events (a deploy
// draining many workers) costs one reconcile instead of one per event; kicks
// that arrive meanwhile coalesce into it. Ticks are never delayed.
func RunOrKick(ctx context.Context, name string, ticks <-chan time.Time, kicks <-chan struct{}, minKickGap time.Duration, leading func() bool, logger *slog.Logger, fn func()) {
	var lastRun time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
		case <-kicks:
			if wait := minKickGap - time.Since(lastRun); wait > 0 {
				timer := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		}
		if leading != nil && !leading() {
			continue
		}
		SafeCycle(name, logger, fn)
		lastRun = time.Now()
	}
}

// LeaderTickPoll is how often a running leader-gated cycle re-checks
// leadership (RunLeaderTick). The leadership watch polls every 5 seconds, so
// one second adds little to how long a step-down takes to stop the cycle.
const LeaderTickPoll = time.Second

// RunLeaderTick runs one cycle of a leader-gated loop that mutates cluster
// state (the warm-pool reconcile, the staging GC) under a context that ends at
// the cycle's budget or as soon as leading() reports false (#1602). The gated
// ticker only checks leadership when a tick starts, so without this a cycle in
// progress at a step-down kept creating and deleting pods next to the new
// leader's own loop, and one database call hung on a dead connection could hold
// it indefinitely. Both loops are level-triggered, so a cycle cut short is
// finished by the next one. A nil leading means always leading. An error is
// logged, except once the parent context is done (shutdown).
func RunLeaderTick(ctx context.Context, name string, budget, poll time.Duration, leading func() bool, logger *slog.Logger, fn func(context.Context) error) {
	tctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	if leading != nil {
		go func() {
			t := time.NewTicker(poll)
			defer t.Stop()
			for {
				select {
				case <-tctx.Done():
					return
				case <-t.C:
					if !leading() {
						cancel()
						return
					}
				}
			}
		}()
	}
	if err := fn(tctx); err != nil && ctx.Err() == nil {
		logger.Error(name, "error", err)
	}
}
