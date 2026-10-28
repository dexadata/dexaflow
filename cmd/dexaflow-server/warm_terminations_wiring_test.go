package main

import (
	"context"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/executor"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// TestBuildWarmPodInformerRecordsTerminationsOnTheReaperLister: the warm-pod
// informer the server builds records the final state of a deleted warm worker
// on the same warm-pod client the reaper lists through, so the warm-worker-lost
// reaper names an OOM kill of a worker deleted before any LIST saw it (#1835).
func TestBuildWarmPodInformerRecordsTerminationsOnTheReaperLister(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "warm-oom", Namespace: "leoflow",
			Labels: map[string]string{"leoflow.io/warm-worker": "true"},
		},
		Status: corev1.PodStatus{Phase: corev1.PodFailed, ContainerStatuses: []corev1.ContainerStatus{{
			Name:  "warm-worker",
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}},
		}}},
	}
	cs := fake.NewClientset(pod)
	warmPods := executor.NewKubernetesWarmPods(cs, "leoflow", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if wi := buildWarmPodInformer(ctx, cs, "leoflow", warmPods, discardLog()); wi == nil {
		t.Fatal("buildWarmPodInformer returned nil")
	}
	if err := cs.CoreV1().Pods("leoflow").Delete(ctx, "warm-oom", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for warmPods.LastTerminationReason("warm-oom") != "OOMKilled" {
		if time.Now().After(deadline) {
			t.Fatal("the deleted OOMKilled warm worker was not recorded on the reaper's lister")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
