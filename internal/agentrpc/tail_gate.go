package agentrpc

import (
	"context"
	"log/slog"
	"time"

	"github.com/dexadata/dexaflow/internal/logs"
)

// LogSubscriberProbe is implemented by a LogPublisher that can tell whether
// anyone is tailing an attempt (logs.RedisTailer counts with PUBSUB NUMSUB,
// logs.MemoryTailer its own subscribers). With logs.tail.publish set to
// on_demand, StreamLogs then publishes only while someone listens; under the
// default (always), or with a publisher without it, every line is published.
type LogSubscriberProbe interface {
	HasSubscribers(ctx context.Context, ref logs.Ref) (bool, error)
}

// tailProbeInterval bounds how often one log stream asks whether its attempt is
// tailed: one probe per interval while lines flow, instead of one PUBLISH per
// line, plus one whenever the lines held since the last probe reach half of
// the replay bounds (see tailReplayMaxLines), so a probe is never more than an
// interval or half a ring of output away. It is also how late a new tail can be
// noticed, which the replay covers. var (not const) so tests and benchmarks can
// change it.
var tailProbeInterval = time.Second

// tailReplayMaxLines and tailReplayMaxBytes bound what a gate holds for replay
// between probes. The gate probes again as soon as what it holds reaches half of
// either bound, or before a line that would not fit in what is left, so the
// held lines never overflow between two probes however fast the task logs: the
// replay is complete whatever the output rate, and the probes (a Redis round
// trip each) stay at least half a ring of output apart. The one line that
// cannot be held is a single line larger than the whole byte budget (lines can
// be close to the 4 MiB message limit): the gate probes right before it, so a
// follower already there receives it live; with nobody there it is dropped from
// the replay and a follower who subscribes later reads it from the stored log.
// The byte bound is what keeps a stream nobody follows from holding gigabytes.
// vars (not consts) so tests can lower them.
var (
	tailReplayMaxLines = 1024
	tailReplayMaxBytes = 1 << 20 // 1 MiB
)

// tailGate decides, per log stream, whether a line is published for the live
// tail. While a probe has found nobody, lines are held instead of published.
// When a later probe finds a subscriber, the held lines are published first, in
// order and ahead of the line that triggered the probe, so a tail that
// subscribed between two probes receives every line that arrived after it
// subscribed: the held lines are exactly those received since the last probe
// that found nobody, the earliest moment that subscriber can have been
// listening, and the gate probes again before holding a line could cost a held
// line its place (see tailReplayMaxLines), so the guarantee does not depend on
// how fast the task logs. A probe that finds nobody drops what was held, since
// nobody could have been waiting for it. A probe that fails counts as a
// subscriber, so a broken probe never costs a watcher its lines. The gate probes
// only when a line arrives: a follower that subscribes while the task is silent
// is noticed, and the lines held before that replayed, when the task's next
// line comes. Replayed lines carry the replay flag (logs.MarkReplay), and the
// tail reader skips only flagged lines it already served from the stored log
// (see the api package).
//
// A gate belongs to one stream's receive loop and is not safe for concurrent use.
type tailGate struct {
	pub   LogPublisher
	probe LogSubscriberProbe // nil: publish every line
	ref   logs.Ref
	now   func() time.Time

	probedAt  time.Time
	listening bool
	held      replayRing
	warned    bool
}

// newTailGate builds the gate for one attempt's stream. Unless onDemand is set
// (logs.tail.publish: on_demand) the gate never probes and publishes every line
// as it arrives.
func newTailGate(pub LogPublisher, ref logs.Ref, now func() time.Time, onDemand bool) *tailGate {
	g := &tailGate{pub: pub, ref: ref, now: now}
	if probe, ok := pub.(LogSubscriberProbe); ok && onDemand {
		g.probe = probe
	}
	return g
}

// publish offers one encoded line to the live tail.
func (g *tailGate) publish(ctx context.Context, line string) {
	if g.probe != nil && g.probeDue(line) {
		g.refresh(ctx)
	}
	if g.probe != nil && !g.listening {
		g.hold(line)
		return
	}
	g.send(ctx, line)
}

