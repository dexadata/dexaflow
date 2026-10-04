package executor

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/dexadata/dexaflow/internal/taskoutcome"
)

// fakeConfirmer serves provisional infra failures and records confirmations.
type fakeConfirmer struct {
	rows      []ProvisionalInfraFailure
	listErr   error
	confirmed []Attempt
}

func (f *fakeConfirmer) ListProvisionalInfraFailures(context.Context) ([]ProvisionalInfraFailure, error) {
	return f.rows, f.listErr
}

func (f *fakeConfirmer) ConfirmInfraFailure(_ context.Context, _ string, try, epoch int) (bool, error) {
	f.confirmed = append(f.confirmed, Attempt{TryNumber: try, AttemptEpoch: epoch})
	return true, nil
}

func provisionalRow() ProvisionalInfraFailure {
	return ProvisionalInfraFailure{TaskInstanceID: "ti-1", DagRunID: "run-a", TaskID: "extract", TryNumber: 1, AttemptEpoch: 2}
}

// withTaskContainer gives a pod a task container status: terminated with exit
// code code when terminated is true, running otherwise.
func withTaskContainer(pod *corev1.Pod, terminated bool, code int32) *corev1.Pod {
	cs := corev1.ContainerStatus{Name: taskContainerName}
	if terminated {
		cs.State.Terminated = &corev1.ContainerStateTerminated{ExitCode: code}
	} else {
		cs.State.Running = &corev1.ContainerStateRunning{}
	}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{cs}
	return pod
}

func confirmSweep(t *testing.T, pods ...*corev1.Pod) *fakeConfirmer {
	t.Helper()
	cs := fake.NewClientset()
	for _, p := range pods {
		if _, err := cs.CoreV1().Pods("leoflow").Create(context.Background(), p, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create pod: %v", err)
		}
	}
	c := &fakeConfirmer{rows: []ProvisionalInfraFailure{provisionalRow()}}
	r := NewReconciler(cs, "leoflow", &fakeReporter{})
	r.SetInfraConfirmer(c)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return c
}

// TestReconcilerConfirmsAnInfraMarkWithNoPodLeft: the attempt's pod is gone,
// so no record can surface; the mark is confirmed and the planner may re-place.
func TestReconcilerConfirmsAnInfraMarkWithNoPodLeft(t *testing.T) {
	c := confirmSweep(t)
	if len(c.confirmed) != 1 || c.confirmed[0] != (Attempt{TryNumber: 1, AttemptEpoch: 2}) {
		t.Fatalf("want the listed (try, epoch) confirmed, got %+v", c.confirmed)
	}
}

// TestReconcilerConfirmsWhenTheTaskContainerExitedWithoutSuccess: the pod is
// still there but its task container terminated with no SUCCESS record.
func TestReconcilerConfirmsWhenTheTaskContainerExitedWithoutSuccess(t *testing.T) {
	pod := withTaskContainer(epochPod("p", "run-a", "extract", 1, 2, corev1.PodFailed), true, 137)
	if c := confirmSweep(t, pod); len(c.confirmed) != 1 {
		t.Fatalf("a terminated task container without a SUCCESS record confirms the mark, got %+v", c.confirmed)
	}
}

// TestReconcilerLeavesATerminatingPod: a pod stopped in place is still inside
// its termination grace; it may already report phase Failed (reason
// DeadlineExceeded) while its task container runs. The test is the container's
// terminated state, never the phase: wait for the next sweep.
func TestReconcilerLeavesATerminatingPod(t *testing.T) {
	pod := withTaskContainer(epochPod("p", "run-a", "extract", 1, 2, corev1.PodFailed), false, 0)
	pod.Status.Reason = "DeadlineExceeded"
	if c := confirmSweep(t, pod); len(c.confirmed) != 0 {
		t.Fatalf("a task container that has not terminated must not be confirmed, got %+v", c.confirmed)
	}
}

// TestReconcilerDoesNotConfirmOverASuccessRecord: a SUCCESS record of the same
// attempt is ground truth; the mark must not be confirmed over it.
func TestReconcilerDoesNotConfirmOverASuccessRecord(t *testing.T) {
	pod := withRecord(epochPod("p", "run-a", "extract", 1, 2, corev1.PodSucceeded), taskoutcome.Succeeded())
	pod.Status.ContainerStatuses[0].Name = taskContainerName
	if c := confirmSweep(t, pod); len(c.confirmed) != 0 {
		t.Fatalf("a SUCCESS record must not be confirmed away, got %+v", c.confirmed)
	}
}

// TestReconcilerConfirmIgnoresOtherAttempts: a live pod of a different epoch
// (or try) is not this attempt's evidence.
func TestReconcilerConfirmIgnoresOtherAttempts(t *testing.T) {
	other := withTaskContainer(epochPod("p", "run-a", "extract", 1, 1, corev1.PodRunning), false, 0)
	if c := confirmSweep(t, other); len(c.confirmed) != 1 {
		t.Fatalf("another epoch's live pod must not hold the confirmation, got %+v", c.confirmed)
	}
}

// TestReconcilerConfirmListErrorIsNotFatal: a failed list is retried next
// sweep; the settle sweep itself still completes.
func TestReconcilerConfirmListErrorIsNotFatal(t *testing.T) {
	c := &fakeConfirmer{listErr: errors.New("db down")}
	r := NewReconciler(fake.NewClientset(), "leoflow", &fakeReporter{})
	r.SetInfraConfirmer(c)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if r.LastSweepCompletedAt().IsZero() {
		t.Error("the settle sweep must still be stamped")
	}
}
