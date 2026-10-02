package main

import (
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
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

// TestWarmPodResources is X4: a warm pod is sized like the dedicated pod of a
// task that declares no resources (the executor.defaults.resources_* L0 default),
// unless the operator sizes warm pods explicitly; each dimension falls back on
// its own, and nothing configured leaves the warm pod unsized as before.
func TestWarmPodResources(t *testing.T) {
	cases := []struct {
		name                string
		defCPU, defMem      string
		warmCPU, warmMem    string
		wantNil             bool
		wantCPU, wantMemory string
	}{
		{name: "nothing configured", wantNil: true},
		{name: "inherits the platform default", defCPU: "250m", defMem: "256Mi", wantCPU: "250m", wantMemory: "256Mi"},
		{name: "warm override wins", defCPU: "250m", defMem: "256Mi", warmCPU: "1", warmMem: "2Gi", wantCPU: "1", wantMemory: "2Gi"},
		{name: "per dimension", defCPU: "250m", defMem: "256Mi", warmMem: "2Gi", wantCPU: "250m", wantMemory: "2Gi"},
	}
	for _, c := range cases {
		cfg := &config.ServerConfig{}
		cfg.Executor.Defaults.ResourcesCPU, cfg.Executor.Defaults.ResourcesMemory = c.defCPU, c.defMem
		cfg.Execution.WarmPodResourcesCPU, cfg.Execution.WarmPodResourcesMemory = c.warmCPU, c.warmMem
		got := warmPodResources(cfg)
		if c.wantNil {
			if got != nil {
				t.Errorf("%s: warmPodResources = %+v, want nil", c.name, got)
			}
			continue
		}
		if got == nil || got.Requests == nil || got.Limits == nil {
			t.Fatalf("%s: warmPodResources = %+v, want requests and limits", c.name, got)
		}
		for _, q := range []*domain.ResourceQuantity{got.Requests, got.Limits} {
			if q.CPU != c.wantCPU || q.Memory != c.wantMemory {
				t.Errorf("%s: quantity = %+v, want cpu %q memory %q", c.name, q, c.wantCPU, c.wantMemory)
			}
		}

		authn := auth.NewJWTAuthenticator(nil, "secret", time.Hour)
		spec, err := warmPodSpecFunc(cfg, authn, "cp:9000")(executor.WarmTarget{DagVersionID: "dv-1", Image: "reg/dag:v1"})
		if err != nil {
			t.Fatalf("warmPodSpecFunc: %v", err)
		}
		if spec.Resources == nil || spec.Resources.Limits.Memory != c.wantMemory {
			t.Errorf("%s: warm pod spec resources = %+v, want the resolved warm resources", c.name, spec.Resources)
		}
	}
}
