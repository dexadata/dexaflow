package observability

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// TestUsageRecorder covers ADR 0096 points 4 and 6: the usage counters count
// attempts per tenant and state and slot seconds per tenant, and the lag gauge
// holds the last value set.
func TestUsageRecorder(t *testing.T) {
	// Arrange
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)

	// Act
	m.RecordTaskAttempt("acme", "success", 180)
	m.RecordTaskAttempt("acme", "success", 20)
	m.RecordTaskAttempt("acme", "failed", 0)
	m.SetUsageEventsLag(1500 * time.Millisecond)

	// Assert
	if got := counterTotal(t, reg, "dexaflow_task_attempts_total"); got != 3 {
		t.Errorf("attempts = %v, want 3", got)
	}
	if got := counterTotal(t, reg, "dexaflow_task_slot_seconds_total"); got != 200 {
		t.Errorf("slot seconds = %v, want 200", got)
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		switch f.GetName() {
		case "dexaflow_task_attempts_total":
			if n := len(f.GetMetric()); n != 2 {
				t.Errorf("attempt series = %d, want one per tenant and state", n)
			}
		case "dexaflow_usage_events_lag_seconds":
			if v := f.GetMetric()[0].GetGauge().GetValue(); v != 1.5 {
				t.Errorf("lag gauge = %v, want 1.5", v)
			}
		}
	}
}
