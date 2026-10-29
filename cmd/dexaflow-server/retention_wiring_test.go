package main

import (
	"context"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/config"
)

// With no retention class configured nothing is started: the default install
// runs no janitor and deletes nothing.
func TestStartRetentionOffByDefault(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if startRetention(ctx, config.RetentionSection{Interval: time.Hour, BatchSize: 1000, MaxRowsPerCycle: 1}, false, nil, func() bool { return true }, nil, discardLog()) {
		t.Fatal("startRetention started a janitor with every class off")
	}
}

// A configured class starts the leader-gated janitor, and the operator's
// settings reach it unchanged.
func TestStartRetentionWhenConfigured(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sec := config.RetentionSection{DagRunsDays: 30, AuditLogDays: 365, DryRun: true, Interval: time.Hour, BatchSize: 500, BatchPause: time.Second, MaxRowsPerCycle: 9}
	if !startRetention(ctx, sec, false, nil, func() bool { return false }, nil, discardLog()) {
		t.Fatal("startRetention did not start a janitor with a class configured")
	}
	got := retentionConfig(sec)
	if got.DagRunsDays != 30 || got.AuditLogDays != 365 || !got.DryRun || got.BatchSize != 500 || got.BatchPause != time.Second || got.MaxRowsPerCycle != 9 {
		t.Fatalf("retentionConfig = %+v", got)
	}
}

// TestStartRetentionForUsageEvents covers ADR 0096 point 5: the usage events
// window starts the janitor only while recording is on, and reaches it.
func TestStartRetentionForUsageEvents(t *testing.T) {
	// Arrange
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sec := config.RetentionSection{UsageEventsDays: 30, Interval: time.Hour, BatchSize: 1000, MaxRowsPerCycle: 1}
	never := func() bool { return false }

	// Act
	off := startRetention(ctx, sec, false, nil, never, nil, discardLog())
	on := startRetention(ctx, sec, true, nil, never, nil, discardLog())
	got := retentionConfig(sec)

	// Assert
	if off || !on {
		t.Fatalf("started with recording off = %v, on = %v; want only with recording on", off, on)
	}
	if got.UsageEventsDays != 30 {
		t.Errorf("retentionConfig = %+v, want usage_events_days 30", got)
	}
}
