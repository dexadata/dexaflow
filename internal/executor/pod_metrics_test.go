package executor

import (
	"context"
	"errors"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

type podCreation struct {
	dagID, result string
	took          time.Duration
}

type podMetricsSpy struct{ seen []podCreation }

func (s *podMetricsSpy) ObservePodCreation(dagID, result string, d time.Duration) {
	s.seen = append(s.seen, podCreation{dagID, result, d})
}

// Every task pod create is counted by dag and result and its API latency is
// observed (dexaflow_pods_created_total, dexaflow_task_pod_creation_duration_seconds,
// #1186). Both were declared and never written.
func TestExecuteRecordsPodCreation(t *testing.T) {
	cs := fake.NewClientset()
	spy := &podMetricsSpy{}
	e := NewKubernetesExecutor(cs, "ns")
	e.SetPodMetrics(spy)

	if _, err := e.Execute(context.Background(), Request{RunID: "r", DagID: "etl", TaskID: "t1", TryNumber: 1, Image: "img"}); err != nil {
		t.Fatal(err)
	}
	cs.PrependReactor("create", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("admission webhook denied the request")
	})
	if _, err := e.Execute(context.Background(), Request{RunID: "r", DagID: "etl", TaskID: "t2", TryNumber: 1, Image: "img"}); err == nil {
		t.Fatal("the second create must fail")
	}

	if len(spy.seen) != 2 {
		t.Fatalf("pod creations recorded = %v, want 2", spy.seen)
	}
	if spy.seen[0].dagID != "etl" || spy.seen[0].result != "success" {
		t.Errorf("first create = %+v, want dag etl, result success", spy.seen[0])
	}
	if spy.seen[1].result != "error" {
		t.Errorf("failed create = %+v, want result error", spy.seen[1])
	}
}

// A task the execution policy refuses never calls the apiserver, so no pod
// creation is recorded for it.
func TestExecuteRefusedRecordsNoPodCreation(t *testing.T) {
	spy := &podMetricsSpy{}
	e := NewKubernetesExecutor(fake.NewClientset(), "ns")
	e.SetPodMetrics(spy)
	policy, err := domain.ParseExecutionPolicy(domain.ExecutionPolicyConfig{Fields: []string{"none"}})
	if err != nil {
		t.Fatal(err)
	}
	req := sampleReq()
	req.Execution.Affinity = map[string]any{"nodeAffinity": map[string]any{}}
	req.ExecutionPolicy = policy
	if disp, _ := e.Execute(context.Background(), req); disp != Refused {
		t.Fatalf("disposition = %s, want Refused", disp)
	}
	if len(spy.seen) != 0 {
		t.Errorf("a refused task must record no pod creation, got %v", spy.seen)
	}
}
