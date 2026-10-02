package agentrpc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/logs"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// probingPublisher is a LogPublisher that also answers the subscriber probe,
// counting both kinds of call.
type probingPublisher struct {
	published []string
	watching  bool
	probeErr  error
	probes    int
}

func (p *probingPublisher) Publish(_ context.Context, _ logs.Ref, line string) error {
	p.published = append(p.published, line)
	return nil
}

func (p *probingPublisher) HasSubscribers(context.Context, logs.Ref) (bool, error) {
	p.probes++
	return p.watching, p.probeErr
}

// fakeClock is a settable clock for the gate's probe interval.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }
func newFakeClock() *fakeClock               { return &fakeClock{t: time.Unix(1_700_000_000, 0)} }
func gateFor(p LogPublisher, c *fakeClock) *tailGate {
	return newTailGate(p, logs.Ref{TaskID: "t"}, c.now)
}

// TestTailGateSkipsPublishWithoutSubscribers: nobody tails most attempts, and a
// Redis PUBLISH per line for them is pure overhead on the log stream. With no
// subscriber the gate publishes nothing and probes at most once per interval.
func TestTailGateSkipsPublishWithoutSubscribers(t *testing.T) {
	pub, clock := &probingPublisher{}, newFakeClock()
	g := gateFor(pub, clock)
	for i := 0; i < 100; i++ {
		g.publish(context.Background(), fmt.Sprintf("line %d", i))
	}
	if len(pub.published) != 0 {
		t.Fatalf("published %d lines with no subscriber, want 0", len(pub.published))
	}
	if pub.probes != 1 {
		t.Errorf("probed %d times within one interval, want 1", pub.probes)
	}
}

// TestTailGateReplaysLinesSinceTheLastEmptyProbe pins the no-gap guarantee: a
// subscriber that arrives between two probes is noticed only at the next one,
// so the lines received since the last probe that found nobody are published
// first, in order. Today's unconditional publish could not deliver more than
// that: those are exactly the lines a subscriber could have been listening for.
func TestTailGateReplaysLinesSinceTheLastEmptyProbe(t *testing.T) {
	pub, clock := &probingPublisher{}, newFakeClock()
	g := gateFor(pub, clock)
	g.publish(context.Background(), "A") // probe: nobody
	clock.advance(tailProbeInterval / 2)
	g.publish(context.Background(), "B") // within the interval: held
	pub.watching = true                  // a tail subscribes here
	clock.advance(tailProbeInterval / 2)
	g.publish(context.Background(), "C") // probe: subscribed
	g.publish(context.Background(), "D")
	if got := strings.Join(pub.published, ","); got != "A,B,C,D" {
		t.Fatalf("published %q, want A,B,C,D", got)
	}
}

// TestTailGateForgetsLinesBeforeAnEmptyProbe: a line received before a probe
// that found nobody had no possible reader, so it is not replayed later.
func TestTailGateForgetsLinesBeforeAnEmptyProbe(t *testing.T) {
	pub, clock := &probingPublisher{}, newFakeClock()
	g := gateFor(pub, clock)
	g.publish(context.Background(), "A")
	clock.advance(tailProbeInterval)
	g.publish(context.Background(), "B") // probe: still nobody, A is dropped
	pub.watching = true
	clock.advance(tailProbeInterval)
	g.publish(context.Background(), "C")
	if got := strings.Join(pub.published, ","); got != "B,C" {
		t.Fatalf("published %q, want B,C", got)
	}
}

// TestTailGateStopsAfterSubscribersLeave: publishing ends once the last tail is
// gone, within one probe interval.
func TestTailGateStopsAfterSubscribersLeave(t *testing.T) {
	pub, clock := &probingPublisher{watching: true}, newFakeClock()
	g := gateFor(pub, clock)
	g.publish(context.Background(), "A")
	pub.watching = false
	clock.advance(tailProbeInterval)
	g.publish(context.Background(), "B")
	g.publish(context.Background(), "C")
	if got := strings.Join(pub.published, ","); got != "A" {
		t.Fatalf("published %q, want only A", got)
	}
}

// TestTailGateFailsOpen: a probe error must not cost a watcher its lines, so the
// gate publishes as before.
func TestTailGateFailsOpen(t *testing.T) {
	pub, clock := &probingPublisher{probeErr: errors.New("redis down")}, newFakeClock()
	g := gateFor(pub, clock)
	g.publish(context.Background(), "A")
	g.publish(context.Background(), "B")
	if got := strings.Join(pub.published, ","); got != "A,B" {
		t.Fatalf("published %q, want A,B", got)
	}
}

// TestTailGateWithoutProbePublishesEverything keeps a publisher that cannot
// count subscribers on today's behavior.
func TestTailGateWithoutProbePublishesEverything(t *testing.T) {
	var got []string
	pub := publishFunc(func(line string) { got = append(got, line) })
	g := newTailGate(pub, logs.Ref{}, time.Now)
	g.publish(context.Background(), "A")
	g.publish(context.Background(), "B")
	if strings.Join(got, ",") != "A,B" {
		t.Fatalf("published %v, want A,B", got)
	}
}

// TestTailGateBoundsHeldLines: the lines held for a replay are bounded, keeping
// the newest.
func TestTailGateBoundsHeldLines(t *testing.T) {
	pub, clock := &probingPublisher{}, newFakeClock()
	g := gateFor(pub, clock)
	for i := 0; i < tailReplayMaxLines+10; i++ {
		g.publish(context.Background(), fmt.Sprintf("%d", i))
	}
	pub.watching = true
	clock.advance(tailProbeInterval)
	g.publish(context.Background(), "last")
	if n := len(pub.published); n != tailReplayMaxLines+1 {
		t.Fatalf("published %d lines, want the %d newest held lines plus the new one", n, tailReplayMaxLines)
	}
	if pub.published[0] != "10" {
		t.Errorf("first replayed line = %q, want the oldest kept (10)", pub.published[0])
	}
}

// publishFunc adapts a function to LogPublisher with no subscriber probe.
type publishFunc func(string)

func (f publishFunc) Publish(_ context.Context, _ logs.Ref, line string) error {
	f(line)
	return nil
}

// lineRecorder is a LogWriter that also takes pre-encoded lines.
type lineRecorder struct {
	events []logs.Event
	lines  []string
}

func (w *lineRecorder) WriteEvent(ev logs.Event) error { w.events = append(w.events, ev); return nil }
func (w *lineRecorder) WriteLine(line string) error    { w.lines = append(w.lines, line); return nil }
func (w *lineRecorder) Close() error                   { return nil }

// TestWriteLineEncodesOnce: the line handed to storage and the one published for
// the live tail are the same encoding, produced once.
func TestWriteLineEncodesOnce(t *testing.T) {
	w := &lineRecorder{}
	var published []string
	line := &agentv1.LogLine{Message: "hello", Stream: "stdout"}
	if err := writeLine(w, line, func(l string) { published = append(published, l) }, nil); err != nil {
		t.Fatalf("writeLine: %v", err)
	}
	if len(w.events) != 0 {
		t.Errorf("WriteEvent called %d times, want the pre-encoded path", len(w.events))
	}
	if len(w.lines) != 1 || len(published) != 1 || w.lines[0] != published[0] {
		t.Fatalf("stored %v and published %v, want the same single encoding", w.lines, published)
	}
	if logs.DecodeLine(published[0]).Message != "hello" {
		t.Errorf("published %q, want the encoded event", published[0])
	}
}
