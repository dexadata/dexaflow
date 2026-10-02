package main

import (
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/executor"
)

// TestWarmPodSpecFuncCarriesSelfLifecycleCaps locks the wiring (ADR 0058
// D9/D10/D6/H3): warmPodSpecFunc copies the operator's execution.* caps onto the
// warm-pod spec, and anchors the per-attempt watchdog to
// auth.max_attempt_credential_lifetime — an attempt can never validly outlive its
// credential ceiling, so that is the hard upper bound with no separate knob.
func TestWarmPodSpecFuncCarriesSelfLifecycleCaps(t *testing.T) {
	cfg := &config.ServerConfig{}
	cfg.Execution.MaxAttemptsPerWorker = 50
	cfg.Execution.MaxWorkerLifetime = time.Hour
	cfg.Execution.WorkerIdleTTL = 5 * time.Minute
	cfg.Auth.MaxAttemptCredentialLifetime = 24 * time.Hour

	authn := auth.NewJWTAuthenticator(nil, "secret", time.Hour)
	spec, err := warmPodSpecFunc(cfg, authn, "cp:9000")(executor.WarmTarget{DagVersionID: "dv-1", Image: "reg/dag:v1"})
	if err != nil {
		t.Fatalf("warmPodSpecFunc: %v", err)
	}

	if spec.MaxAttemptsPerWorker != 50 {
		t.Errorf("MaxAttemptsPerWorker = %d, want 50", spec.MaxAttemptsPerWorker)
	}
	if spec.MaxWorkerLifetimeSeconds != 3600 {
		t.Errorf("MaxWorkerLifetimeSeconds = %d, want 3600", spec.MaxWorkerLifetimeSeconds)
	}
	if spec.WorkerIdleTTLSeconds != 300 {
		t.Errorf("WorkerIdleTTLSeconds = %d, want 300", spec.WorkerIdleTTLSeconds)
	}
	// The watchdog is the credential ceiling (24h), NOT a bespoke knob.
	if spec.AttemptWatchdogSeconds != 86400 {
		t.Errorf("AttemptWatchdogSeconds = %d, want 86400 (= max_attempt_credential_lifetime)", spec.AttemptWatchdogSeconds)
	}
}
