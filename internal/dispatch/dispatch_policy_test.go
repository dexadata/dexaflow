package dispatch

import (
	"context"
	"errors"
	"testing"

	"github.com/dexadata/dexaflow/internal/executor"
)

func policyOf(t *testing.T, doc string) *executor.Policy {
	t.Helper()
	p, err := executor.ParsePolicy([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestDispatchRefusesWhatThePolicyRefuses: a refused task is reported as
// Refused (permanent, no dispatch retries) and reaches neither a warm worker
// nor the executor.
func TestDispatchRefusesWhatThePolicyRefuses(t *testing.T) {
	placer := &fakePlacer{ok: true}
	exec := &fakeExecutor{}
	d := newDispatcher(&fakeResolver{resolved: Resolved{TaskInstanceID: "ti", Image: "evil.io/x:1"}}, &fakeIssuer{token: "t"}, exec)
	d.SetWarmPlacer(placer)
	d.SetExecutorPolicy(policyOf(t, "images:\n  allowed: [registry.example.com/]\n"))

	disp, err := d.Dispatch(context.Background(), "run", "etl", "ver-1", pythonTask())

	if disp != executor.Refused || !errors.Is(err, executor.ErrPolicyRefused) {
		t.Fatalf("Dispatch = %v, %v; want Refused, ErrPolicyRefused", disp, err)
	}
	if placer.calls != 0 || exec.req.TaskInstanceID != "" {
		t.Errorf("a refused task reached the placer (%d calls) or the executor (%+v)", placer.calls, exec.req)
	}
}

// TestDispatchAppliesPolicyAfterPlatformDefaults: the default ServiceAccount
// and the L0 resources are held to the policy too, and force rules reach the
// executor request.
func TestDispatchAppliesPolicyAfterPlatformDefaults(t *testing.T) {
	exec := &fakeExecutor{}
	d := newDispatcher(&fakeResolver{resolved: Resolved{TaskInstanceID: "ti", Image: "etl:v1"}}, &fakeIssuer{token: "t"}, exec)
	d.SetDefaultTaskServiceAccount("leoflow-task")
	d.SetExecutorPolicy(policyOf(t, "runtime_class_name: gvisor\nservice_account:\n  allowed: [leoflow-task]\nresources:\n  max: {memory: 1Gi}\n"))
	task := pythonTask()
	task.Resources = nil

	if _, err := d.Dispatch(context.Background(), "run", "etl", "", task); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	ex := exec.req.Execution
	if ex.RuntimeClassName == nil || *ex.RuntimeClassName != "gvisor" || ex.ServiceAccount != "leoflow-task" {
		t.Errorf("execution = %+v, want gvisor and the default SA", ex)
	}
	if l := exec.req.Resources.Limits; l == nil || l.Memory != "1Gi" {
		t.Errorf("limits = %+v, want the memory ceiling filled in", l)
	}
}

// TestDispatchKeepsForcedPolicyOffWarmWorkers: warm workers do not carry the
// forced runtime class or placement yet, so such a policy sends every task to
// a dedicated pod.
func TestDispatchKeepsForcedPolicyOffWarmWorkers(t *testing.T) {
	placer := &fakePlacer{ok: true}
	exec := &fakeExecutor{}
	d := newDispatcher(&fakeResolver{resolved: Resolved{TaskInstanceID: "ti", Image: "etl:v1"}}, &fakeIssuer{token: "t"}, exec)
	d.SetWarmPlacer(placer)
	d.SetExecutorPolicy(policyOf(t, "runtime_class_name: gvisor\n"))

	if _, err := d.Dispatch(context.Background(), "run", "etl", "ver-1", pythonTask()); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if placer.calls != 0 || exec.req.TaskInstanceID != "ti" {
		t.Errorf("placer calls = %d, executor got %q; want the dedicated path", placer.calls, exec.req.TaskInstanceID)
	}
}

// TestDispatchRestrictOnlyPolicyKeepsWarmPlacement: a policy with only
// restrict rules checks the task and then still uses a warm worker.
func TestDispatchRestrictOnlyPolicyKeepsWarmPlacement(t *testing.T) {
	placer := &fakePlacer{ok: true}
	d := newDispatcher(&fakeResolver{resolved: Resolved{TaskInstanceID: "ti", Image: "registry.example.com/etl:1"}}, &fakeIssuer{token: "t"}, &fakeExecutor{})
	d.SetWarmPlacer(placer)
	d.SetExecutorPolicy(policyOf(t, "images:\n  allowed: [registry.example.com/]\n"))

	if disp, err := d.Dispatch(context.Background(), "run", "etl", "ver-1", pythonTask()); err != nil || disp != executor.Dispatched {
		t.Fatalf("Dispatch = %v, %v", disp, err)
	}
	if placer.calls != 1 {
		t.Errorf("placer calls = %d, want 1", placer.calls)
	}
}
