package executor

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/dexadata/dexaflow/internal/taskoutcome"
)

// disrupted marks a pod with the DisruptionTarget condition the kubelet, the
// scheduler or the eviction API sets on a pod it is about to stop.
func disrupted(pod *corev1.Pod, reason string) *corev1.Pod {
	pod.Status.Conditions = append(pod.Status.Conditions, corev1.PodCondition{
		Type: corev1.DisruptionTarget, Status: corev1.ConditionTrue, Reason: reason,
	})
	return pod
}

// taskExit gives a pod a terminated task container with the given exit code
// and reason, and no outcome record.
func taskExit(pod *corev1.Pod, code int32, reason string) *corev1.Pod {
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:  taskContainerName,
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: code, Reason: reason}},
	}}
	return pod
}

// withPodReason sets the pod-level failure reason the kubelet writes.
func withPodReason(pod *corev1.Pod, reason, message string) *corev1.Pod {
	pod.Status.Reason, pod.Status.Message = reason, message
	return pod
}

// TestReconcileSettlesDisruptedPodsAsInfra is R-02 (#1595): a task pod the
// cluster stopped (graceful node shutdown on a spot reclaim, node-pressure
// eviction, preemption, admission refusal) is an infrastructure failure under
// ADR 0051, so the reconciler settles it on the infra rail and the task keeps
// its own retries. Before the fix every case below settled as a task failure.
func TestReconcileSettlesDisruptedPodsAsInfra(t *testing.T) {
	cases := map[string]*corev1.Pod{
		// Graceful node shutdown: the kubelet kills the task (exit 137) and
		// marks the pod with DisruptionTarget=TerminationByKubelet.
		"ti-shutdown": disrupted(taskExit(withPodReason(managedPod("p-shutdown", "ti-shutdown", corev1.PodFailed),
			"Terminated", "Pod was terminated in response to imminent node shutdown."), 137, "Error"), "TerminationByKubelet"),
		// Node-pressure eviction.
		"ti-evicted": disrupted(withPodReason(managedPod("p-evicted", "ti-evicted", corev1.PodFailed),
			"Evicted", "The node was low on resource: memory."), "TerminationByKubelet"),
		// Preemption by the scheduler carries only the condition.
		"ti-preempted": disrupted(taskExit(managedPod("p-preempted", "ti-preempted", corev1.PodFailed), 143, "Error"),
			"PreemptionByScheduler"),
		// Older kubelets and admission refusals set only the pod reason.
		"ti-evicted-old":   withPodReason(managedPod("p-evicted-old", "ti-evicted-old", corev1.PodFailed), "Evicted", ""),
		"ti-shutdown-old":  withPodReason(managedPod("p-shutdown-old", "ti-shutdown-old", corev1.PodFailed), "Shutdown", ""),
		"ti-nodeshutdown":  withPodReason(managedPod("p-nodeshutdown", "ti-nodeshutdown", corev1.PodFailed), "NodeShutdown", ""),
		"ti-terminated":    withPodReason(managedPod("p-terminated", "ti-terminated", corev1.PodFailed), "Terminated", ""),
		"ti-outofcpu":      withPodReason(managedPod("p-outofcpu", "ti-outofcpu", corev1.PodFailed), "OutOfcpu", ""),
		"ti-outofmemory":   withPodReason(managedPod("p-outofmemory", "ti-outofmemory", corev1.PodFailed), "OutOfmemory", ""),
		"ti-admissionfail": withPodReason(managedPod("p-admissionfail", "ti-admissionfail", corev1.PodFailed), "UnexpectedAdmissionError", ""),
		// The agent caught the SIGTERM and wrote a FAILED record before dying:
		// the record does not make the node's death the task's fault.
		"ti-sigterm-record": disrupted(withPodReason(withRecord(managedPod("p-sigterm-record", "ti-sigterm-record", corev1.PodFailed),
			taskoutcome.FailedWith(143)), "Terminated", ""), "TerminationByKubelet"),
	}
	cs := fake.NewClientset()
	for _, p := range cases {
		if err := cs.Tracker().Add(p); err != nil {
			t.Fatal(err)
		}
	}
	rep := &fakeReporter{}
	if err := NewReconciler(cs, "leoflow", rep).Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	for id := range cases {
		got, ok := rep.settled[id]
		if !ok || got.kind != settleInfra {
			t.Errorf("%s: want an infra settle, got %+v (settled=%v)", id, got, ok)
			continue
		}
		if !strings.HasPrefix(got.reason, "pod_disrupted: ") {
			t.Errorf("%s: reason %q must carry the pod_disrupted mark", id, got.reason)
		}
		if got.tryNumber != 1 {
			t.Errorf("%s: the infra settle must carry the pod's try-number, got %d", id, got.tryNumber)
		}
	}
}

