package config

import (
	"strings"
	"testing"
)

// TestRetentionKeepsUsageEventsThirtyDays covers ADR 0096 point 5: the usage
// events keep a 30 day window by default, and the janitor only starts for it
// when recording is on, so a default install still starts nothing.
func TestRetentionKeepsUsageEventsThirtyDays(t *testing.T) {
	// Arrange
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	r := c.Retention

	// Act
	off, on := r.Active(false), r.Active(true)

	// Assert
	if r.UsageEventsDays != 30 {
		t.Errorf("usage_events_days = %d, want 30", r.UsageEventsDays)
	}
	if off || !on {
		t.Errorf("Active(false) = %v, Active(true) = %v; want the janitor only when recording is on", off, on)
	}
	if err := r.Validate(); err != nil {
		t.Errorf("Validate() on defaults = %v, want nil", err)
	}
}

// TestRetentionUsageEventsWindowIsChecked: a negative window fails boot, 0
// keeps every event, so recording alone no longer starts the janitor, and the
// pacing is checked only when the window has work.
func TestRetentionUsageEventsWindowIsChecked(t *testing.T) {
	// Arrange
	negative := RetentionSection{UsageEventsDays: -1}
	keepAll := RetentionSection{}

	// Act
	err := negative.Validate()

	// Assert
	if err == nil || !strings.Contains(err.Error(), "retention.usage_events_days") {
		t.Fatalf("Validate() = %v, want an error naming retention.usage_events_days", err)
	}
	if keepAll.Active(true) {
		t.Error("Active(true) with usage_events_days 0 = true, want false")
	}
	pacing := RetentionSection{UsageEventsDays: 30}
	if err := pacing.ValidateWith(true); err == nil || !strings.Contains(err.Error(), "retention.batch_size") {
		t.Errorf("ValidateWith(true) with the usage window on and zero pacing = %v, want the pacing checked", err)
	}
	if err := pacing.ValidateWith(false); err != nil {
		t.Errorf("ValidateWith(false) with zero pacing = %v, want nil: an install that does not record starts no janitor", err)
	}
}
