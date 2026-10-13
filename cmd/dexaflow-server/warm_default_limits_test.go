package main

import (
	"reflect"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
)

// TestWarmPodSpecFuncAppliesDefaultLimits: without executor.unit, the default
// task limits (#537) size the warm pods too, so a task placed on a warm worker
// runs under the same cap as one on a dedicated pod. Nothing configured leaves
// warm pods unsized, as before.
func TestWarmPodSpecFuncAppliesDefaultLimits(t *testing.T) {
	authn := auth.NewJWTAuthenticator(nil, "secret", time.Hour)
	target := executor.WarmTarget{DagVersionID: "dv-1", Image: "reg/dag:v1"}

	cfg := &config.ServerConfig{}
	spec, err := warmPodSpecFunc(cfg, authn, "cp:9000")(target)
	if err != nil {
		t.Fatalf("warmPodSpecFunc: %v", err)
	}
	if spec.Resources != nil {
		t.Errorf("Resources = %+v with nothing configured; want nil", spec.Resources)
	}

	cfg.Executor.Defaults.LimitsCPU, cfg.Executor.Defaults.LimitsMemory = "2", "4Gi"
	spec, err = warmPodSpecFunc(cfg, authn, "cp:9000")(target)
	if err != nil {
		t.Fatalf("warmPodSpecFunc: %v", err)
	}
	want := &domain.Resources{Limits: &domain.ResourceQuantity{CPU: "2", Memory: "4Gi"}}
	if !reflect.DeepEqual(spec.Resources, want) {
		t.Errorf("Resources = %+v; want %+v", spec.Resources, want)
	}

	// A unit still sizes the warm pod: one unit, as requests and limits.
	cfg.Executor.Unit.CPU, cfg.Executor.Unit.Memory = "250m", "512Mi"
	spec, err = warmPodSpecFunc(cfg, authn, "cp:9000")(target)
	if err != nil {
		t.Fatalf("warmPodSpecFunc: %v", err)
	}
	if spec.Resources == nil || spec.Resources.Limits == nil || spec.Resources.Limits.Memory != "512Mi" {
		t.Errorf("Resources = %+v; want one unit (512Mi)", spec.Resources)
	}
}
