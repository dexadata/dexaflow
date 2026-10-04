package executor

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// startedPod is a task pod the kubelet has accepted: status.startTime is set,
// so its containers may be running user code.
func startedPod(name, runID, taskID string, try int, phase corev1.PodPhase) *corev1.Pod {
	pod := taskPod(name, runID, taskID, try, phase)
	pod.UID = types.UID("uid-" + name)
	now := metav1.Now()
	pod.Status.StartTime = &now
	return pod
}

type decisions struct{ got []string }

func (d *decisions) RecordSchedulerDecision(s string) { d.got = append(d.got, s) }

func activeDeadline(t *testing.T, cs *fake.Clientset, name string) *int64 {
	t.Helper()
	pod, err := cs.CoreV1().Pods("leoflow").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get pod %s: %v", name, err)
	}
	return pod.Spec.ActiveDeadlineSeconds
}

func patchActions(cs *fake.Clientset) []k8stesting.PatchAction {
	var out []k8stesting.PatchAction
	for _, a := range cs.Actions() {
		if p, ok := a.(k8stesting.PatchAction); ok && a.GetVerb() == "patch" {
			out = append(out, p)
		}
	}
	return out
}

// TestTeardownStopsAStartedPodInPlace (ADR 0052 amendment, part 3): a reaped
// pod whose containers started is stopped by lowering its active deadline to
// 1 second, not deleted. The kubelet kills the containers with the normal
// grace, and the pod object stays, carrying the task container's termination
// message for the reconciler.
func TestTeardownStopsAStartedPodInPlace(t *testing.T) {
	pod := startedPod("p", "run-a", "extract", 1, corev1.PodRunning)
	long := int64(3600)
	pod.Spec.ActiveDeadlineSeconds = &long
	cs := fake.NewClientset(pod)
	e := NewKubernetesExecutor(cs, "leoflow")
	if err := e.DeleteTaskPod(context.Background(), Attempt{RunID: "run-a", TaskID: "extract", TryNumber: 1}); err != nil {
		t.Fatalf("DeleteTaskPod: %v", err)
	}
	if !podNames(t, cs)["p"] {
		t.Fatal("a started pod must be stopped in place, not deleted")
	}
	if d := activeDeadline(t, cs, "p"); d == nil || *d != 1 {
		t.Fatalf("activeDeadlineSeconds = %v, want 1", d)
	}
	patches := patchActions(cs)
	if len(patches) != 1 {
		t.Fatalf("want one patch, got %d", len(patches))
	}
	if body := string(patches[0].GetPatch()); !strings.Contains(body, `"uid":"uid-p"`) {
		t.Errorf("the patch must pin the listed pod's UID, got %s", body)
	}
}

// TestTeardownDeletesANeverStartedPod: a pod with no startTime has no
// container and no record, and a Pending pod can still start the task, so it
// is deleted as before.
func TestTeardownDeletesANeverStartedPod(t *testing.T) {
	cs := fake.NewClientset(taskPod("p", "run-a", "extract", 1, corev1.PodPending))
	e := NewKubernetesExecutor(cs, "leoflow")
	if err := e.DeleteTaskPod(context.Background(), Attempt{RunID: "run-a", TaskID: "extract", TryNumber: 1}); err != nil {
		t.Fatalf("DeleteTaskPod: %v", err)
	}
	if podNames(t, cs)["p"] {
		t.Fatal("a never-started pod must be deleted")
	}
	if n := len(patchActions(cs)); n != 0 {
		t.Errorf("a never-started pod is not patched, got %d patches", n)
	}
}

