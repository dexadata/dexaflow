package agentrpc

import (
	"sync"
	"sync/atomic"
)

// TailReplayMeter records what the on_demand live-tail gates hold for replay
// (observability.Metrics implements it). Optional.
type TailReplayMeter interface {
	// SetLogTailReplayBytes reports the bytes every gate holds together.
	SetLogTailReplayBytes(n int64)
	// RecordLogTailReplayEviction counts a ring emptied by the budget.
	RecordLogTailReplayEviction()
}

// tailBudget bounds what all of a server's tail gates hold for replay together
// (logs.tail.replay_budget_bytes, #1621). Each gate bounds its own ring
// (tailReplayMaxBytes), but a gate exists per log stream, so a thousand streams
// that nobody follows and whose tasks went quiet after a burst could hold a
// ring each, about 500 MiB, until their next line.
//
// When a held line takes the total over the budget, whole rings are dropped,
// starting with the gate that held a line least recently: the stream silent the
// longest is the least likely to gain a follower before its next probe. A ring
// is dropped whole, never trimmed, so what a gate holds stays a contiguous run
// ending at its newest line. What a dropped ring held is still in the stored
// log; a follower who subscribes before that stream's next probe reads it from
// there instead of from the replay, the same as a line larger than the ring.
//
// A budget of 0 bounds nothing; the gates still report what they hold.
type tailBudget struct {
	max   int64
	meter TailReplayMeter

	held atomic.Int64 // bytes held by every gate together

	mu    sync.Mutex // guards gates; never taken while a gate's mu is held
	gates map[*tailGate]struct{}
}

func newTailBudget(maxBytes int64, meter TailReplayMeter) *tailBudget {
	return &tailBudget{max: maxBytes, meter: meter, gates: map[*tailGate]struct{}{}}
}

func (b *tailBudget) join(g *tailGate) {
	b.mu.Lock()
	b.gates[g] = struct{}{}
	b.mu.Unlock()
}

func (b *tailBudget) leave(g *tailGate) {
	b.mu.Lock()
	delete(b.gates, g)
	b.mu.Unlock()
}

// charge adds delta bytes (negative when a ring empties) to the total.
func (b *tailBudget) charge(delta int64) {
	if delta == 0 {
		return
	}
	n := b.held.Add(delta)
	if b.meter != nil {
		b.meter.SetLogTailReplayBytes(n)
	}
}

// enforce drops rings, the least recently held first, until the total fits the
// budget. self is the gate that just held a line: its ring goes last, only when
// dropping every other ring was not enough.
func (b *tailBudget) enforce(self *tailGate) {
	if b.max <= 0 || b.held.Load() <= b.max {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for b.held.Load() > b.max {
		victim := b.oldestHolder(self)
		if victim == nil {
			if self.heldBytes.Load() == 0 {
				return
			}
			victim = self
		}
		victim.evict()
		if b.meter != nil {
			b.meter.RecordLogTailReplayEviction()
		}
	}
}

// oldestHolder returns the gate other than self that holds something and held
// its newest line least recently, or nil. Called with b.mu held.
func (b *tailBudget) oldestHolder(self *tailGate) *tailGate {
	var oldest *tailGate
	var oldestAt int64
	for g := range b.gates {
		if g == self || g.heldBytes.Load() == 0 {
			continue
		}
		if at := g.heldAt.Load(); oldest == nil || at < oldestAt {
			oldest, oldestAt = g, at
		}
	}
	return oldest
}
