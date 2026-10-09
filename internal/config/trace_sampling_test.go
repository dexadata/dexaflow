package config

import "testing"

// TestTraceSamplingDefaultsKeepEverySpan pins the gate's default (ADR 0062):
// every request is traced, probes and static assets included, exactly as
// before.
func TestTraceSamplingDefaultsKeepEverySpan(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	if c.Observability.OTel.SampleRatio != 1 {
		t.Errorf("sample_ratio = %v, want 1", c.Observability.OTel.SampleRatio)
	}
	if c.Observability.OTel.SkipProbeSpans {
		t.Error("skip_probe_spans = true, want false")
	}
}

// TestTraceSamplingBindsBothPrefixes pins the env-only Helm path under the
// current and the legacy prefix.
func TestTraceSamplingBindsBothPrefixes(t *testing.T) {
	for _, prefix := range []string{"DEXAFLOW_", "LEOFLOW_"} {
		t.Run(prefix, func(t *testing.T) {
			t.Setenv(prefix+"OBSERVABILITY_OTEL_SAMPLE_RATIO", "0.25")
			t.Setenv(prefix+"OBSERVABILITY_OTEL_SKIP_PROBE_SPANS", "true")
			c, err := LoadServer("", nil)
			if err != nil {
				t.Fatalf("LoadServer() error = %v", err)
			}
			if c.Observability.OTel.SampleRatio != 0.25 || !c.Observability.OTel.SkipProbeSpans {
				t.Fatalf("sample_ratio=%v skip_probe_spans=%v, want 0.25 and true",
					c.Observability.OTel.SampleRatio, c.Observability.OTel.SkipProbeSpans)
			}
		})
	}
}