// TestReconcileDisruptionKeepsTaskOutcomes: the infra rail is only for a pod
// the cluster stopped. A durable SUCCESS or RESCHEDULE record still wins, an
// OOMKilled task container is the task's own memory limit, and a condition
// that is not True, or an unrelated pod reason, changes nothing.
func TestReconcileDisruptionKeepsTaskOutcomes(t *testing.T) {
	at := time.Date(2099, 1, 2, 3, 4, 5, 0, time.UTC)
	cases := map[string]struct {
		pod  *corev1.Pod
		want settleKind
	}{
		"success record on a disrupted pod": {
			disrupted(withRecord(managedPod("p-ok", "ti-ok", corev1.PodFailed), taskoutcome.Succeeded()), "TerminationByKubelet"),
			settleSucceeded,
		},
		"reschedule record on a disrupted pod": {
			disrupted(withRecord(managedPod("p-resched", "ti-resched", corev1.PodFailed), taskoutcome.RescheduledAt(at)), "TerminationByKubelet"),
			settleReschedule,
		},
		"OOMKilled task container": {
			taskExit(managedPod("p-oom", "ti-oom", corev1.PodFailed), 137, "OOMKilled"),
			settleFailed,
		},
		"OOMKilled task container on a disrupted pod": {
			disrupted(taskExit(managedPod("p-oom-d", "ti-oom-d", corev1.PodFailed), 137, "OOMKilled"), "TerminationByKubelet"),
			settleFailed,
		},
		"DisruptionTarget not True": {
			func() *corev1.Pod {
				p := taskExit(managedPod("p-false", "ti-false", corev1.PodFailed), 1, "Error")
				p.Status.Conditions = []corev1.PodCondition{{Type: corev1.DisruptionTarget, Status: corev1.ConditionFalse}}
				return p
			}(),
			settleFailed,
		},
		"plain task failure": {
			taskExit(managedPod("p-exit", "ti-exit", corev1.PodFailed), 1, "Error"),
			settleFailed,
		},
		"deadline exceeded": {
			withPodReason(managedPod("p-deadline", "ti-deadline", corev1.PodFailed), "DeadlineExceeded", ""),
			settleFailed,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			rep := &fakeReporter{}
			if err := NewReconciler(fake.NewClientset(c.pod), "leoflow", rep).Reconcile(context.Background()); err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			tiID := c.pod.Annotations["leoflow.io/task-instance-id"]
			if got := rep.settled[tiID]; got.kind != c.want {
				t.Errorf("settled %+v, want kind %v", got, c.want)
			}
		})
	}
}

// TestDisruptionReasonIsBounded: the pod's reason and message come from the
// kubelet and end up in an end-user field, so the infra reason is capped like
// every other reason the reconciler builds.
func TestDisruptionReasonIsBounded(t *testing.T) {
	pod := withPodReason(managedPod("p", "ti", corev1.PodFailed), "Evicted", strings.Repeat("x", 10*taskoutcome.MaxReasonLen))
	v := classifyPod(pod)
	if v.settle != settleInfra {
		t.Fatalf("settle = %v, want infra", v.settle)
	}
	if len(v.reason) > taskoutcome.MaxReasonLen {
		t.Errorf("reason length %d exceeds the cap %d", len(v.reason), taskoutcome.MaxReasonLen)
	}
}

// Kubelet eviction messages, verbatim from pkg/kubelet/eviction/helpers.go
// (nodeLowMessageFmt, containerMessageFmt, containerEphemeralStorageMessageFmt,
// podEphemeralStorageMessageFmt, emptyDirMessageFmt) as the kubelet renders them.
const (
	evictNodePressureMsg = "The node was low on resource: ephemeral-storage. Threshold quantity: 10Gi, available: 9Gi. " +
		"Container task was using 12Gi, request is 0, has larger consumption of ephemeral-storage. "
	evictContainerEphemeralMsg = `Container task exceeded its local ephemeral storage limit "1Gi". `
	evictPodEphemeralMsg       = "Pod ephemeral local storage usage exceeds the total limit of containers 1Gi. "
	evictEmptyDirMsg           = `Usage of EmptyDir volume "scratch" exceeds the limit "500Mi". `
)

