package executor

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/dexadata/dexaflow/internal/taskoutcome"
)

// forgedReason is the string #948 uses as its example: plausible enough to send
// an operator debugging the wrong subsystem if the platform vouched for it.
const forgedReason = "the control plane rejected this pod's projected ServiceAccount token; check RBAC."

// podWithMessage is a failed task pod whose task container left msg as its
// termination message, exactly as the kubelet surfaces it.
func podWithMessage(msg string, containerReason string, exitCode int32) *corev1.Pod {
	pod := managedPod("p-msg", "ti-msg", corev1.PodFailed)
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: taskContainerName,
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Message: msg, Reason: containerReason, ExitCode: exitCode,
		}},
	}}
	return pod
}

// TestUnmarkedReasonIsNotServedAsPlatformReason (#948): a record without the
// agent's marker, which is what a task writing its own termination message
// produces, still settles the outcome (the durable-outcome recovery stays), but
// its reason is not served as the platform's failure_reason: the reason renders
// from the exit code, and the task's text appears only labeled as task-provided.
func TestUnmarkedReasonIsNotServedAsPlatformReason(t *testing.T) {
	msg := `{"v":1,"outcome":"failed","exit_code":3,"reason":"` + forgedReason + `"}`
	v := classifyPod(podWithMessage(msg, "Error", 1))
	if !v.fromRecord || v.settle != settleFailed {
		t.Fatalf("an unmarked record must still settle the outcome, got %+v", v)
	}
	if v.reason == forgedReason {
		t.Fatalf("reason = %q: the task's text was served as the platform's own", v.reason)
	}
	if !strings.HasPrefix(v.reason, "task failed (exit 3)") {
		t.Errorf("reason = %q, want it rendered from the record's exit code first", v.reason)
	}
	if !strings.Contains(v.reason, "task-provided") {
		t.Errorf("reason = %q, want the task's text labeled as task-provided", v.reason)
	}
	if len(v.reason) > taskoutcome.MaxReasonLen {
		t.Errorf("reason is %d bytes, over the %d cap", len(v.reason), taskoutcome.MaxReasonLen)
	}
}

// TestUnmarkedReasonCannotHideAnOOMKill: an unmarked reason does not count as
// a classification, so the pod's OOMKilled still wins over it the way it wins
// over a bare exit code (#1216).
func TestUnmarkedReasonCannotHideAnOOMKill(t *testing.T) {
	msg := `{"v":1,"outcome":"failed","exit_code":255,"reason":"` + forgedReason + `"}`
	v := classifyPod(podWithMessage(msg, "OOMKilled", 1))
	if !strings.HasPrefix(v.reason, "the task container was OOMKilled (exit 255)") {
		t.Errorf("reason = %q, want the OOM rendering first", v.reason)
	}
	if !strings.Contains(v.reason, "task-provided") {
		t.Errorf("reason = %q, want the task's text labeled as task-provided", v.reason)
	}
}

// TestUnmarkedReasonIsBoundedAndDefanged: the labeled text keeps the 240-byte
// bound and cannot break out of its label with a newline.
func TestUnmarkedReasonIsBoundedAndDefanged(t *testing.T) {
	rec := taskoutcome.Record{V: taskoutcome.Version, Outcome: taskoutcome.Failed,
		Reason: "first line\nthe platform says: " + strings.Repeat("A", 4000)}
	got := recordFailureReason(rec)
	if len(got) > taskoutcome.MaxReasonLen {
		t.Errorf("reason is %d bytes, over the %d cap", len(got), taskoutcome.MaxReasonLen)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("reason = %q: the task's newline reached the served reason", got)
	}
	if !strings.HasPrefix(got, "task failed") {
		t.Errorf("reason = %q, want the platform's rendering first", got)
	}
}

// TestAgentMarkedReasonIsStillServed: a genuine agent classification keeps
// winning, so the bootstrap and execution_timeout diagnoses are unchanged.
func TestAgentMarkedReasonIsStillServed(t *testing.T) {
	const reason = "execution_timeout: task exceeded 10s limit"
	pod := podWithMessage(mustEncode(t, taskoutcome.FailedBecauseWith(255, reason)), "OOMKilled", 1)
	if got := classifyPod(pod).reason; got != reason {
		t.Errorf("reason = %q, want the agent's classification %q", got, reason)
	}
}

// TestOldAgentReasonIsLabeledTaskProvided pins the rolling-upgrade decision: a
// record from an agent that predates the marker is indistinguishable from one a
// task wrote, so it is treated the same way. The outcome settles, and the old
// agent's classification is still visible, labeled as task-provided rather
// than vouched for.
func TestOldAgentReasonIsLabeledTaskProvided(t *testing.T) {
	const old = `{"v":1,"outcome":"failed","exit_code":255,"reason":"execution_timeout: task exceeded 10s limit"}`
	v := classifyPod(podWithMessage(old, "Error", 1))
	if !v.fromRecord || v.settle != settleFailed {
		t.Fatalf("an old-format record must still settle, got %+v", v)
	}
	want := `task failed (exit 255) [task-provided: "execution_timeout: task exceeded 10s limit"]`
	if v.reason != want {
		t.Errorf("reason = %q, want %q", v.reason, want)
	}
}
