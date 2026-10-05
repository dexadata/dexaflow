package config

import "testing"

// TestDropLegacyMetricNamesDefaultsOff pins the gate's default (ADR 0062): a
// config that never mentions it keeps publishing the leoflow_ metric names.
func TestDropLegacyMetricNamesDefaultsOff(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	if c.Observability.Metrics.DropLegacyNames {
		t.Fatal("observability.metrics.drop_legacy_names defaults to true, want false")
	}
}

// TestDropLegacyMetricNamesBindsBothPrefixes pins that the gate is reachable
// from the env-only Helm path under the current and the legacy prefix.
func TestDropLegacyMetricNamesBindsBothPrefixes(t *testing.T) {
	for _, env := range []string{"DEXAFLOW_OBSERVABILITY_METRICS_DROP_LEGACY_NAMES", "LEOFLOW_OBSERVABILITY_METRICS_DROP_LEGACY_NAMES"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv(env, "true")
			c, err := LoadServer("", nil)
			if err != nil {
				t.Fatalf("LoadServer() error = %v", err)
			}
			if !c.Observability.Metrics.DropLegacyNames {
				t.Fatalf("%s=true did not enable observability.metrics.drop_legacy_names", env)
			}
		})
	}
}
