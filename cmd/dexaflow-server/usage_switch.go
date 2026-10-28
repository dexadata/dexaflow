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

// usageSwitch is the store the leader writes usage.events.enabled to, and
// reads the usage events lag from.
type usageSwitch interface {
	SetEventsEnabled(ctx context.Context, enabled bool) (bool, error)
	EventsLag(ctx context.Context) (time.Duration, error)
}

// usageLagGauge receives dexaflow_usage_events_lag_seconds.
type usageLagGauge interface {
	SetUsageEventsLag(d time.Duration)
}

// usageSwitchWriter writes this replica's usage.events.enabled to the row the
// usage event trigger reads (ADR 0096 point 7). Only the leader writes, so its
// setting wins when replicas disagree. Each write also exports the lag of the
// events the service API cannot serve yet.
type usageSwitchWriter struct {
	enabled bool
	store   usageSwitch
	lag     usageLagGauge
	logger  *slog.Logger
	written bool
	failing bool
	// lagFailing is set while lag reads fail, so an outage warns once.
	lagFailing bool
}

// newUsageSwitchWriter builds the writer; a nil lag drops the gauge.
func newUsageSwitchWriter(enabled bool, store usageSwitch, lag usageLagGauge, logger *slog.Logger) *usageSwitchWriter {
	return &usageSwitchWriter{enabled: enabled, store: store, lag: lag, logger: logger}
}

// write stores the setting and exports the lag. It logs the first write of
// this process and every write that moved the stored value, and warns once
// per run of failures.
func (w *usageSwitchWriter) write(ctx context.Context) {
	w.writeSwitch(ctx)
	w.exportLag(ctx)
}

func (w *usageSwitchWriter) writeSwitch(ctx context.Context) {
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

// exportLag sets the lag gauge: 0 while recording is off, and otherwise the
// age of the oldest event held back by an older open transaction. A failed
// read keeps the last value and warns once per run of failures.
func (w *usageSwitchWriter) exportLag(ctx context.Context) {
	if w.lag == nil {
		return
	}
	if !w.enabled {
		w.lag.SetUsageEventsLag(0)
		return
	}
	d, err := w.store.EventsLag(ctx)
	if err != nil {
		if !w.lagFailing {
			w.logger.Warn("could not read the usage events lag; dexaflow_usage_events_lag_seconds keeps its last value", "error", err)
		}
		w.lagFailing = true
		return
	}
	w.lagFailing = false
	w.lag.SetUsageEventsLag(d)
}

// startUsageSwitch writes the setting every usageSwitchInterval while this
// replica leads, so a new leader applies its own within one interval.
func startUsageSwitch(ctx context.Context, enabled bool, store usageSwitch, lag usageLagGauge, leading func() bool, logger *slog.Logger) {
	w := newUsageSwitchWriter(enabled, store, lag, logger)
	gatedtick.Start(ctx, "usage-switch", usageSwitchInterval, leading, logger, func() { w.write(ctx) })
}
