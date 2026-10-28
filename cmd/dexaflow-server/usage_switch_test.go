package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// fakeUsageSwitch records the values written and answers with the queued
// results.
type fakeUsageSwitch struct {
	writes  []bool
	changed []bool
	errs    []error
}

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
	w := newUsageSwitchWriter(true, store, logger)

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
