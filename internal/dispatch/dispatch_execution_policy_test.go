package dispatch

import (
	"context"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
)

// The operator execution allowlist (issue #629) is enforced at dispatch for a
// DAG registered before the list changed: the task is Refused (fails once, no
// retries) before a token is minted or a warm worker is offered the attempt.

func policyDispatcher(t *testing.T, c domain.ExecutionPolicyConfig) (*Dispatcher, *fakeExecutor, *fakeIssuer, *fakePlacer) {
	t.Helper()
	policy, err := domain.ParseExecutionPolicy(c)
	if err != nil {
		t.Fatal(err)
	}
	exec := &fakeExecutor{}
	iss := &fakeIssuer{token: "t"}
	placer := &fakePlacer{ok: true}
	d := newDispatcher(&fakeResolver{resolved: Resolved{TaskInstanceID: "ti", Image: "etl:v1"}}, iss, exec)
	d.SetWarmPlacer(placer)
	d.SetPlatformDefaults(PlatformDefaults{ExecutionPolicy: policy})
	return d, exec, iss, placer
}

func TestDispatchRefusesAFieldOutsideTheExecutionAllowlist(t *testing.T) {
	d, exec, iss, placer := policyDispatcher(t, domain.ExecutionPolicyConfig{Fields: []string{"tolerations"}})
	task := domain.TaskSpec{TaskID: "t", Type: domain.TaskTypePython, Entrypoint: "dag:t",
		Execution: &domain.Execution{PriorityClassName: "system-cluster-critical"}}
	disp, err := d.Dispatch(context.Background(), "run", "etl", "ver", task)
	if err == nil || disp != executor.Refused {
		t.Fatalf("Dispatch = %v, %v; want Refused", disp, err)
	}
	if !strings.Contains(err.Error(), "execution.priority_class_name") {
		t.Errorf("error %q should name the field", err)
	}
	if exec.req.TaskID != "" || placer.calls != 0 || iss.id.TaskID != "" {
		t.Errorf("refused task reached the executor (%v), a warm worker (%d) or the issuer (%q)", exec.req.TaskID, placer.calls, iss.id.TaskID)
	}
}

// A task within the allowlist dispatches, and the policy rides on the request
// so the executor can enforce it as well.
func TestDispatchPassesTheExecutionPolicyToTheExecutor(t *testing.T) {
	d, exec, _, placer := policyDispatcher(t, domain.ExecutionPolicyConfig{PriorityClasses: []string{"etl"}})
	placer.ok = false
	task := domain.TaskSpec{TaskID: "t", Type: domain.TaskTypePython, Entrypoint: "dag:t",
		Execution: &domain.Execution{PriorityClassName: "etl"}}
	disp, err := d.Dispatch(context.Background(), "run", "etl", "ver", task)
	if err != nil || disp != executor.Dispatched {
		t.Fatalf("Dispatch = %v, %v; want Dispatched", disp, err)
	}
	if exec.req.ExecutionPolicy == nil {
		t.Error("request carries no execution policy")
	}
}

// The image and service_account allowlists (issue #537 C2) are enforced at
// dispatch too: the image is the one the run resolved, the service account the
// one the task names (the operator's default is never checked).
func TestDispatchRefusesAnImageOrServiceAccountOutsideTheAllowlist(t *testing.T) {
	d, exec, _, placer := policyDispatcher(t, domain.ExecutionPolicyConfig{
		Images: []string{"registry.example.com/*"}, ServiceAccounts: []string{"etl"},
	})
	task := domain.TaskSpec{TaskID: "t", Type: domain.TaskTypePython, Entrypoint: "dag:t"}
	disp, err := d.Dispatch(context.Background(), "run", "etl", "ver", task)
	if err == nil || disp != executor.Refused || !strings.Contains(err.Error(), "etl:v1") {
		t.Fatalf("Dispatch = %v, %v; want Refused naming image etl:v1", disp, err)
	}
	if exec.req.TaskID != "" || placer.calls != 0 {
		t.Errorf("refused task reached the executor or a warm worker")
	}

	d, exec, _, placer = policyDispatcher(t, domain.ExecutionPolicyConfig{ServiceAccounts: []string{"etl"}})
	placer.ok = false
	d.SetDefaultTaskServiceAccount("operator-default")
	if disp, err := d.Dispatch(context.Background(), "run", "etl", "ver", task); err != nil || disp != executor.Dispatched {
		t.Fatalf("task without a service account = %v, %v; want Dispatched under the operator default", disp, err)
	}
	if exec.req.Execution.ServiceAccount != "operator-default" {
		t.Errorf("service account = %q, want the operator default", exec.req.Execution.ServiceAccount)
	}
	task.Execution = &domain.Execution{ServiceAccount: "admin"}
	if disp, err := d.Dispatch(context.Background(), "run", "etl", "ver", task); err == nil || disp != executor.Refused {
		t.Fatalf("unlisted service account = %v, %v; want Refused", disp, err)
	}
}
