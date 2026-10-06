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
// counting both kinds of call. onProbe, when set, runs right after a probe has
// been answered, with the probe's ordinal, so a test can subscribe a follower
// at that moment.
type probingPublisher struct {
	published []string
	watching  bool
	probeErr  error
	probes    int
	onProbe   func(n int)
}

func (p *probingPublisher) Publish(_ context.Context, _ logs.Ref, line string) error {
	p.published = append(p.published, line)
	return nil
}

func (p *probingPublisher) HasSubscribers(context.Context, logs.Ref) (bool, error) {
	p.probes++
	watching := p.watching
	if p.onProbe != nil {
		p.onProbe(p.probes)
	}
	return watching, p.probeErr
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

// TestTailGateProbesBeforeHeldLinesOverflow: the lines held for a replay are
// bounded in memory by probing, not by dropping. With nobody listening the gate
// holds at most half of tailReplayMaxLines, probing once per half ring, and
// when a follower is found it replays everything since the last probe, oldest
// first, ahead of the line that found the follower.
func TestTailGateProbesBeforeHeldLinesOverflow(t *testing.T) {
	pub, clock := &probingPublisher{}, newFakeClock()
	g := gateFor(pub, clock)
	n, half := tailReplayMaxLines+10, tailReplayMaxLines/2
	for i := 0; i < n; i++ {
		g.publish(context.Background(), fmt.Sprintf("%d", i))
		if g.held.n > half {
			t.Fatalf("held %d lines after line %d, want at most %d", g.held.n, i, half)
		}
	}
	if len(pub.published) != 0 {
		t.Fatalf("published %d lines with no subscriber, want 0", len(pub.published))
	}
	if want := 1 + (n-1)/half; pub.probes != want {
		t.Errorf("probed %d times for %d lines at one instant, want %d (one per half ring)", pub.probes, n, want)
	}
	pub.watching = true
	clock.advance(tailProbeInterval)
	g.publish(context.Background(), "last")
	var want []string
	for i := (n - 1) / half * half; i < n; i++ { // held since the last probe
		want = append(want, fmt.Sprintf("%d", i))
	}
	want = append(want, "last")
	if got := strings.Join(pub.published, ","); got != strings.Join(want, ",") {
		t.Fatalf("published %q, want the lines held since the last probe then last: %q", got, want)
	}
}

// TestTailGateKeepsEveryLineForAFollowerBetweenProbes pins the no-gap
// guarantee against the task's output rate: a follower that subscribes right
// after a probe that found nobody receives every line that arrives after it
// subscribed, however many arrive before the next timed probe. The gate used
// to hold at most tailReplayMaxLines of them until the next interval, so a
// task logging faster than that lost its oldest lines on a follower that was
// already there. The probe answers nobody once and then finds the follower,
// which costs a bounded number of probes.
func TestTailGateKeepsEveryLineForAFollowerBetweenProbes(t *testing.T) {
	pub, clock := &probingPublisher{}, newFakeClock()
	g := gateFor(pub, clock)
	first := logs.EncodeLine(logs.Event{Message: "before the follower"})
	g.publish(context.Background(), first) // the probe finds nobody and the line is held
	pub.watching = true                    // the follower subscribes here
	const n = 2000
	want := []string{first}
	for i := 0; i < n; i++ { // all at one instant: no timed probe
		line := logs.EncodeLine(logs.Event{Message: fmt.Sprintf("l%d", i)})
		want = append(want, line)
		g.publish(context.Background(), line)
	}
	got, replayed := deliveredLines(t, pub.published)
	assertSameLines(t, got, want)
	if replayed != tailReplayMaxLines/2 {
		t.Errorf("replayed %d lines, want the %d held when the half ring probe found the follower", replayed, tailReplayMaxLines/2)
	}
	if limit := n/(tailReplayMaxLines/2) + 2; pub.probes > limit {
		t.Errorf("probed %d times for %d lines, want at most %d", pub.probes, n, limit)
	}
}

// TestTailGateKeepsEveryLargeLineForAFollowerBetweenProbes is the byte-bound
// twin: with lines large enough that the byte budget is what the held lines
// reach first, the follower still receives every line, and the probe that
// found it came from the bytes, well before the line count could have.
func TestTailGateKeepsEveryLargeLineForAFollowerBetweenProbes(t *testing.T) {
	orig := tailReplayMaxBytes
	tailReplayMaxBytes = 16 << 10
	t.Cleanup(func() { tailReplayMaxBytes = orig })
	pub, clock := &probingPublisher{}, newFakeClock()
	g := gateFor(pub, clock)
	first := logs.EncodeLine(logs.Event{Message: "before the follower"})
	g.publish(context.Background(), first) // the probe finds nobody and the line is held
	pub.watching = true                    // the follower subscribes here
	const n = 500
	want, total := []string{first}, len(first)
	for i := 0; i < n; i++ { // all at one instant: no timed probe
		line := logs.EncodeLine(logs.Event{Message: fmt.Sprintf("l%d %s", i, strings.Repeat("x", 1000))})
		want = append(want, line)
		total += len(line)
		g.publish(context.Background(), line)
	}
	got, replayed := deliveredLines(t, pub.published)
	assertSameLines(t, got, want)
	if replayed == 0 || replayed >= tailReplayMaxLines/2 {
		t.Errorf("replayed %d lines, want a replay cut by the byte bound, before %d lines", replayed, tailReplayMaxLines/2)
	}
	if limit := total/(tailReplayMaxBytes/2) + 2; pub.probes > limit {
		t.Errorf("probed %d times for %d bytes, want at most %d", pub.probes, total, limit)
	}
}

// deliveredLines strips the replay flag from every published line, counting
// the flagged ones, and checks that they come first: a replay is what the
// follower would have missed, so it must precede every live line.
func deliveredLines(t *testing.T, published []string) (raw []string, replayed int) {
	t.Helper()
	live := false
	for i, line := range published {
		r, replay := logs.SplitReplay(line)
		raw = append(raw, r)
		switch {
		case replay && live:
			t.Fatalf("line %d is a replay published after a live line", i)
		case replay:
			replayed++
		default:
			live = true
		}
	}
	return raw, replayed
}

// assertSameLines fails on the first line that differs between got and want.
func assertSameLines(t *testing.T, got, want []string) {
	t.Helper()
	for i := range min(len(got), len(want)) {
		if got[i] != want[i] {
			t.Fatalf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
	if len(got) != len(want) {
		t.Fatalf("published %d lines, want %d", len(got), len(want))
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

// TestTailGateProbesBeforeHeldBytesOverflow: lines can be close to the 4 MiB
// message limit, so the replay is bounded by bytes as well as by lines, and the
// gate probes before the held bytes could overflow, in both ways a line can get
// there: the held bytes passing half the budget, and a line that does not fit
// in what is left of it. Nothing held is ever dropped.
func TestTailGateProbesBeforeHeldBytesOverflow(t *testing.T) {
	orig := tailReplayMaxBytes
	tailReplayMaxBytes = 1000
	t.Cleanup(func() { tailReplayMaxBytes = orig })
	pub, clock := &probingPublisher{}, newFakeClock()
	g := gateFor(pub, clock)
	line := strings.Repeat
	g.publish(context.Background(), line("a", 400)) // the probe finds nobody and the line is held
	g.publish(context.Background(), line("b", 400)) // fits: held
	if pub.probes != 1 || g.held.bytes != 800 {
		t.Fatalf("after 800 bytes: %d probes, %d bytes held, want 1 probe and 800 bytes", pub.probes, g.held.bytes)
	}
	g.publish(context.Background(), line("c", 400)) // half the budget is held, so the probe runs, finds nobody and forgets a and b
	if pub.probes != 2 || g.held.bytes != 400 {
		t.Fatalf("past half the budget: %d probes, %d bytes held, want 2 probes and only c", pub.probes, g.held.bytes)
	}
	g.publish(context.Background(), line("d", 700)) // would not fit beside c, so the probe runs, finds nobody and forgets c
	if pub.probes != 3 || g.held.bytes != 700 {
		t.Fatalf("after a line that did not fit: %d probes, %d bytes held, want 3 probes and only d", pub.probes, g.held.bytes)
	}
	pub.watching = true
	g.publish(context.Background(), line("e", 100)) // half the budget held: probe finds the follower
	if got := lineShapes(pub.published); got != "d:700,e:100" {
		t.Fatalf("published %s, want d replayed then e", got)
	}
}

// TestTailGateDropsALineLargerThanTheByteBudget documents the one line the
// replay cannot hold. The gate probes right before it, so a follower already
// there receives it live, and the lines held before it are settled by that
// probe like any other: replayed to a follower it finds, forgotten otherwise.
// With nobody there the line is dropped from the replay and a later follower
// reads it from the stored log.
func TestTailGateDropsALineLargerThanTheByteBudget(t *testing.T) {
	orig := tailReplayMaxBytes
	tailReplayMaxBytes = 10
	t.Cleanup(func() { tailReplayMaxBytes = orig })
	pub, clock := &probingPublisher{}, newFakeClock()
	g := gateFor(pub, clock)
	g.publish(context.Background(), "aaaa")                  // the probe finds nobody and the line is held
	g.publish(context.Background(), strings.Repeat("g", 11)) // cannot fit, so the probe runs, finds nobody, forgets aaaa and does not hold g
	if pub.probes != 2 || g.held.n != 0 {
		t.Fatalf("after an oversized line: %d probes, %d lines held, want 2 probes and nothing held", pub.probes, g.held.n)
	}
	pub.watching = true
	clock.advance(tailProbeInterval)
	g.publish(context.Background(), "h")
	if got := strings.Join(pub.published, ","); got != "h" {
		t.Fatalf("published %q, want only h: the oversized line had no follower to go to", got)
	}

	pub.published, pub.watching = nil, false
	clock.advance(tailProbeInterval)
	g.publish(context.Background(), "bbbb") // the probe finds nobody and the line is held
	pub.watching = true
	g.publish(context.Background(), strings.Repeat("g", 11)) // cannot fit: probe finds the follower
	if got := lineShapes(pub.published); got != "b:4,g:11" {
		t.Fatalf("published %s, want bbbb replayed then the oversized line live", got)
	}
}

// TestReplayRingKeepsTheNewestPastEitherBound: the ring enforces its own memory
// bound whatever is pushed into it, as a contiguous run of the newest lines,
// and a line larger than the whole budget empties it. Through the gate it is
// emptied by a probe before it would have to drop anything.
func TestReplayRingKeepsTheNewestPastEitherBound(t *testing.T) {
	var r replayRing
	for i := 0; i < tailReplayMaxLines+10; i++ {
		r.push(fmt.Sprintf("%d", i))
	}
	var got []string
	r.drain(func(l string) { got = append(got, l) })
	if len(got) != tailReplayMaxLines || got[0] != "10" || got[len(got)-1] != fmt.Sprint(tailReplayMaxLines+9) {
		t.Fatalf("drained %d lines from %q to %q, want the newest %d, 10 first", len(got), got[0], got[len(got)-1], tailReplayMaxLines)
	}

	orig := tailReplayMaxBytes
	tailReplayMaxBytes = 10
	t.Cleanup(func() { tailReplayMaxBytes = orig })
	for _, l := range []string{"aaaa", "bbbb", "cccc", "dddd"} {
		r.push(l)
		if r.bytes > tailReplayMaxBytes {
			t.Fatalf("held %d bytes, want at most %d", r.bytes, tailReplayMaxBytes)
		}
	}
	got = nil
	r.drain(func(l string) { got = append(got, l) })
	if strings.Join(got, ",") != "cccc,dddd" {
		t.Fatalf("drained %q, want the newest lines that fit (cccc,dddd)", got)
	}
	r.push("ffff")
	r.push(strings.Repeat("g", 11))
	if r.n != 0 || r.bytes != 0 {
		t.Fatalf("held %d lines and %d bytes after an oversized line, want the ring emptied", r.n, r.bytes)
	}
}

// lineShapes renders published lines as first byte and length, so a failure
// message stays readable with long lines.
func lineShapes(lines []string) string {
	shapes := make([]string, len(lines))
	for i, l := range lines {
		shapes[i] = fmt.Sprintf("%s:%d", l[:1], len(l))
	}
	return strings.Join(shapes, ",")
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

// TestStreamLogsOnDemandDeliversEveryLineToAFollower drives the gate the way a
// task pod's stream does, through StreamLogs with on_demand publishing: a
// follower that subscribes right after the first probe found nobody receives
// every one of the lines that arrive before the next probe, in order, and the
// stream is stored in full. A timed probe, should the stream take longer than
// the interval, finds the same follower and changes nothing.
func TestStreamLogsOnDemandDeliversEveryLineToAFollower(t *testing.T) {
	srv, a := newServer(&fakeStore{})
	sink := &captureSink{}
	srv.SetLogSink(sink)
	pub := &probingPublisher{}
	pub.onProbe = func(n int) {
		if n == 1 {
			pub.watching = true
		}
	}
	srv.SetLogPublisher(pub)
	srv.SetTailPublishOnDemand(true)
	msgs := make([]*agentv1.LogLine, 2*tailReplayMaxLines+1)
	for i := range msgs {
		msgs[i] = &agentv1.LogLine{Message: fmt.Sprintf("l%d", i), Stream: "stdout"}
	}
	if err := srv.StreamLogs(&fakeStreamLogsServer{ctx: ctxWithToken(t, a), msgs: msgs}); err != nil {
		t.Fatalf("StreamLogs: %v", err)
	}
	got, replayed := deliveredLines(t, pub.published)
	if len(got) != len(msgs) {
		t.Fatalf("the follower received %d of %d lines (%d replayed, %d probes)", len(got), len(msgs), replayed, pub.probes)
	}
	for i, line := range got {
		if msg := logs.DecodeLine(line).Message; msg != msgs[i].GetMessage() {
			t.Fatalf("line %d = %q, want %q", i, msg, msgs[i].GetMessage())
		}
	}
	if stored, _ := sink.snapshot(); len(stored) != len(msgs) {
		t.Errorf("stored %d lines, want %d", len(stored), len(msgs))
	}
}

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
