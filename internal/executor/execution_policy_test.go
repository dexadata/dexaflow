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
