package agentrpc

import (
	"context"
	"log/slog"
	"time"

	"github.com/dexadata/dexaflow/internal/logs"
)

// LogSubscriberProbe is implemented by a LogPublisher that can tell whether
// anyone is tailing an attempt (logs.RedisTailer counts with PUBSUB NUMSUB,
// logs.MemoryTailer its own subscribers). StreamLogs then publishes only while
// someone listens; a publisher without it gets every line, as before.
type LogSubscriberProbe interface {
	HasSubscribers(ctx context.Context, ref logs.Ref) (bool, error)
}

// tailProbeInterval bounds how often one log stream asks whether its attempt is
// tailed: one probe per interval while lines flow, instead of one PUBLISH per
// line. It is also how late a new tail can be noticed, which the replay covers.
// var (not const) so tests and benchmarks can change it.
var tailProbeInterval = time.Second

// tailReplayMaxLines bounds the lines a gate holds for replay between probes.
var tailReplayMaxLines = 1024

// tailGate decides, per log stream, whether a line is published for the live
// tail. While a probe has found nobody, lines are held instead of published.
// When a later probe finds a subscriber, the held lines are published first, in
// order, so a tail that subscribed between two probes still receives every line
// that arrived after it subscribed: the held lines are exactly those received
// since the last probe that found nobody, the earliest moment that subscriber
// can have been listening. A probe that finds nobody drops what was held, since
// nobody could have been waiting for it. The tail reader skips the leading
// replayed lines it already served from the stored log (see the api package).
//
// A gate belongs to one stream's receive loop and is not safe for concurrent use.
type tailGate struct {
	pub   LogPublisher
	probe LogSubscriberProbe // nil: publish every line
	ref   logs.Ref
	now   func() time.Time

	probedAt  time.Time
	listening bool
	held      []string
	warned    bool
}

// newTailGate builds the gate for one attempt's stream.
func newTailGate(pub LogPublisher, ref logs.Ref, now func() time.Time) *tailGate {
	g := &tailGate{pub: pub, ref: ref, now: now}
	if probe, ok := pub.(LogSubscriberProbe); ok {
		g.probe = probe
	}
	return g
}

// publish offers one encoded line to the live tail.
func (g *tailGate) publish(ctx context.Context, line string) {
	if g.probe != nil && (g.probedAt.IsZero() || g.now().Sub(g.probedAt) >= tailProbeInterval) {
		g.refresh(ctx)
	}
	if g.probe != nil && !g.listening {
		g.hold(line)
		return
	}
	g.send(ctx, line)
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
		g.held = g.held[:0]
		g.listening = false
		return
	}
	g.listening = true
	for _, held := range g.held {
		g.send(ctx, held)
	}
	g.held = g.held[:0]
}

// hold keeps a line for a possible replay, dropping the oldest past the bound.
func (g *tailGate) hold(line string) {
	if len(g.held) >= tailReplayMaxLines {
		g.held = append(g.held[:0], g.held[len(g.held)-tailReplayMaxLines+1:]...)
	}
	g.held = append(g.held, line)
}

func (g *tailGate) send(ctx context.Context, line string) {
	if err := g.pub.Publish(ctx, g.ref, line); err != nil {
		slog.Warn("publishing log tail", "task", g.ref.TaskID, "error", err)
	}
}
