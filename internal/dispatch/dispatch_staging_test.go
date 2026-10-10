package dispatch

import (
	"context"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
)

// TestDispatchWiresStagingVolume asserts the per-run staging volume (ADR 0022)
// is wired into the executor request when the DAG opts in: a deterministic claim
// name (so clear+re-run re-attaches it) plus the pinned size/class/access mode.
func TestDispatchWiresStagingVolume(t *testing.T) {
	res := &fakeResolver{resolved: Resolved{
		TaskInstanceID: "ti-1", TenantID: "acme",
		Staging: &domain.StagingConfig{Enabled: true, Size: "5Gi", StorageClass: "fast"},
	}}
	exec := &fakeExecutor{}
	d := newDispatcher(res, &fakeIssuer{token: "t"}, exec)
	d.SetPlatformDefaults(PlatformDefaults{StagingAccessMode: "ReadWriteMany"})

	if _, err := d.Dispatch(context.Background(), "run-1", "etl", "", pythonTask()); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if want := executor.StagingClaimName("etl", "run-1"); exec.req.StagingClaim != want {
		t.Errorf("staging claim = %q, want %q", exec.req.StagingClaim, want)
	}
	if exec.req.StagingSize != "5Gi" || exec.req.StagingStorageClass != "fast" || exec.req.StagingAccessMode != "ReadWriteMany" {
		t.Errorf("staging fields not wired: %+v", exec.req)
	}
}

// TestDispatchStagingDefaultsFillFromPlatform: staging enabled but size/class
// unset → the per-cluster platform defaults fill them (ADR 0022/0023).
func TestDispatchStagingDefaultsFillFromPlatform(t *testing.T) {
	res := &fakeResolver{resolved: Resolved{Staging: &domain.StagingConfig{Enabled: true}}}
	exec := &fakeExecutor{}
	d := newDispatcher(res, &fakeIssuer{token: "t"}, exec)
	d.SetPlatformDefaults(PlatformDefaults{StagingSize: "10Gi", StagingStorageClass: "std", StagingAccessMode: "ReadWriteOnce"})

	if _, err := d.Dispatch(context.Background(), "run-1", "etl", "", pythonTask()); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if exec.req.StagingSize != "10Gi" || exec.req.StagingStorageClass != "std" {
		t.Errorf("platform defaults should fill staging size/class, got %+v", exec.req)
	}
}

// TestDispatchDisabledStagingLeavesNoClaim: a DAG without staging must not get a
// claim — the volume is strictly opt-in.
func TestDispatchDisabledStagingLeavesNoClaim(t *testing.T) {
	res := &fakeResolver{resolved: Resolved{Staging: &domain.StagingConfig{Enabled: false}}}
	exec := &fakeExecutor{}
	if _, err := newDispatcher(res, &fakeIssuer{token: "t"}, exec).Dispatch(context.Background(), "r", "etl", "", pythonTask()); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if exec.req.StagingClaim != "" {
		t.Errorf("disabled staging must not set a claim, got %q", exec.req.StagingClaim)
	}
}

// TestDispatchSetsAgentTLSCAConfigMap: the configured agent TLS CA configmap is
// propagated to the executor request.
func TestDispatchSetsAgentTLSCAConfigMap(t *testing.T) {
	exec := &fakeExecutor{}
	d := newDispatcher(&fakeResolver{}, &fakeIssuer{token: "t"}, exec)
	d.SetAgentTLSCAConfigMap("agent-ca")
	if _, err := d.Dispatch(context.Background(), "r", "etl", "", pythonTask()); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if exec.req.AgentTLSCAConfigMap != "agent-ca" {
		t.Errorf("agent TLS CA configmap = %q, want agent-ca", exec.req.AgentTLSCAConfigMap)
	}
}

// TestDispatchSkipsWarmForStagingRun pins ADR 0058 D5: a staging attempt is
// never offered to a warm worker (which carries no per-run /staging mount). Even
// with a placer that WOULD accept, the staging run must take the dedicated path
// and get its StagingClaim.
func TestDispatchSkipsWarmForStagingRun(t *testing.T) {
	res := &fakeResolver{resolved: Resolved{
		TaskInstanceID: "ti-1", TenantID: "acme", Image: "etl:v1",
		Staging: &domain.StagingConfig{Enabled: true},
	}}
	exec := &fakeExecutor{}
	placer := &fakePlacer{ok: true} // would place if it were ever offered
	d := newDispatcher(res, &fakeIssuer{token: "t"}, exec)
	d.SetWarmPlacer(placer)

	disp, err := d.Dispatch(context.Background(), "run-1", "etl", "ver-7", pythonTask())
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if placer.calls != 0 {
		t.Errorf("staging attempt offered to the warm placer %d times, want 0 (ADR 0058 D5)", placer.calls)
	}
	if disp != executor.Dispatched {
		t.Errorf("disposition = %v, want Dispatched via the dedicated path", disp)
	}
	if want := executor.StagingClaimName("etl", "run-1"); exec.req.StagingClaim != want {
		t.Errorf("dedicated staging claim = %q, want %q", exec.req.StagingClaim, want)
	}
}
