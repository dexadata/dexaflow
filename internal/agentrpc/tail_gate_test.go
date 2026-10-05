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
	return newTailGate(p, logs.Ref{TaskID: "t"}, c.now, true)
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
	g := newTailGate(pub, logs.Ref{}, time.Now, true)
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

// TestTailGateAlwaysPublishesWithoutProbing pins the default of
// logs.tail.publish: under "always" every line is published as it arrives and
// the publisher is never probed, even when it could count subscribers.
func TestTailGateAlwaysPublishesWithoutProbing(t *testing.T) {
	pub, clock := &probingPublisher{}, newFakeClock()
	g := newTailGate(pub, logs.Ref{TaskID: "t"}, clock.now, false)
	for _, l := range []string{"A", "B", "C"} {
		g.publish(context.Background(), l)
		clock.advance(tailProbeInterval)
	}
	if got := strings.Join(pub.published, ","); got != "A,B,C" {
		t.Fatalf("published %q, want A,B,C", got)
	}
	if pub.probes != 0 {
		t.Errorf("probed %d times under \"always\", want 0", pub.probes)
	}
}

// TestServerTailGateFollowsPublishMode: a Server publishes every line unless
// it was switched to on-demand publishing.
func TestServerTailGateFollowsPublishMode(t *testing.T) {
	s := &Server{tail: &probingPublisher{}}
	if g := s.tailGateFor(logs.Ref{TaskID: "t"}); g.probe != nil {
		t.Fatal("a default Server gates the live tail, want every line published")
	}
	s.SetTailPublishOnDemand(true)
	if g := s.tailGateFor(logs.Ref{TaskID: "t"}); g.probe == nil {
		t.Fatal("an on-demand Server does not probe for subscribers")
	}
}

// TestTailGateBoundsHeldBytes: lines can be close to the 4 MiB message limit,
// so the replay is bounded by bytes as well as by lines. The newest lines that
// fit are kept, and a line larger than the whole budget leaves nothing held
// rather than a replay with a gap.
func TestTailGateBoundsHeldBytes(t *testing.T) {
	orig := tailReplayMaxBytes
	tailReplayMaxBytes = 10
	t.Cleanup(func() { tailReplayMaxBytes = orig })
	pub, clock := &probingPublisher{}, newFakeClock()
	g := gateFor(pub, clock)
	for _, l := range []string{"aaaa", "bbbb", "cccc", "dddd"} {
		g.publish(context.Background(), l)
	}
	if got := g.held.bytes; got > tailReplayMaxBytes {
		t.Fatalf("held %d bytes, want at most %d", got, tailReplayMaxBytes)
	}
	pub.watching = true
	clock.advance(tailProbeInterval)
	g.publish(context.Background(), "e")
	if got := strings.Join(pub.published, ","); got != "cccc,dddd,e" {
		t.Fatalf("published %q, want the newest lines that fit (cccc,dddd) then e", got)
	}

	pub.published, pub.watching = nil, false
	clock.advance(tailProbeInterval)
	g.publish(context.Background(), "ffff")
	g.publish(context.Background(), strings.Repeat("g", 11))
	pub.watching = true
	clock.advance(tailProbeInterval)
	g.publish(context.Background(), "h")
	if got := strings.Join(pub.published, ","); got != "h" {
		t.Fatalf("published %q, want only h after an oversized line emptied the replay", got)
	}
}

// TestTailGateMarksReplayedLines: a replayed line carries the replay flag so a
// follower skips only replayed lines it already read from the store; a line
// published as it arrives carries none.
func TestTailGateMarksReplayedLines(t *testing.T) {
	pub, clock := &probingPublisher{}, newFakeClock()
	g := gateFor(pub, clock)
	held := logs.EncodeLine(logs.Event{Message: "held"})
	live := logs.EncodeLine(logs.Event{Message: "live"})
	g.publish(context.Background(), held) // probe: nobody, held
	pub.watching = true
	clock.advance(tailProbeInterval)
	g.publish(context.Background(), live) // probe: subscribed, replay then live
	if len(pub.published) != 2 {
		t.Fatalf("published %d lines, want the replay and the live line", len(pub.published))
	}
	if raw, replay := logs.SplitReplay(pub.published[0]); !replay || raw != held {
		t.Errorf("replayed line = %q, want %q flagged as a replay", pub.published[0], held)
	}
	if raw, replay := logs.SplitReplay(pub.published[1]); replay || raw != live {
		t.Errorf("live line = %q, want %q unflagged", pub.published[1], live)
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
