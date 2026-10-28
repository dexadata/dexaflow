package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// fakeUsageSwitch records the values written and answers with the queued
// results.
type fakeUsageSwitch struct {
	writes  []bool
	changed []bool
	errs    []error
	lag     time.Duration
	lagErr  error
	lagRead int
}

func (f *fakeUsageSwitch) EventsLag(context.Context) (time.Duration, error) {
	f.lagRead++
	return f.lag, f.lagErr
}

// fakeLagGauge records what the writer sets.
type fakeLagGauge struct{ set []time.Duration }

func (g *fakeLagGauge) SetUsageEventsLag(d time.Duration) { g.set = append(g.set, d) }

func (f *fakeUsageSwitch) SetEventsEnabled(_ context.Context, enabled bool) (bool, error) {
	i := len(f.writes)
	f.writes = append(f.writes, enabled)
	var changed bool
	var err error
	if i < len(f.changed) {
		changed = f.changed[i]
	}
	if i < len(f.errs) {
		err = f.errs[i]
	}
	return changed, err
}

// TestUsageSwitchWriterLogsWhatItWrote covers ADR 0096 point 7: the leader
// writes its setting on every cycle, says once which value it wrote, logs
// again only when the stored value moved, and warns once per outage.
func TestUsageSwitchWriterLogsWhatItWrote(t *testing.T) {
	// Arrange
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	down := errors.New("connection refused")
	store := &fakeUsageSwitch{
		changed: []bool{true, false, false, false, false, true},
		errs:    []error{nil, nil, down, down, nil, nil},
	}
	w := newUsageSwitchWriter(true, store, nil, logger)

	// Act
	for range 6 {
		w.write(context.Background())
	}

	// Assert
	if len(store.writes) != 6 {
		t.Fatalf("writes = %v, want one per cycle", store.writes)
	}
	for i, v := range store.writes {
		if !v {
			t.Errorf("write %d = false, want the configured true", i)
		}
	}
	out := buf.String()
	if n := strings.Count(out, "usage events switch written"); n != 2 {
		t.Errorf("logged the written switch %d times, want 2 (the first write and the one that changed it):\n%s", n, out)
	}
	if !strings.Contains(out, "enabled=true") {
		t.Errorf("log does not say which value was written:\n%s", out)
	}
	if n := strings.Count(out, "could not write the usage events switch"); n != 1 {
		t.Errorf("warned %d times for one outage, want 1:\n%s", n, out)
	}
}

// TestUsageSwitchWriterExportsTheLag covers ADR 0096 point 4: while recording
// is on, each cycle exports how long the oldest event has been held back; with
// it off the gauge is 0 and the lag is not read; a failed read keeps the last
// value.
func TestUsageSwitchWriterExportsTheLag(t *testing.T) {
	// Arrange
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	onStore := &fakeUsageSwitch{lag: 3 * time.Second}
	offStore := &fakeUsageSwitch{lag: time.Hour}
	failing := &fakeUsageSwitch{lagErr: errors.New("connection refused")}
	on, off, failed := &fakeLagGauge{}, &fakeLagGauge{}, &fakeLagGauge{}

	// Act
	newUsageSwitchWriter(true, onStore, on, logger).write(context.Background())
	newUsageSwitchWriter(false, offStore, off, logger).write(context.Background())
	newUsageSwitchWriter(true, failing, failed, logger).write(context.Background())

	// Assert
	if len(on.set) != 1 || on.set[0] != 3*time.Second {
		t.Errorf("lag set with recording on = %v, want [3s]", on.set)
	}
	if len(off.set) != 1 || off.set[0] != 0 || offStore.lagRead != 0 {
		t.Errorf("lag set with recording off = %v after %d reads, want [0] without reading", off.set, offStore.lagRead)
	}
	if len(failed.set) != 0 {
		t.Errorf("lag set after a failed read = %v, want nothing", failed.set)
	}
}
