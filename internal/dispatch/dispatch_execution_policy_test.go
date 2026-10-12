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
