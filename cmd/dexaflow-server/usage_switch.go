package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/dexadata/dexaflow/internal/gatedtick"
)

// usageSwitchInterval is how often the leader rewrites the usage event switch.
// A write that finds the same value changes nothing, so the cost is one
// single-row UPDATE; the interval bounds how long a new leader with another
// setting takes to apply it.
const usageSwitchInterval = 10 * time.Second

// usageSwitch is the store the leader writes usage.events.enabled to.
type usageSwitch interface {
	SetEventsEnabled(ctx context.Context, enabled bool) (bool, error)
}

// usageSwitchWriter writes this replica's usage.events.enabled to the row the
// usage event trigger reads (ADR 0096 point 7). Only the leader writes, so its
// setting wins when replicas disagree.
type usageSwitchWriter struct {
	enabled bool
	store   usageSwitch
	logger  *slog.Logger
	written bool
	failing bool
}

func newUsageSwitchWriter(enabled bool, store usageSwitch, logger *slog.Logger) *usageSwitchWriter {
	return &usageSwitchWriter{enabled: enabled, store: store, logger: logger}
}

// write stores the setting. It logs the first write of this process and every
// write that moved the stored value, and warns once per run of failures.
func (w *usageSwitchWriter) write(ctx context.Context) {
	changed, err := w.store.SetEventsEnabled(ctx, w.enabled)
	if err != nil {
		if !w.failing {
			w.logger.Warn("could not write the usage events switch; recording keeps its previous setting", "enabled", w.enabled, "error", err)
		}
		w.failing = true
		return
	}
	w.failing = false
	if changed || !w.written {
		w.logger.Info("usage events switch written", "enabled", w.enabled, "changed", changed)
	}
	w.written = true
}

// startUsageSwitch writes the setting every usageSwitchInterval while this
// replica leads, so a new leader applies its own within one interval.
func startUsageSwitch(ctx context.Context, enabled bool, store usageSwitch, leading func() bool, logger *slog.Logger) {
	w := newUsageSwitchWriter(enabled, store, logger)
	gatedtick.Start(ctx, "usage-switch", usageSwitchInterval, leading, logger, func() { w.write(ctx) })
}