// TestReconcileOwnLimitEvictionIsATaskFailure is review finding 1595-1: the
// kubelet also evicts a pod (reason Evicted) when the task outgrows a limit of
// its own, the container's or the pod's ephemeral-storage limit or an emptyDir
// sizeLimit. That is the task's fault, the same class as OOMKilled, and it is
// deterministic, so it must stay a task failure that spends a retry instead of
// being re-placed on the infra budget. A node-pressure eviction, whose message
// names the node, stays infra. Each own-limit case is checked with and without
// the DisruptionTarget condition and with a FAILED record the agent wrote on
// the SIGTERM.
func TestReconcileOwnLimitEvictionIsATaskFailure(t *testing.T) {
	limits := map[string]string{
		"container ephemeral-storage limit": evictContainerEphemeralMsg,
		"pod ephemeral-storage limit":       evictPodEphemeralMsg,
		"emptyDir sizeLimit":                evictEmptyDirMsg,
	}
	shapes := map[string]func(*corev1.Pod) *corev1.Pod{
		"reason only":    func(p *corev1.Pod) *corev1.Pod { return p },
		"with condition": func(p *corev1.Pod) *corev1.Pod { return disrupted(p, "TerminationByKubelet") },
		"with a FAILED record": func(p *corev1.Pod) *corev1.Pod {
			return disrupted(withRecord(p, taskoutcome.FailedWith(143)), "TerminationByKubelet")
		},
	}
	for limit, msg := range limits {
		for shape, mk := range shapes {
			t.Run(limit+"/"+shape, func(t *testing.T) {
				pod := mk(withPodReason(managedPod("p-limit", "ti-limit", corev1.PodFailed), "Evicted", msg))
				rep := &fakeReporter{}
				if err := NewReconciler(fake.NewClientset(pod), "leoflow", rep).Reconcile(context.Background()); err != nil {
					t.Fatalf("Reconcile: %v", err)
				}
				got := rep.settled["ti-limit"]
				if got.kind != settleFailed {
					t.Fatalf("settled %+v, want a task failure", got)
				}
				if strings.HasPrefix(got.reason, "pod_disrupted") {
					t.Errorf("reason %q must not carry the infra mark", got.reason)
				}
				if !strings.Contains(got.reason, strings.TrimSpace(msg)) {
					t.Errorf("reason %q must name the kubelet's message %q", got.reason, msg)
				}
			})
		}
	}

	t.Run("node pressure stays infra", func(t *testing.T) {
		pod := disrupted(withPodReason(managedPod("p-node", "ti-node", corev1.PodFailed), "Evicted", evictNodePressureMsg),
			"TerminationByKubelet")
		if v := classifyPod(pod); v.settle != settleInfra {
			t.Fatalf("settle = %v, want infra", v.settle)
		}
		old := withPodReason(managedPod("p-node-old", "ti-node-old", corev1.PodFailed), "Evicted", evictNodePressureMsg)
		if v := classifyPod(old); v.settle != settleInfra {
			t.Fatalf("reason-only node pressure: settle = %v, want infra", v.settle)
		}
	})

	// Only an eviction is read for a limit: a node shutdown whose message
	// happens to share the wording is still the cluster stopping the pod.
	t.Run("limit wording on a non-eviction stays infra", func(t *testing.T) {
		pod := disrupted(withPodReason(managedPod("p-sd", "ti-sd", corev1.PodFailed), "Terminated", evictEmptyDirMsg),
			"TerminationByKubelet")
		if v := classifyPod(pod); v.settle != settleInfra {
			t.Fatalf("settle = %v, want infra", v.settle)
		}
	})
}

// TestOwnLimitEvictionReasonIsBounded: the kubelet message lands in the
// end-user field, so the task-failure reason is capped too.
func TestOwnLimitEvictionReasonIsBounded(t *testing.T) {
	long := `Usage of EmptyDir volume "` + strings.Repeat("v", 10*taskoutcome.MaxReasonLen) + `" exceeds the limit "1Gi". `
	v := classifyPod(withPodReason(managedPod("p", "ti", corev1.PodFailed), "Evicted", long))
	if v.settle != settleFailed {
		t.Fatalf("settle = %v, want a task failure", v.settle)
	}
	if len(v.reason) > taskoutcome.MaxReasonLen {
		t.Errorf("reason length %d exceeds the cap %d", len(v.reason), taskoutcome.MaxReasonLen)
	}
}
