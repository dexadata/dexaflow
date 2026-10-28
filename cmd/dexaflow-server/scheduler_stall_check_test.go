package main

import (
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/config"
)

type fakeStallReporter struct {
	stalled bool
	last    time.Time
	limit   time.Duration
}

func (f *fakeStallReporter) Heartbeat() (bool, time.Time) { return !f.stalled, f.last }

func (f *fakeStallReporter) StalledFor(limit time.Duration) (bool, time.Time) {
	f.limit = limit
	return f.stalled, f.last
}

type heartbeatOnly struct{}

func (heartbeatOnly) Heartbeat() (bool, time.Time) { return false, time.Time{} }

// TestSchedulerStallCheckIsOptInAndInProcess is #539: no check unless the
// setting is on and this process runs the scheduler; then the check fails
// exactly while the scheduler reports a stall.
func TestSchedulerStallCheckIsOptInAndInProcess(t *testing.T) {
	cfg := &config.ServerConfig{}
	if schedulerStallCheck(cfg, &fakeStallReporter{stalled: true}) != nil {
		t.Error("a check was built with server.liveness_scheduler_stall at 0")
	}
	cfg.Server.LivenessSchedulerStall = 2 * time.Minute
	if schedulerStallCheck(cfg, nil) != nil {
		t.Error("a check was built for a process that runs no scheduler")
	}
	if schedulerStallCheck(cfg, heartbeatOnly{}) != nil {
		t.Error("a check was built for a scheduler in another process")
	}

	rep := &fakeStallReporter{last: time.Now().Add(-5 * time.Minute)}
	check := schedulerStallCheck(cfg, rep)
	if check == nil {
		t.Fatal("no check built for an in-process scheduler with the setting on")
	}
	if err := check(); err != nil {
		t.Errorf("check failed for a scheduler that is not stalled: %v", err)
	}
	if rep.limit != 2*time.Minute {
		t.Errorf("check asked for a %s stall, want the configured 2m", rep.limit)
	}
	rep.stalled = true
	err := check()
	if err == nil || !strings.Contains(err.Error(), "has not ticked") {
		t.Errorf("check() = %v for a stalled scheduler, want an error naming the stall", err)
	}
}
