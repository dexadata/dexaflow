package executor

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/dexadata/dexaflow/internal/taskoutcome"
)

// withMessage puts a raw termination message on the pod's task container: a
// record the agent did not necessarily write.
func withMessage(pod *corev1.Pod, msg string) *corev1.Pod {
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:  taskContainerName,
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Message: msg}},
	}}
	return pod
}

// reconcileOne runs one reconcile sweep over pod and returns what it settled
// for the task instance id.
func reconcileOne(t *testing.T, pod *corev1.Pod, id string) (settledOutcome, bool) {
	t.Helper()
	cs := fake.NewClientset()
	if err := cs.Tracker().Add(pod); err != nil {
		t.Fatal(err)
	}
	rep := &fakeReporter{}
	if err := NewReconciler(cs, "leoflow", rep).Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got, ok := rep.settled[id]
	return got, ok
}

// TestReconcileSettlesAnUnexecutableStartAsInfra is R-01's third item
// (#1594). An agent whose control plane did not answer before the task's
// code ran exits with the unexecutable record. The reconciler settles it on
// the infra rail (ADR 0051), so the task keeps its own retries, with the
// control_plane_unavailable mark ahead of the agent's classification. Before
// the fix the same pod settled as a task failure.
func TestReconcileSettlesAnUnexecutableStartAsInfra(t *testing.T) {
	const reason = "the control plane did not answer"
	pod := withRecord(managedPod("p-start", "ti-start", corev1.PodFailed), taskoutcome.UnexecutableBecause(reason))

	got, ok := reconcileOne(t, pod, "ti-start")
	if !ok || got.kind != settleInfra {
		t.Fatalf("want an infra settle, got %+v (settled=%v)", got, ok)
	}
	if !strings.HasPrefix(got.reason, "control_plane_unavailable: ") || !strings.Contains(got.reason, reason) {
		t.Errorf("reason %q must carry the control_plane_unavailable mark and the agent's classification", got.reason)
	}
	if got.tryNumber != 1 {
		t.Errorf("the infra settle must carry the pod's try-number, got %d", got.tryNumber)
	}
}

// TestReconcileInfraMarkerNeedsTheAgentsBounds: the task can write its own
// termination message, so the marker is honored only on the record the agent
// writes, a classified reason and no exit code (no user process ran). Any
// other shape settles as a task failure, as before.
func TestReconcileInfraMarkerNeedsTheAgentsBounds(t *testing.T) {
	cases := map[string]string{
		"no author":    `{"v":1,"outcome":"failed","reason":"the control plane did not answer","infra":true}`,
		"no reason":    `{"v":1,"outcome":"failed","infra":true}`,
		"an exit code": `{"v":1,"outcome":"failed","exit_code":1,"reason":"x","author":"agent","infra":true}`,
	}
	for name, msg := range cases {
		t.Run(name, func(t *testing.T) {
			if _, ok := taskoutcome.Decode(msg); !ok {
				t.Fatalf("fixture must decode: %s", msg)
			}
			got, ok := reconcileOne(t, withMessage(managedPod("p-forged", "ti-forged", corev1.PodFailed), msg), "ti-forged")
			if !ok || got.kind != settleFailed {
				t.Errorf("want a task-failure settle, got %+v (settled=%v)", got, ok)
			}
		})
	}
}
