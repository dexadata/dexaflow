package executor

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/dexadata/dexaflow/internal/taskoutcome"
)

// taskPod builds a minimal pod carrying the label scheme the dispatcher stamps
// (see BuildPod), so the reaper's label-selector deletes can find it.
func taskPod(name, runID, taskID string, try int, phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "leoflow",
			Labels: map[string]string{
				"leoflow.io/run-id":     sanitizeLabel(runID),
				"leoflow.io/task-id":    sanitizeLabel(taskID),
				"leoflow.io/try-number": itoa(try),
			},
		},
		Status: corev1.PodStatus{Phase: phase},
	}
}

func itoa(n int) string {
	// tiny helper so the test does not import strconv just for one call.
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

func podNames(t *testing.T, cs *fake.Clientset) map[string]bool {
	t.Helper()
	list, err := cs.CoreV1().Pods("leoflow").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing pods: %v", err)
	}
	out := map[string]bool{}
	for i := range list.Items {
		out[list.Items[i].Name] = true
	}
	return out
}

// TestDeleteTaskPod_TargetsExactlyTheReapedTI is the load-bearing invariant for
// #474: a reaped TI's pod is deleted, but a DIFFERENT run's pod and a DIFFERENT
// attempt's pod are left untouched. The reaper must only tear down the exact
// (run, task, try) it just settled — never a live neighbor.
func TestDeleteTaskPod_TargetsExactlyTheReapedTI(t *testing.T) {
	cs := fake.NewSimpleClientset(
		taskPod("victim", "run-a", "extract", 2, corev1.PodRunning),
		taskPod("other-run", "run-b", "extract", 2, corev1.PodRunning),    // different run
		taskPod("other-task", "run-a", "transform", 2, corev1.PodRunning), // different task
		taskPod("newer-try", "run-a", "extract", 3, corev1.PodRunning),    // different (newer) attempt
	)
	e := NewKubernetesExecutor(cs, "leoflow")

	if err := e.DeleteTaskPod(context.Background(), "run-a", "extract", 2); err != nil {
		t.Fatalf("DeleteTaskPod: %v", err)
	}
	got := podNames(t, cs)
	if got["victim"] {
		t.Errorf("reaped TI's pod was NOT deleted: %v", got)
	}
	for _, keep := range []string{"other-run", "other-task", "newer-try"} {
		if !got[keep] {
			t.Errorf("pod %q was wrongly deleted (must survive): %v", keep, got)
		}
	}
}

// TestDeleteTaskPod_ToleratesNotFound: deleting when no matching pod exists is a
// no-op, not an error — the pod may already have been GC'd.
func TestDeleteTaskPod_ToleratesNotFound(t *testing.T) {
	cs := fake.NewSimpleClientset()
	e := NewKubernetesExecutor(cs, "leoflow")
	if err := e.DeleteTaskPod(context.Background(), "run-a", "extract", 1); err != nil {
		t.Errorf("DeleteTaskPod on absent pod = %v, want nil", err)
	}
}

// TestDeleteRunPods_DeletesAllOfTheRunOnly: the orphan reaper abandons a whole
// run, so every pod of that run is torn down — and no other run's is.
func TestDeleteRunPods_DeletesAllOfTheRunOnly(t *testing.T) {
	cs := fake.NewSimpleClientset(
		taskPod("a1", "run-a", "extract", 1, corev1.PodRunning),
		taskPod("a2", "run-a", "transform", 1, corev1.PodPending),
		taskPod("b1", "run-b", "extract", 1, corev1.PodRunning),
	)
	e := NewKubernetesExecutor(cs, "leoflow")
	if err := e.DeleteRunPods(context.Background(), "run-a"); err != nil {
		t.Fatalf("DeleteRunPods: %v", err)
	}
	got := podNames(t, cs)
	if got["a1"] || got["a2"] {
		t.Errorf("run-a pods survived: %v", got)
	}
	if !got["b1"] {
		t.Errorf("run-b pod wrongly deleted: %v", got)
	}
}

