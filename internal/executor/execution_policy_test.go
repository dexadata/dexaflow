package executor

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/dexadata/dexaflow/internal/domain"
)

// The Kubernetes executor enforces the operator execution allowlist (issue
// #629) itself before it builds the pod: a request that breaks it is Refused
// and no pod is created.
func TestKubernetesExecuteRefusesARequestOutsideTheExecutionAllowlist(t *testing.T) {
	policy, err := domain.ParseExecutionPolicy(domain.ExecutionPolicyConfig{Fields: []string{"none"}})
	if err != nil {
		t.Fatal(err)
	}
	cs := fake.NewClientset()
	e := NewKubernetesExecutor(cs, "leoflow")
	req := sampleReq()
	req.Execution.Affinity = map[string]any{"nodeAffinity": map[string]any{}}
	req.ExecutionPolicy = policy
	disp, err := e.Execute(context.Background(), req)
	if err == nil || disp != Refused {
		t.Fatalf("Execute = %v, %v; want Refused", disp, err)
	}
	if !strings.Contains(err.Error(), "execution.affinity") {
		t.Errorf("error %q should name the field", err)
	}
	pods, _ := cs.CoreV1().Pods("leoflow").List(context.Background(), metav1.ListOptions{})
	if len(pods.Items) != 0 {
		t.Errorf("a refused request created %d pod(s)", len(pods.Items))
	}
}

// The service account on a request may be the operator's default, filled in by
// the dispatcher (and named on DefaultServiceAccount) after it checked the
// task's own; the executor must not refuse it (issue #537 C2).
func TestKubernetesExecuteDoesNotRecheckTheFilledInServiceAccount(t *testing.T) {
	policy, err := domain.ParseExecutionPolicy(domain.ExecutionPolicyConfig{ServiceAccounts: []string{"etl"}})
	if err != nil {
		t.Fatal(err)
	}
	e := NewKubernetesExecutor(fake.NewClientset(), "leoflow")
	req := sampleReq()
	req.Execution.ServiceAccount = "operator-default"
	req.DefaultServiceAccount = "operator-default"
	req.ExecutionPolicy = policy
	if disp, err := e.Execute(context.Background(), req); err != nil || disp != Dispatched {
		t.Fatalf("Execute = %v, %v; want Dispatched", disp, err)
	}
}

// Any other service account on the request is checked again, so a Request
// builder that skipped the dispatcher's check cannot run a pod under an
// account outside the allowlist (issue #537 C2).
func TestKubernetesExecuteRefusesAServiceAccountOutsideTheAllowlist(t *testing.T) {
	policy, err := domain.ParseExecutionPolicy(domain.ExecutionPolicyConfig{ServiceAccounts: []string{"etl"}})
	if err != nil {
		t.Fatal(err)
	}
	cs := fake.NewClientset()
	e := NewKubernetesExecutor(cs, "leoflow")
	req := sampleReq()
	req.Execution.ServiceAccount = "cluster-admin-ish"
	req.DefaultServiceAccount = "operator-default"
	req.ExecutionPolicy = policy
	disp, err := e.Execute(context.Background(), req)
	if err == nil || disp != Refused || !strings.Contains(err.Error(), "cluster-admin-ish") {
		t.Fatalf("Execute = %v, %v; want Refused naming the service account", disp, err)
	}
	pods, _ := cs.CoreV1().Pods("leoflow").List(context.Background(), metav1.ListOptions{})
	if len(pods.Items) != 0 {
		t.Errorf("a refused request created %d pod(s)", len(pods.Items))
	}
}

// The image allowlist (issue #537 C2) is enforced on the request's image.
func TestKubernetesExecuteRefusesAnImageOutsideTheAllowlist(t *testing.T) {
	policy, err := domain.ParseExecutionPolicy(domain.ExecutionPolicyConfig{Images: []string{"registry.example.com/*"}})
	if err != nil {
		t.Fatal(err)
	}
	cs := fake.NewClientset()
	e := NewKubernetesExecutor(cs, "leoflow")
	req := sampleReq()
	req.Image = "docker.io/someone/miner:latest"
	req.ExecutionPolicy = policy
	disp, err := e.Execute(context.Background(), req)
	if err == nil || disp != Refused || !strings.Contains(err.Error(), "miner") {
		t.Fatalf("Execute = %v, %v; want Refused naming the image", disp, err)
	}
	pods, _ := cs.CoreV1().Pods("leoflow").List(context.Background(), metav1.ListOptions{})
	if len(pods.Items) != 0 {
		t.Errorf("a refused request created %d pod(s)", len(pods.Items))
	}
}
