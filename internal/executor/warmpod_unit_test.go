package executor

import (
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

// With an operator resource unit a warm pod is one unit (ADR 0066 §3): the
// spec's Resources become the container's requests and limits. Without them
// the container is left unsized, as before.
func TestBuildWarmPodAppliesResources(t *testing.T) {
	spec := baseWarmSpec()
	spec.Resources = &domain.Resources{
		Requests: &domain.ResourceQuantity{CPU: "250m", Memory: "512Mi"},
		Limits:   &domain.ResourceQuantity{CPU: "250m", Memory: "512Mi"},
	}
	r := BuildWarmPod(spec).Spec.Containers[0].Resources
	if got := r.Limits.Cpu().String(); got != "250m" {
		t.Errorf("cpu limit = %s, want 250m", got)
	}
	if got := r.Requests.Memory().String(); got != "512Mi" {
		t.Errorf("memory request = %s, want 512Mi", got)
	}

	bare := BuildWarmPod(baseWarmSpec()).Spec.Containers[0].Resources
	if len(bare.Requests) != 0 || len(bare.Limits) != 0 {
		t.Errorf("unsized warm pod got resources %+v, want none", bare)
	}
}