// TestTaskPodPresence covers the three-way liveness answer the reapers act on.
// A Pending/Running pod means the dispatch landed and the node is just slow
// (#461), so every reaper DEFERS. A pod that is present but no longer running is
// the reconciler's to settle from its termination log, so it must be
// distinguishable from a genuine absence — collapsing those two into one "not
// active" bool is what let the pod-lost reaper delete a finished pod and destroy
// the evidence of its outcome.
func TestTaskPodPresence(t *testing.T) {
	tests := []struct {
		name  string
		phase corev1.PodPhase
		want  PodPresence
	}{
		{"pending pod is live (defer)", corev1.PodPending, PodPresenceLive},
		{"running pod is live (defer)", corev1.PodRunning, PodPresenceLive},
		{"failed pod is present-but-terminal", corev1.PodFailed, PodPresenceTerminal},
		{"succeeded pod is present-but-terminal", corev1.PodSucceeded, PodPresenceTerminal},
		{"unknown-phase pod is present, so not absent", corev1.PodUnknown, PodPresenceTerminal},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cs := fake.NewSimpleClientset(taskPod("p", "run-a", "extract", 1, tc.phase))
			e := NewKubernetesExecutor(cs, "leoflow")
			got, err := e.TaskPodPresence(context.Background(), "run-a", "extract", 1)
			if err != nil {
				t.Fatalf("TaskPodPresence: %v", err)
			}
			if got != tc.want {
				t.Errorf("TaskPodPresence(phase=%s) = %v, want %v", tc.phase, got, tc.want)
			}
		})
	}
}

// TestTaskPodPresence_LiveWinsOverTerminalSibling: a lingering terminal pod must
// never mask a live one for the same attempt — presence answers about the
// attempt, and any live pod for it means work may still be running.
func TestTaskPodPresence_LiveWinsOverTerminalSibling(t *testing.T) {
	cs := fake.NewSimpleClientset(
		taskPod("dead", "run-a", "extract", 1, corev1.PodFailed),
		taskPod("live", "run-a", "extract", 1, corev1.PodRunning),
	)
	e := NewKubernetesExecutor(cs, "leoflow")
	got, err := e.TaskPodPresence(context.Background(), "run-a", "extract", 1)
	if err != nil {
		t.Fatalf("TaskPodPresence: %v", err)
	}
	if got != PodPresenceLive {
		t.Errorf("TaskPodPresence = %v, want %v", got, PodPresenceLive)
	}
}

// TestTaskPodPresence_PinsTryNumber is the #723 selector lock: a try-1 pod
// lingers Pending, but a liveness query for try 2 must NOT match it. Asking
// about the attempt the reaper is about to fail (try 2) must report an absence,
// so the reaper neither false-defers on a stale older attempt's pod nor mistakes
// it for that attempt's own outcome.
func TestTaskPodPresence_PinsTryNumber(t *testing.T) {
	cs := fake.NewSimpleClientset(taskPod("try1", "run-a", "extract", 1, corev1.PodPending))
	e := NewKubernetesExecutor(cs, "leoflow")

	got, err := e.TaskPodPresence(context.Background(), "run-a", "extract", 2)
	if err != nil {
		t.Fatalf("TaskPodPresence: %v", err)
	}
	if got != PodPresenceAbsent {
		t.Errorf("#723: a try-2 liveness query saw %v; try-number must be pinned so try 1's pod is invisible", got)
	}
	// Sanity: the same query for the attempt that DOES have a Pending pod is live.
	got, err = e.TaskPodPresence(context.Background(), "run-a", "extract", 1)
	if err != nil {
		t.Fatalf("TaskPodPresence(try1): %v", err)
	}
	if got != PodPresenceLive {
		t.Errorf("try-1's own Pending pod read as %v, want %v", got, PodPresenceLive)
	}
}

