package dispatch

import (
	"context"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
	"github.com/dexadata/dexaflow/pkg/admission"
)

// The executor policy (ADR 0063) runs after the admission hooks (ADR 0100 §7)
// and forces its fields on every attempt.

func TestForcePolicyReachesThePodRequest(t *testing.T) {
	d, exec, _, _ := admissionDispatcher(t, AdmissionOptions{})
	d.SetDefaultTaskServiceAccount("default-sa")
	d.SetForcePolicy(mustForcePolicy(t, "force:\n  runtime_class_name: gvisor\n  service_account: sandboxed\n  node_selector:\n    pool: sandbox\n"))
	task := pythonTask()
	task.Execution = &domain.Execution{Labels: map[string]string{"team": "data"}}
	if disp, err := d.Dispatch(context.Background(), "run", "etl", "ver", task); err != nil || disp != executor.Dispatched {
		t.Fatalf("Dispatch = %v, %v", disp, err)
	}
	e := exec.req.Execution
	if e.RuntimeClassName == nil || *e.RuntimeClassName != "gvisor" || e.ServiceAccount != "sandboxed" || e.NodeSelector["pool"] != "sandbox" {
		t.Fatalf("forced fields missing from the pod request: %+v", e)
	}
	if e.Labels["team"] != "data" {
		t.Errorf("the task's own labels were lost: %+v", e.Labels)
	}
	a := exec.req.AuthoredExecution
	if a == nil {
		t.Fatal("a forced attempt must carry the task's own settings for the allowlist re-check")
	}
	if a.RuntimeClassName != nil || a.ServiceAccount != "" || len(a.NodeSelector) != 0 || a.Labels["team"] != "data" {
		t.Errorf("AuthoredExecution = %+v; want the task's own settings only", a)
	}
	if task.Execution.RuntimeClassName != nil || task.Execution.NodeSelector != nil {
		t.Error("the policy wrote through the task")
	}
}

func TestForcePolicyOverridesAnAdmissionHook(t *testing.T) {
	hook := admit(admission.AdmitWith(admission.Changes{
		ServiceAccount: "hook-sa",
		NodeSelector:   map[string]string{"pool": "hook", "zone": "b"},
	}))
	d, exec, _, _ := admissionDispatcher(t, AdmissionOptions{}, hook)
	d.SetForcePolicy(mustForcePolicy(t, "force:\n  service_account: sandboxed\n  node_selector:\n    pool: sandbox\n"))
	if _, err := d.Dispatch(context.Background(), "run", "etl", "ver", pythonTask()); err != nil {
		t.Fatal(err)
	}
	e := exec.req.Execution
	if e.ServiceAccount != "sandboxed" || e.NodeSelector["pool"] != "sandbox" || e.NodeSelector["zone"] != "b" {
		t.Fatalf("a hook undid the policy, or its own keys were lost: %+v", e)
	}
	if a := exec.req.AuthoredExecution; a == nil || a.ServiceAccount != "" || len(a.NodeSelector) != 0 {
		t.Errorf("AuthoredExecution = %+v; want the task's own (empty) settings", a)
	}
}

func TestForcePolicyKeepsAttemptsOffWarmWorkers(t *testing.T) {
	d, exec, _, _ := admissionDispatcher(t, AdmissionOptions{})
	placer := &fakePlacer{ok: true}
	d.SetWarmPlacer(placer)
	d.SetForcePolicy(mustForcePolicy(t, "force:\n  runtime_class_name: gvisor\n"))
	if _, err := d.Dispatch(context.Background(), "run", "etl", "ver", pythonTask()); err != nil {
		t.Fatal(err)
	}
	if placer.calls != 0 {
		t.Fatal("a forced runtime class must not run on a warm worker, which does not have it")
	}
	if exec.req.TaskInstanceID == "" {
		t.Fatal("the attempt never reached the dedicated pod path")
	}
}

func TestForcePolicyPassesTheAllowlistReCheck(t *testing.T) {
	// fields: none forbids the DAG from setting runtime_class_name; the
	// operator forcing it is not the DAG setting it.
	policy, err := domain.ParseExecutionPolicy(domain.ExecutionPolicyConfig{Fields: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	d, exec, _, _ := admissionDispatcher(t, AdmissionOptions{})
	d.SetPlatformDefaults(PlatformDefaults{ExecutionPolicy: policy})
	d.SetForcePolicy(mustForcePolicy(t, "force:\n  runtime_class_name: gvisor\n"))
	if disp, err := d.Dispatch(context.Background(), "run", "etl", "ver", pythonTask()); err != nil || disp != executor.Dispatched {
		t.Fatalf("Dispatch = %v, %v", disp, err)
	}
	a := exec.req.AuthoredExecution
	if a == nil {
		t.Fatal("AuthoredExecution missing")
	}
	if err := policy.CheckTask(domain.TaskSpec{TaskID: "extract", Execution: a}); err != nil {
		t.Fatalf("the executor's re-check would refuse a forced field: %v", err)
	}
	if err := policy.CheckTask(domain.TaskSpec{TaskID: "extract", Execution: &exec.req.Execution}); err == nil {
		t.Fatal("sanity: the forced execution does carry a governed field")
	}
}

func TestNoForcePolicyLeavesDispatchAlone(t *testing.T) {
	d, exec, _, _ := admissionDispatcher(t, AdmissionOptions{})
	if _, err := d.Dispatch(context.Background(), "run", "etl", "ver", pythonTask()); err != nil {
		t.Fatal(err)
	}
	if exec.req.AuthoredExecution != nil || exec.req.Execution.RuntimeClassName != nil {
		t.Fatalf("no policy must leave the request as it was: %+v", exec.req)
	}
}