// probeDue reports whether the gate asks for subscribers before offering line:
// on the first line, once the last probe is tailProbeInterval old, and, while
// lines are being held, once the ring is half full or line would not fit in it,
// so that no held line is dropped between two probes. While a subscriber is
// known the ring is empty and only the interval counts.
func (g *tailGate) probeDue(line string) bool {
	if g.probedAt.IsZero() || g.now().Sub(g.probedAt) >= tailProbeInterval {
		return true
	}
	return !g.listening && g.held.nearFull(line)
}

// refresh probes for subscribers and applies the transition: replay what was
// held when someone arrived, forget it when nobody is there.
func (g *tailGate) refresh(ctx context.Context) {
	g.probedAt = g.now()
	listening, err := g.probe.HasSubscribers(ctx, g.ref)
	if err != nil {
		// Fail open: a broken probe must not cost a watcher its lines.
		if !g.warned {
			slog.Warn("probing log tail subscribers failed; publishing every line", "task", g.ref.TaskID, "error", err)
			g.warned = true
		}
		listening = true
	}
	if !listening {
		g.held.reset()
		g.listening = false
		return
	}
	g.listening = true
	g.held.drain(func(held string) { g.send(ctx, logs.MarkReplay(held)) })
}

// hold keeps a line for a possible replay (see replayRing).
func (g *tailGate) hold(line string) { g.held.push(line) }

// replayRing holds the newest lines received since the last probe, within
// tailReplayMaxLines and tailReplayMaxBytes, as a ring so that holding a line
// costs O(1) however full the ring is. What it holds is always a contiguous run
// ending at the newest line: a line larger than the whole byte budget empties
// the ring rather than leave a replay with a gap. The gate probes, and so
// empties the ring, before a push that would drop a line (see nearFull), so the
// drop in push is the ring's own safety net for its memory bound.
type replayRing struct {
	lines []string // ring storage, grown on demand up to tailReplayMaxLines
	head  int      // index of the oldest held line
	n     int      // number of held lines
	bytes int      // total length of the held lines
}

// nearFull reports whether the ring holds at least half of either bound, or
// cannot take line without dropping a held line: the point at which the gate
// probes again, so what the ring holds between two probes is never dropped.
// Because the ring is emptied by every probe, what it holds is exactly what
// arrived since the last one, so this needs no counter of its own.
func (r *replayRing) nearFull(line string) bool {
	return r.n >= tailReplayMaxLines/2 || r.bytes >= tailReplayMaxBytes/2 || r.bytes+len(line) > tailReplayMaxBytes
}

// push holds line, dropping the oldest lines past either bound.
func (r *replayRing) push(line string) {
	if len(line) > tailReplayMaxBytes {
		r.reset()
		return
	}
	for r.n > 0 && (r.n >= tailReplayMaxLines || r.bytes+len(line) > tailReplayMaxBytes) {
		r.bytes -= len(r.lines[r.head])
		r.lines[r.head] = ""
		r.head = (r.head + 1) % len(r.lines)
		r.n--
	}
	if r.n == len(r.lines) {
		r.grow()
	}
	r.lines[(r.head+r.n)%len(r.lines)] = line
	r.n++
	r.bytes += len(line)
}

// grow doubles the storage (up to tailReplayMaxLines), oldest line first. Called
// only when the storage is full.
func (r *replayRing) grow() {
	next := make([]string, min(max(2*len(r.lines), 16), max(tailReplayMaxLines, 1)))
	for i := 0; i < r.n; i++ {
		next[i] = r.lines[(r.head+i)%len(r.lines)]
	}
	r.lines, r.head = next, 0
}

// drain hands every held line to fn, oldest first, and empties the ring.
func (r *replayRing) drain(fn func(string)) {
	for i := 0; i < r.n; i++ {
		fn(r.lines[(r.head+i)%len(r.lines)])
	}
	r.reset()
}

// reset drops every held line, keeping the storage for reuse.
func (r *replayRing) reset() {
	clear(r.lines)
	r.head, r.n, r.bytes = 0, 0, 0
}

func (g *tailGate) send(ctx context.Context, line string) {
	if err := g.pub.Publish(ctx, g.ref, line); err != nil {
		slog.Warn("publishing log tail", "task", g.ref.TaskID, "error", err)
	}
}
