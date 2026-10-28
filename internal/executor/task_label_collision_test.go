package executor

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

const collisionRun = "11111111-2222-3333-4444-555555555555"

// livePodOf builds the live task pod BuildPod creates for one task of
// collisionRun, on try 1 and epoch 1.
func livePodOf(task string) *corev1.Pod {
	p := BuildPod(Request{TaskInstanceID: "ti-" + task, RunID: collisionRun, DagID: "etl", TaskID: task, TryNumber: 1, AttemptEpoch: 1, Image: "img"})
	p.Namespace = "leoflow"
	p.UID = types.UID("uid-" + task)
	now := metav1.Now()
	p.Status.Phase = corev1.PodRunning
	p.Status.StartTime = &now
	return p
}

func attemptOfTask(task string) Attempt {
	return Attempt{RunID: collisionRun, TaskID: task, TryNumber: 1, AttemptEpoch: 1}
}

// TestBuildPodStampsTheRawTaskID: the task-id label is lossy (lowercased, '_'
// and '.' mapped to '-'), so the pod also carries the raw id, which the
// attempt filters compare exactly (R-10, #1598).
func TestBuildPodStampsTheRawTaskID(t *testing.T) {
	p := livePodOf("Load_A.x")
	if got := p.Annotations[podAnnotationRawTaskID]; got != "Load_A.x" {
		t.Errorf("raw task id annotation = %q, want %q", got, "Load_A.x")
	}
}

// TestTaskIDLabelCollisionDoesNotCrossTasks is R-10 / M-07: task ids that
// differ only by case, '_' or '.' share the task-id label. Before the fix the
// presence read of a task with no pod saw its sibling's live pod (so its
// pod-lost and dispatch-lost reaps deferred forever), and its teardown stopped
// the sibling's live pod.
func TestTaskIDLabelCollisionDoesNotCrossTasks(t *testing.T) {
	pairs := [][2]string{
		{"load_a", "load-a"},
		{"Load", "load"},
		{"extract.users", "extract_users"},
	}
	for _, pair := range pairs {
		reaped, sibling := pair[0], pair[1]
		t.Run(reaped+" vs "+sibling, func(t *testing.T) {
			ctx := context.Background()
			other := livePodOf(sibling)
			cs := fake.NewClientset(other)
			e := NewKubernetesExecutor(cs, "leoflow")

			pres, err := e.TaskPodPresence(ctx, attemptOfTask(reaped))
			if err != nil {
				t.Fatal(err)
			}
			if pres != PodPresenceAbsent {
				t.Errorf("presence of %s, which has no pod, = %v; the sibling's pod must not count", reaped, pres)
			}
			if _, err = e.DeleteTaskPod(ctx, attemptOfTask(reaped)); err != nil {
				t.Fatal(err)
			}
			got, err := cs.CoreV1().Pods("leoflow").Get(ctx, other.Name, metav1.GetOptions{})
			if err != nil {
				t.Fatalf("the sibling's pod was deleted: %v", err)
			}
			if got.Spec.ActiveDeadlineSeconds != nil {
				t.Errorf("tearing down %s stopped %s's live pod", reaped, sibling)
			}
			if pres, err := e.TaskPodPresence(ctx, attemptOfTask(sibling)); err != nil || pres != PodPresenceLive {
				t.Errorf("the sibling's own presence = %v err=%v, want live", pres, err)
			}
		})
	}
}

// TestCachedPodActiveDoesNotCrossTasks: the informer fast path asks the same
// exact-task question as the live read.
func TestCachedPodActiveDoesNotCrossTasks(t *testing.T) {
	cs := fake.NewClientset(livePodOf("load-a"))
	pi := NewPodInformer(cs, "leoflow")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pi.Start(ctx)
	if !pi.WaitForCacheSync(ctx) {
		t.Fatal("cache did not sync")
	}
	if pi.CachedPodActive(attemptOfTask("load_a")) {
		t.Errorf("a sibling's live pod must not make load_a active")
	}
	if !pi.CachedPodActive(attemptOfTask("load-a")) {
		t.Errorf("the task's own pod must read active")
	}
}

// TestInfraEvidenceDoesNotCrossTasks: the infra confirmation reads only the
// attempt's own pods, so a sibling's running pod does not hold a mark back.
func TestInfraEvidenceDoesNotCrossTasks(t *testing.T) {
	pods := []*corev1.Pod{livePodOf("load-a")}
	if got := attemptEvidence(pods, attemptOfTask("load_a")); got != evidenceNone {
		t.Errorf("evidence for load_a = %v, want none (the pod is load-a's)", got)
	}
	if got := attemptEvidence(pods, attemptOfTask("load-a")); got != evidencePending {
		t.Errorf("evidence for load-a = %v, want pending (its own running pod)", got)
	}
}

// TestPodWithoutRawTaskIDKeepsLabelMatching: a pod created by a release before
// the annotation existed is matched on its labels alone, as before, so an
// upgrade neither orphans its teardown nor hides it from a presence read.
func TestPodWithoutRawTaskIDKeepsLabelMatching(t *testing.T) {
	ctx := context.Background()
	legacy := livePodOf("load_a")
	delete(legacy.Annotations, podAnnotationRawTaskID)
	cs := fake.NewClientset(legacy)
	e := NewKubernetesExecutor(cs, "leoflow")
	if pres, err := e.TaskPodPresence(ctx, attemptOfTask("load_a")); err != nil || pres != PodPresenceLive {
		t.Errorf("a pre-annotation pod must still read live for its task, got %v err=%v", pres, err)
	}
	if _, err := e.DeleteTaskPod(ctx, attemptOfTask("load_a")); err != nil {
		t.Fatal(err)
	}
	got, err := cs.CoreV1().Pods("leoflow").Get(ctx, legacy.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Spec.ActiveDeadlineSeconds == nil {
		t.Errorf("a pre-annotation pod of the reaped attempt must still be torn down")
	}
}