// TestTaskPodPresence_AbsentIsNotTerminal: no pod at all is a genuine absence —
// the state that authorizes a pod-lost reap — and must not be reported as a
// present-but-terminal pod.
func TestTaskPodPresence_AbsentIsNotTerminal(t *testing.T) {
	cs := fake.NewSimpleClientset()
	e := NewKubernetesExecutor(cs, "leoflow")
	got, err := e.TaskPodPresence(context.Background(), "run-a", "extract", 1)
	if err != nil {
		t.Fatalf("TaskPodPresence: %v", err)
	}
	if got != PodPresenceAbsent {
		t.Errorf("TaskPodPresence with no pods = %v, want %v", got, PodPresenceAbsent)
	}
}

// --- Teardown preserves a terminal pod's outcome record (#928) --------------

// TestDeleteTaskPod_PreservesTerminalPodCarryingOutcomeRecord is the #928
// invariant at the delete site: a reaper's teardown exists to stop a container
// that is still running (#474), and a pod that already reached a terminal phase
// has none. Deleting it therefore accomplishes exactly one thing — destroying
// the durable outcome record on its termination message, the only evidence the
// reconciler could settle the attempt from (ADR 0052). The pod must survive the
// teardown with its record intact, whatever the reaper's own decision was.
func TestDeleteTaskPod_PreservesTerminalPodCarryingOutcomeRecord(t *testing.T) {
	pod := withRecord(taskPod("finished", "run-a", "extract", 1, corev1.PodSucceeded), taskoutcome.Succeeded())
	cs := fake.NewSimpleClientset(pod)
	e := NewKubernetesExecutor(cs, "leoflow")

	if err := e.DeleteTaskPod(context.Background(), "run-a", "extract", 1); err != nil {
		t.Fatalf("DeleteTaskPod: %v", err)
	}
	got, err := cs.CoreV1().Pods("leoflow").Get(context.Background(), "finished", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("#928: the terminal pod was deleted, destroying the outcome record the reconciler settles from: %v", err)
	}
	rec, ok := outcomeRecord(got)
	if !ok || rec.Outcome != taskoutcome.Success {
		t.Errorf("surviving pod lost its outcome record: rec=%+v ok=%v", rec, ok)
	}
}

// TestDeleteTaskPod_PhaseDecidesTheTeardown pins which phases a teardown may
// delete. Pending/Running have a container to stop, so the #474 teardown is the
// whole point. Succeeded/Failed have none and carry the attempt's outcome, so
// they are the reconciler's — it both settles and garbage-collects them.
//
// Unknown is deliberately on the DELETE side even though pod presence classifies
// it as present-but-terminal (see PodPresence): a pod in Unknown may still have a
// running container, which is precisely the case the teardown exists for, and
// classifyPod groups Unknown with Pending/Running, so the reconciler neither
// settles nor collects it — skipping it here would leak it with nothing to stop
// the container it may still be running.
// TestDeleteTaskPod_PreservesARecordBearingPodWhateverItsPhase pins the
// invariant the guard exists for — never destroy an outcome record — rather than
// the phase that stands in for it. Phase is only a sound proxy while a task pod
// has one container and RestartPolicy Never, which makes "the task container
// terminated" imply "the phase is terminal". Add a second container and the
// implication breaks: a service mesh injects a sidecar (reachable by an author
// through execution annotations), the task container terminates and writes its
// record, the sidecar keeps running, and the phase stays Running forever. That
// is the one case where the record is the ONLY settle path, so it is the worst
// possible pod to delete.
func TestDeleteTaskPod_PreservesARecordBearingPodWhateverItsPhase(t *testing.T) {
	t.Parallel()

	rec, err := taskoutcome.Succeeded().Encode()
	if err != nil {
		t.Fatalf("encoding the outcome record: %v", err)
	}
	pod := taskPod("sidecar", "sidecar-run", "work", 1, corev1.PodRunning)
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:  taskContainerName,
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: rec}},
	}, {
		Name:  "istio-proxy",
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	}}

	cs := fake.NewSimpleClientset(pod)
	e := &KubernetesExecutor{clientset: cs, namespace: "leoflow"}
	if derr := e.DeleteTaskPod(context.Background(), "sidecar-run", "work", 1); derr != nil {
		t.Fatalf("DeleteTaskPod: %v", derr)
	}
	if _, gerr := cs.CoreV1().Pods("leoflow").Get(context.Background(), pod.Name, metav1.GetOptions{}); gerr != nil {
		t.Fatalf("#928: a pod carrying a decodable outcome record was deleted despite the record being the only settle path: %v", gerr)
	}
}

