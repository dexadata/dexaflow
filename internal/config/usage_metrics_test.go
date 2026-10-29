package config

import "testing"

// TestUsageMetricsAreOffByDefault covers ADR 0096 point 6: the per-tenant
// usage counters have one series per tenant, so they are off unless asked for.
func TestUsageMetricsAreOffByDefault(t *testing.T) {
	// Arrange
	t.Setenv("DEXAFLOW_OBSERVABILITY_USAGE_METRICS", "")

	// Act
	c, err := LoadServer("", nil)

	// Assert
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if c.Observability.UsageMetrics {
		t.Error("observability.usage_metrics = true by default, want false")
	}
}

// TestUsageMetricsEnvTurnsThemOn: the environment reaches the key.
func TestUsageMetricsEnvTurnsThemOn(t *testing.T) {
	// Arrange
	t.Setenv("DEXAFLOW_OBSERVABILITY_USAGE_METRICS", "true")

	// Act
	c, err := LoadServer("", nil)

	// Assert
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if !c.Observability.UsageMetrics {
		t.Error("observability.usage_metrics = false with the variable set, want true")
	}
}
