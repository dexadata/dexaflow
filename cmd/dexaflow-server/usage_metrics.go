package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/gatedtick"
)

// Pacing of the usage counters: one cycle every usageCounterInterval reads at
// most usageCounterPages pages of usageCounterPageSize events, so a backlog is
// worked off over a few cycles instead of in one long read.
const (
	usageCounterInterval = 15 * time.Second
	usageCounterPageSize = 1000
	usageCounterPages    = 10
)

// usageEventSource is the store the usage counters read: the events after a
// cursor, and the horizon a new leader starts at.
type usageEventSource interface {
	Horizon(ctx context.Context) (domain.UsageCursor, error)
	ReadEvents(ctx context.Context, after domain.UsageCursor, limit int) (domain.UsagePage, error)
}

// usageCounters receives dexaflow_task_attempts_total and
// dexaflow_task_slot_seconds_total.
type usageCounters interface {
	RecordTaskAttempt(tenant, state string, slotSeconds float64)
}

// usageCounter advances the per-tenant usage counters from the usage events
// (ADR 0096 point 6), after its own cursor, so they and the service API never
// disagree. It counts each attempt once, by its first event (revision 0): a
// later re-mark is in the API only, since a counter cannot move a count from
// one state to another.
type usageCounter struct {
	src      usageEventSource
	rec      usageCounters
	leading  func() bool
	pageSize int
	logger   *slog.Logger
	// cursor is nil until this replica leads; it is dropped when it stops
	// leading, so each takeover starts at the horizon of that moment.
	cursor *domain.UsageCursor
}

func newUsageCounter(src usageEventSource, rec usageCounters, leading func() bool, pageSize int, logger *slog.Logger) *usageCounter {
	return &usageCounter{src: src, rec: rec, leading: leading, pageSize: pageSize, logger: logger}
}

// cycle runs one round. A replica that does not lead drops its cursor; one
// that has just taken the lead only reads the horizon, so a failover is a
// counter reset and never replays the events kept for the API.
func (c *usageCounter) cycle(ctx context.Context) {
	if !c.leading() {
		c.cursor = nil
		return
	}
	if c.cursor == nil {
		h, err := c.src.Horizon(ctx)
		if err != nil {
			c.logger.Debug("could not read the usage events horizon", "error", err)
			return
		}
		c.cursor = &h
		return
	}
	for range usageCounterPages {
		if !c.page(ctx) {
			return
		}
	}
}

// page reads and counts one page and reports whether a full page suggests
// more to read.
func (c *usageCounter) page(ctx context.Context) bool {
	p, err := c.src.ReadEvents(ctx, *c.cursor, c.pageSize)
	switch {
	case err != nil:
		c.logger.Debug("could not read usage events for the counters", "error", err)
		return false
	case !p.Enabled:
		return false
	case p.Gone:
		c.logger.Warn("usage counters skipped events deleted by retention; they count less than the events recorded",
			"from", *c.cursor, "resume", p.Oldest)
		c.cursor = &p.Oldest
		return true
	}
	for _, e := range p.Events {
		if e.Revision == 0 {
			c.rec.RecordTaskAttempt(e.Tenant, string(e.State), slotSeconds(e))
		}
		c.cursor = &e.Cursor
	}
	return len(p.Events) == c.pageSize
}

// slotSeconds is the attempt's size times its duration, 0 without both times.
func slotSeconds(e domain.UsageEvent) float64 {
	if e.StartedAt == nil || e.EndedAt == nil || e.EndedAt.Before(*e.StartedAt) {
		return 0
	}
	return float64(e.PoolSlots) * e.EndedAt.Sub(*e.StartedAt).Seconds()
}

// startUsageCounters runs the counters while recording and usage metrics are
// both on, and reports whether it did. The ticker checks leadership itself,
// so the counter sees each loss of the lead.
func startUsageCounters(ctx context.Context, recording, metrics bool, src usageEventSource, rec usageCounters, leading func() bool, logger *slog.Logger) bool {
	if !metrics {
		return false
	}
	if !recording || rec == nil {
		logger.Warn("observability.usage_metrics is on but usage.events.enabled is off; the usage counters stay empty")
		return false
	}
	c := newUsageCounter(src, rec, leading, usageCounterPageSize, logger)
	gatedtick.Start(ctx, "usage-counters", usageCounterInterval, nil, logger, func() { c.cycle(ctx) })
	return true
}