func TestDeleteTaskPod_PhaseDecidesTheTeardown(t *testing.T) {
	tests := []struct {
		name    string
		phase   corev1.PodPhase
		deleted bool
	}{
		{"pending pod is torn down (a container may yet start)", corev1.PodPending, true},
		{"running pod is torn down (#474: stop the container)", corev1.PodRunning, true},
		{"succeeded pod survives (its record is the reconciler's)", corev1.PodSucceeded, false},
		{"failed pod survives (its record is the reconciler's)", corev1.PodFailed, false},
		{"unknown pod is torn down (may still be running; nothing else collects it)", corev1.PodUnknown, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cs := fake.NewSimpleClientset(taskPod("p", "run-a", "extract", 1, tc.phase))
			e := NewKubernetesExecutor(cs, "leoflow")
			if err := e.DeleteTaskPod(context.Background(), "run-a", "extract", 1); err != nil {
				t.Fatalf("DeleteTaskPod: %v", err)
			}
			survived := podNames(t, cs)["p"]
			if tc.deleted && survived {
				t.Errorf("phase %s: pod survived the teardown, want deleted", tc.phase)
			}
			if !tc.deleted && !survived {
				t.Errorf("phase %s: pod was deleted, want preserved for the reconciler", tc.phase)
			}
		})
	}
}

// TestDeleteRunPods_PreservesTerminalPodsPerPod is the run-scoped half of #928.
// The orphan-run reaper abandons a whole run at the 5-minute threshold with no
// presence read at all, so the rule has to apply per pod inside the run and not
// just to the per-attempt delete: the run's still-running pods are torn down
// (#474) while its finished ones keep their outcome records for the reconciler.
func TestDeleteRunPods_PreservesTerminalPodsPerPod(t *testing.T) {
	cs := fake.NewSimpleClientset(
		withRecord(taskPod("done-ok", "run-a", "extract", 1, corev1.PodSucceeded), taskoutcome.Succeeded()),
		withRecord(taskPod("done-bad", "run-a", "load", 1, corev1.PodFailed), taskoutcome.FailedWith(2)),
		taskPod("still-running", "run-a", "transform", 1, corev1.PodRunning),
		taskPod("still-pending", "run-a", "publish", 1, corev1.PodPending),
		taskPod("other-run", "run-b", "extract", 1, corev1.PodRunning),
	)
	e := NewKubernetesExecutor(cs, "leoflow")
	if err := e.DeleteRunPods(context.Background(), "run-a"); err != nil {
		t.Fatalf("DeleteRunPods: %v", err)
	}
	got := podNames(t, cs)
	for _, keep := range []string{"done-ok", "done-bad"} {
		if !got[keep] {
			t.Errorf("#928: terminal pod %q was deleted by the run-scoped teardown, destroying its outcome record: %v", keep, got)
		}
	}
	for _, gone := range []string{"still-running", "still-pending"} {
		if got[gone] {
			t.Errorf("live pod %q survived the run teardown; #474 requires its container be stopped: %v", gone, got)
		}
	}
	if !got["other-run"] {
		t.Errorf("run-b pod wrongly deleted: %v", got)
	}
}