// TestTeardownFallsBackToDeleteWhenThePatchIsRefused: a hand-maintained Role
// without `patch`, or a webhook rejecting pod updates, must not leave the task
// running: the teardown deletes the pod as before and meters the fallback.
func TestTeardownFallsBackToDeleteWhenThePatchIsRefused(t *testing.T) {
	for name, refusal := range map[string]error{
		"forbidden": apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "p", nil),
		"webhook":   apierrors.NewBadRequest("admission webhook denied the request"),
	} {
		t.Run(name, func(t *testing.T) {
			cs := fake.NewClientset(startedPod("p", "run-a", "extract", 1, corev1.PodRunning))
			cs.PrependReactor("patch", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
				return true, nil, refusal
			})
			e := NewKubernetesExecutor(cs, "leoflow")
			rec := &decisions{}
			e.SetTeardownRecorder(rec)
			if err := e.DeleteTaskPod(context.Background(), Attempt{RunID: "run-a", TaskID: "extract", TryNumber: 1}); err != nil {
				t.Fatalf("DeleteTaskPod: %v", err)
			}
			if podNames(t, cs)["p"] {
				t.Fatal("a refused stop must fall back to delete")
			}
			if len(rec.got) != 1 || rec.got[0] != "reap_teardown_delete_fallback" {
				t.Errorf("the fallback must be metered once, got %v", rec.got)
			}
		})
	}
}

// TestTeardownToleratesAPodGoneBeforeThePatch: NotFound (or a failed UID
// precondition) means the listed pod is already gone; nothing is deleted
// instead and nothing is metered.
func TestTeardownToleratesAPodGoneBeforeThePatch(t *testing.T) {
	cs := fake.NewClientset(startedPod("p", "run-a", "extract", 1, corev1.PodRunning))
	cs.PrependReactor("patch", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, "p")
	})
	e := NewKubernetesExecutor(cs, "leoflow")
	rec := &decisions{}
	e.SetTeardownRecorder(rec)
	if err := e.DeleteTaskPod(context.Background(), Attempt{RunID: "run-a", TaskID: "extract", TryNumber: 1}); err != nil {
		t.Fatalf("DeleteTaskPod: %v", err)
	}
	for _, a := range cs.Actions() {
		if a.GetVerb() == "delete" {
			t.Fatal("a pod gone before the patch must not be deleted again")
		}
	}
	if len(rec.got) != 0 {
		t.Errorf("nothing to meter, got %v", rec.got)
	}
}

// TestTeardownLeavesATerminalPodUntouched: a finished pod is the
// reconciler's (#928), so it is neither patched nor deleted.
func TestTeardownLeavesATerminalPodUntouched(t *testing.T) {
	cs := fake.NewClientset(startedPod("p", "run-a", "extract", 1, corev1.PodFailed))
	e := NewKubernetesExecutor(cs, "leoflow")
	if err := e.DeleteTaskPod(context.Background(), Attempt{RunID: "run-a", TaskID: "extract", TryNumber: 1}); err != nil {
		t.Fatalf("DeleteTaskPod: %v", err)
	}
	for _, a := range cs.Actions() {
		if a.GetVerb() == "patch" || a.GetVerb() == "delete" {
			t.Fatalf("a terminal pod must be left alone, got %s", a.GetVerb())
		}
	}
}

// TestRunTeardownStopsStartedPodsInPlace: the orphan-run reaper's teardown
// uses the same helper and gets the same behavior per pod.
func TestRunTeardownStopsStartedPodsInPlace(t *testing.T) {
	cs := fake.NewClientset(
		startedPod("running", "run-a", "extract", 1, corev1.PodRunning),
		taskPod("pending", "run-a", "load", 1, corev1.PodPending),
	)
	e := NewKubernetesExecutor(cs, "leoflow")
	if err := e.DeleteRunPods(context.Background(), "run-a"); err != nil {
		t.Fatalf("DeleteRunPods: %v", err)
	}
	got := podNames(t, cs)
	if !got["running"] || got["pending"] {
		t.Fatalf("want the started pod stopped in place and the pending one deleted, got %v", got)
	}
	if d := activeDeadline(t, cs, "running"); d == nil || *d != 1 {
		t.Fatalf("activeDeadlineSeconds = %v, want 1", d)
	}
}
