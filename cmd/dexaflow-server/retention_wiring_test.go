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
	if startRetention(ctx, config.RetentionSection{Interval: time.Hour, BatchSize: 1000, MaxRowsPerCycle: 1}, nil, func() bool { return true }, nil, discardLog()) {
		t.Fatal("startRetention started a janitor with every class off")
	}
}

// A configured class starts the leader-gated janitor, and the operator's
// settings reach it unchanged.
func TestStartRetentionWhenConfigured(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sec := config.RetentionSection{DagRunsDays: 30, AuditLogDays: 365, DryRun: true, Interval: time.Hour, BatchSize: 500, BatchPause: time.Second, MaxRowsPerCycle: 9}
	if !startRetention(ctx, sec, nil, func() bool { return false }, nil, discardLog()) {
		t.Fatal("startRetention did not start a janitor with a class configured")
	}
	got := retentionConfig(sec)
	if got.DagRunsDays != 30 || got.AuditLogDays != 365 || !got.DryRun || got.BatchSize != 500 || got.BatchPause != time.Second || got.MaxRowsPerCycle != 9 {
		t.Fatalf("retentionConfig = %+v", got)
	}
}
