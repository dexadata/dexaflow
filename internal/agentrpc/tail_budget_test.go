package agentrpc

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/logs"
)

// fakeReplayMeter records what the budget reports.
type fakeReplayMeter struct {
	bytes     atomic.Int64
	evictions atomic.Int64
}

func (m *fakeReplayMeter) SetLogTailReplayBytes(n int64) { m.bytes.Store(n) }
func (m *fakeReplayMeter) RecordLogTailReplayEviction()  { m.evictions.Add(1) }

// silentBurst makes g hold lines totaling about n bytes: the first line probes
// and finds nobody, the rest are held without a probe, since the clock does not
// move and the burst stays under half the ring.
func silentBurst(g *tailGate, n int) {
	line := strings.Repeat("x", 1023)
	for held := 0; held < n; held += len(line) {
		g.publish(context.Background(), line)
	}
}

// TestTailBudgetDropsTheLongestSilentRingsFirst pins #1621: streams nobody
// follows each hold up to half a ring after a burst, and the budget bounds the
// sum by dropping whole rings, starting with the stream that held a line least
// recently, so the streams that logged last keep their replay.
func TestTailBudgetDropsTheLongestSilentRingsFirst(t *testing.T) {
	clock := newFakeClock()
	meter := &fakeReplayMeter{}
	budget := newTailBudget(300<<10, meter) // room for about two 128 KiB bursts
	gates := make([]*tailGate, 5)
	for i := range gates {
		gates[i] = newTailGate(&probingPublisher{}, logs.Ref{TaskID: fmt.Sprint(i)}, clock.now, true, budget)
		silentBurst(gates[i], 128<<10)
		clock.advance(time.Millisecond)
		if held := budget.held.Load(); held > budget.max {
			t.Fatalf("after stream %d the gates hold %d bytes, over the %d byte budget", i, held, budget.max)
		}
	}
	for i, g := range gates {
		kept := g.heldBytes.Load() > 0
		if want := i >= 3; kept != want {
			t.Errorf("stream %d kept its ring = %v, want %v (only the two that logged last fit)", i, kept, want)
		}
	}
	if got, want := meter.bytes.Load(), budget.held.Load(); got != want {
		t.Errorf("meter reports %d bytes held, the gates hold %d", got, want)
	}
	if got := meter.evictions.Load(); got != 3 {
		t.Errorf("meter counted %d dropped rings, want 3", got)
	}
}

// TestTailBudgetZeroBoundsNothing pins the default: with no budget every ring
// is kept, as before, and the meter still sees what is held.
func TestTailBudgetZeroBoundsNothing(t *testing.T) {
	clock := newFakeClock()
	meter := &fakeReplayMeter{}
	budget := newTailBudget(0, meter)
	for i := 0; i < 5; i++ {
		silentBurst(newTailGate(&probingPublisher{}, logs.Ref{TaskID: fmt.Sprint(i)}, clock.now, true, budget), 128<<10)
	}
	if held := meter.bytes.Load(); held < 5*128<<10 {
		t.Errorf("meter reports %d bytes, want the five bursts (%d or more)", held, 5*128<<10)
	}
	if n := meter.evictions.Load(); n != 0 {
		t.Errorf("a zero budget dropped %d rings", n)
	}
}

// TestTailBudgetReleasesAClosedStream pins that a stream's ring leaves the
// total when the stream ends, so finished attempts cannot use up the budget.
func TestTailBudgetReleasesAClosedStream(t *testing.T) {
	clock := newFakeClock()
	budget := newTailBudget(1<<20, nil)
	g := newTailGate(&probingPublisher{}, logs.Ref{TaskID: "t"}, clock.now, true, budget)
	silentBurst(g, 64<<10)
	if budget.held.Load() == 0 {
		t.Fatal("the burst was not charged to the budget")
	}
	g.close()
	if held := budget.held.Load(); held != 0 {
		t.Errorf("after close the budget still counts %d bytes", held)
	}
	if _, joined := budget.gates[g]; joined {
		t.Error("a closed gate is still a candidate for eviction")
	}
}

// TestTailBudgetDroppedRingReplaysOnlyLaterLines pins what a follower of a
// stream whose ring was dropped receives: the lines held after the drop, in
// order and flagged as replay, never a run with a gap in it.
func TestTailBudgetDroppedRingReplaysOnlyLaterLines(t *testing.T) {
	clock := newFakeClock()
	budget := newTailBudget(1, nil) // any held line overflows
	pub := &probingPublisher{}
	g := newTailGate(pub, logs.Ref{TaskID: "t"}, clock.now, true, budget)
	g.publish(context.Background(), "first") // probes, finds nobody, held then dropped
	if g.heldBytes.Load() != 0 {
		t.Fatalf("the ring holds %d bytes over a 1 byte budget", g.heldBytes.Load())
	}
	pub.watching = true
	clock.advance(tailProbeInterval)
	g.publish(context.Background(), "second")
	if got := strings.Join(pub.published, ","); got != "second" {
		t.Errorf("published %q, want only the line after the drop", got)
	}
}

// TestTailBudgetConcurrentStreams runs gates on their own goroutines against
// one budget, the way StreamLogs does, for the race detector, and checks the
// total ends where the rings say.
func TestTailBudgetConcurrentStreams(t *testing.T) {
	budget := newTailBudget(256<<10, &fakeReplayMeter{})
	var wg sync.WaitGroup
	gates := make([]*tailGate, 16)
	for i := range gates {
		gates[i] = newTailGate(&probingPublisher{}, logs.Ref{TaskID: fmt.Sprint(i)}, newFakeClock().now, true, budget)
		wg.Add(1)
		go func(g *tailGate) {
			defer wg.Done()
			silentBurst(g, 96<<10)
		}(gates[i])
	}
	wg.Wait()
	var sum int64
	for _, g := range gates {
		sum += g.heldBytes.Load()
	}
	if held := budget.held.Load(); held != sum || held > budget.max {
		t.Errorf("budget counts %d bytes, rings hold %d, budget %d", held, sum, budget.max)
	}
	for _, g := range gates {
		g.close()
	}
	if held := budget.held.Load(); held != 0 {
		t.Errorf("after every stream closed the budget counts %d bytes", held)
	}
}
