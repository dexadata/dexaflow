package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/executor"
	"github.com/dexadata/dexaflow/internal/observability"
)

// backstopGauge returns dexaflow_resilience_backstop_disabled by config_key.
func backstopGauge(t *testing.T, reg *prometheus.Registry) map[string]float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	for _, mf := range mfs {
		if mf.GetName() != "dexaflow_resilience_backstop_disabled" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "config_key" {
					out[l.GetValue()] = m.GetGauge().GetValue()
				}
			}
		}
	}
	return out
}

// TestResiliencePostureGaugeFollowsTheCeiling pins #941: a disabled credential
// ceiling stays true for the life of the process, so it is exported as a gauge
// set once at boot, 1 while disabled and 0 while set, and "is any replica
// running with the backstop off" is a query, not a log search with an unbounded
// lookback.
func TestResiliencePostureGaugeFollowsTheCeiling(t *testing.T) {
	const key = "auth.max_attempt_credential_lifetime"
	for _, tc := range []struct {
		ceiling time.Duration
		want    float64
	}{{0, 1}, {-time.Minute, 1}, {24 * time.Hour, 0}} {
		reg := prometheus.NewRegistry()
		cfg := &config.ServerConfig{}
		cfg.Auth.MaxAttemptCredentialLifetime = tc.ceiling
		recordResiliencePosture(cfg, observability.NewMetrics(reg))
		if got := backstopGauge(t, reg); len(got) != 1 || got[key] != tc.want {
			t.Errorf("ceiling %v: gauge = %v, want {%s: %v}", tc.ceiling, got, key, tc.want)
		}
	}

	// An api-only replica does not run the backstops, so it exports nothing,
	// like it logs nothing.
	reg := prometheus.NewRegistry()
	cfg := &config.ServerConfig{}
	cfg.Server.Role = "api"
	recordResiliencePosture(cfg, observability.NewMetrics(reg))
	if got := backstopGauge(t, reg); len(got) != 0 {
		t.Errorf("api-only role: gauge = %v, want no series", got)
	}
}

// TestResilienceWarningCarriesAStableEvent pins the other half of #941: the
// WARN names the event, a constant, so a rule keyed on it survives a second
// rung warning about the same key and any reword of the message.
func TestResilienceWarningCarriesAStableEvent(t *testing.T) {
	var buf bytes.Buffer
	cfg := &config.ServerConfig{}
	warnStartup(cfg, slog.New(slog.NewJSONHandler(&buf, nil)))
	if !strings.Contains(buf.String(), `"event":"`+executor.EventResilienceBackstopDisabled+`"`) {
		t.Fatalf("WARN lacks the event attribute: %s", buf.String())
	}
}
