package executor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/dexadata/dexaflow/internal/taskoutcome"
)

func podPhase(phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{Status: corev1.PodStatus{Phase: phase}}
}

func podWaiting(reason string) *corev1.Pod {
	return podWaitingMsg(reason, "")
}

func podWaitingMsg(reason, message string) *corev1.Pod {
	return &corev1.Pod{Status: corev1.PodStatus{
		Phase: corev1.PodPending,
		ContainerStatuses: []corev1.ContainerStatus{
			{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason, Message: message}}},
		},
	}}
}

func TestClassifyPod(t *testing.T) {
	cases := map[string]struct {
		pod          *corev1.Pod
		wantTerminal bool
		wantSettle   settleKind
	}{
		"failed phase":       {podPhase(corev1.PodFailed), true, settleFailed},
		"succeeded phase":    {podPhase(corev1.PodSucceeded), true, settleNothing},
		"running":            {podPhase(corev1.PodRunning), false, settleNothing},
		"pending":            {podPhase(corev1.PodPending), false, settleNothing},
		"image pull backoff": {podWaiting("ImagePullBackOff"), true, settleFailed},
		"err image pull":     {podWaiting("ErrImagePull"), true, settleFailed},
		"crash loop":         {podWaiting("CrashLoopBackOff"), true, settleFailed},
		"benign waiting":     {podWaiting("ContainerCreating"), false, settleNothing},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := classifyPod(c.pod)
			if got.terminal != c.wantTerminal || got.settle != c.wantSettle {
				t.Errorf("classifyPod = {terminal:%v settle:%v}, want {terminal:%v settle:%v}",
					got.terminal, got.settle, c.wantTerminal, c.wantSettle)
			}
		})
	}
}

// settledOutcome captures one call the reconciler made to the outcome reporter.
type settledOutcome struct {
	kind      settleKind
	tryNumber int
	reason    string
	at        time.Time
}

type fakeReporter struct{ settled map[string]settledOutcome }

func (f *fakeReporter) put(id string, o settledOutcome) {
	if f.settled == nil {
		f.settled = map[string]settledOutcome{}
	}
	f.settled[id] = o
}

func (f *fakeReporter) FailTask(_ context.Context, id string, tryNumber, _ int, reason string) error {
	f.put(id, settledOutcome{kind: settleFailed, tryNumber: tryNumber, reason: reason})
	return nil
}

func (f *fakeReporter) SucceedTask(_ context.Context, id string, tryNumber, _ int) error {
	f.put(id, settledOutcome{kind: settleSucceeded, tryNumber: tryNumber})
	return nil
}

func (f *fakeReporter) RescheduleTask(_ context.Context, id string, tryNumber, _ int, at time.Time) error {
	f.put(id, settledOutcome{kind: settleReschedule, tryNumber: tryNumber, at: at})
	return nil
}

func managedPod(name, tiID string, phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "leoflow",
			Labels: map[string]string{
				"leoflow.io/run-id":     "r1",
				"leoflow.io/try-number": "1",
			},
			Annotations: map[string]string{"leoflow.io/task-instance-id": tiID},
		},
		Status: corev1.PodStatus{Phase: phase},
	}
}

// withRecord attaches a terminated task container carrying the encoded outcome
// record to a pod, so classifyPod reads it as the source of truth (ADR 0052).
func withRecord(pod *corev1.Pod, rec taskoutcome.Record) *corev1.Pod {
	enc, err := rec.Encode()
	if err != nil {
		panic(err)
	}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "task",
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Message: enc,
		}},
	}}
	return pod
}

func TestReconcileReportsOnlyFailedPods(t *testing.T) {
	cs := fake.NewClientset(
		managedPod("p-fail", "ti-fail", corev1.PodFailed),
		managedPod("p-run", "ti-run", corev1.PodRunning),
		managedPod("p-ok", "ti-ok", corev1.PodSucceeded),
	)
	reporter := &fakeReporter{}
	r := NewReconciler(cs, "leoflow", reporter)

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got, ok := reporter.settled["ti-fail"]
	if !ok || got.kind != settleFailed {
		t.Errorf("failed pod's task instance should be settled failed, got %+v (ok=%v)", got, ok)
	}
	if got.tryNumber != 1 {
		t.Errorf("settle must carry the pod's try-number, got %d", got.tryNumber)
	}
	if len(reporter.settled) != 1 {
		t.Errorf("only the failed pod should be settled, got %v", reporter.settled)
	}
}

// TestReconcileRecoversSuccessFromRecord is the headline of ADR 0052: a pod that
// Kubernetes reports as Failed, but whose task container left a `success` outcome
// record (the report was lost, the work was not), is settled SUCCEEDED — not
// failed. This is the zombie-task false-negative, closed.
func TestReconcileRecoversSuccessFromRecord(t *testing.T) {
	cs := fake.NewClientset(
		withRecord(managedPod("p-lost", "ti-lost", corev1.PodFailed), taskoutcome.Succeeded()),
	)
	reporter := &fakeReporter{}
	r := NewReconciler(cs, "leoflow", reporter)

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got, ok := reporter.settled["ti-lost"]
	if !ok || got.kind != settleSucceeded {
		t.Errorf("a Failed pod with a success record must settle succeeded, got %+v (ok=%v)", got, ok)
	}
}

// TestReconcileRecordDrivesFailedAndReschedule: a `failed` record settles failed
// (naming the exit code), and a `reschedule` record settles up_for_reschedule with
// the record's next-poke time — never as a failure (#386 exclusion holds).
func TestReconcileRecordDrivesFailedAndReschedule(t *testing.T) {
	at := time.Date(2099, 1, 2, 3, 4, 5, 0, time.UTC)
	cs := fake.NewClientset(
		withRecord(managedPod("p-fail", "ti-fail", corev1.PodFailed), taskoutcome.FailedWith(42)),
		withRecord(managedPod("p-resched", "ti-resched", corev1.PodRunning), taskoutcome.RescheduledAt(at)),
	)
	reporter := &fakeReporter{}
	r := NewReconciler(cs, "leoflow", reporter)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := reporter.settled["ti-fail"]; got.kind != settleFailed || !strings.Contains(got.reason, "exit 42") {
		t.Errorf("failed record should settle failed naming the exit code, got %+v", got)
	}
	got := reporter.settled["ti-resched"]
	if got.kind != settleReschedule || !got.at.Equal(at) {
		t.Errorf("reschedule record should settle up_for_reschedule at %v, got %+v", at, got)
	}
}

// TestReconcileUndecodableRecordFallsBackToPhase: a termination message that is not
// a valid outcome record (an old agent, a log tail) is ignored and the pod is
// classified by phase — here a Failed pod settles failed.
func TestReconcileUndecodableRecordFallsBackToPhase(t *testing.T) {
	pod := managedPod("p-garbage", "ti-garbage", corev1.PodFailed)
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:  "task",
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: "Traceback (most recent call last): ..."}},
	}}
	cs := fake.NewClientset(pod)
	reporter := &fakeReporter{}
	r := NewReconciler(cs, "leoflow", reporter)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := reporter.settled["ti-garbage"]; got.kind != settleFailed {
		t.Errorf("an undecodable message must fall back to phase (failed), got %+v", got)
	}
}

// TestReconcileSkipsSettleWithoutTryNumber: a pod missing the try-number label
// cannot be attempt-guarded, so the reconciler must NOT settle it (risking a
// clobber of a different attempt) — but it must still age out normally rather than
// leak forever (the label will never appear); the heartbeat reaper settles the TI.
func TestReconcileSkipsSettleWithoutTryNumber(t *testing.T) {
	pod := withRecord(managedPod("p-nolabel", "ti-nolabel", corev1.PodFailed), taskoutcome.Succeeded())
	delete(pod.Labels, "leoflow.io/try-number")
	pod.CreationTimestamp = metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) // aged past the grace period
	cs := fake.NewClientset(pod)
	reporter := &fakeReporter{}
	r := NewReconciler(cs, "leoflow", reporter)
	r.now = func() time.Time { return time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC) }

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(reporter.settled) != 0 {
		t.Errorf("a pod without a try-number must not be settled, got %v", reporter.settled)
	}
	if _, err := cs.CoreV1().Pods("leoflow").Get(context.Background(), "p-nolabel", metav1.GetOptions{}); err == nil {
		t.Error("an aged label-less pod must still be garbage-collected (no forever-leak)")
	}
}

// TestReconcileCollectsReschedulePodImmediately pins the ADR 0052 B1 fix: a
// reschedule pod is collected as soon as its outcome is settled — NOT kept for the
// grace period — because reschedule redispatch reuses the same try_number, so a
// lingering poke pod would re-park the re-dispatched live attempt and flap the
// sensor. Here the pod is young (well within the grace period) yet must be gone.
func TestReconcileCollectsReschedulePodImmediately(t *testing.T) {
	now := time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC)
	pod := withRecord(managedPod("p-poke", "ti-poke", corev1.PodFailed), taskoutcome.RescheduledAt(now.Add(time.Minute)))
	pod.CreationTimestamp = metav1.NewTime(now.Add(-time.Second)) // brand new
	cs := fake.NewClientset(pod)
	reporter := &fakeReporter{}
	r := NewReconciler(cs, "leoflow", reporter)
	r.now = func() time.Time { return now }

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := reporter.settled["ti-poke"]; got.kind != settleReschedule {
		t.Errorf("reschedule pod must be settled up_for_reschedule, got %+v", got)
	}
	if _, err := cs.CoreV1().Pods("leoflow").Get(context.Background(), "p-poke", metav1.GetOptions{}); err == nil {
		t.Error("a settled reschedule pod must be collected immediately, not left to linger and re-park the next attempt")
	}
}

func agedPod(name string, phase corev1.PodPhase, created time.Time) *corev1.Pod {
	p := managedPod(name, "ti-"+name, phase)
	p.CreationTimestamp = metav1.NewTime(created)
	return p
}

func TestReconcileGarbageCollectsOldTerminalPods(t *testing.T) {
	now := time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC)
	cs := fake.NewClientset(
		agedPod("old-success", corev1.PodSucceeded, now.Add(-30*time.Minute)),
		agedPod("old-failed", corev1.PodFailed, now.Add(-30*time.Minute)),
		agedPod("recent-success", corev1.PodSucceeded, now.Add(-1*time.Minute)),
		agedPod("running", corev1.PodRunning, now.Add(-30*time.Minute)),
	)
	r := NewReconciler(cs, "leoflow", &fakeReporter{})
	r.now = func() time.Time { return now }
	r.ttl = 10 * time.Minute

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	remaining := map[string]bool{}
	pods, _ := cs.CoreV1().Pods("leoflow").List(context.Background(), metav1.ListOptions{})
	for _, p := range pods.Items {
		remaining[p.Name] = true
	}
	if remaining["old-success"] || remaining["old-failed"] {
		t.Errorf("old terminal pods should be GC'd, remaining: %v", remaining)
	}
	if !remaining["recent-success"] || !remaining["running"] {
		t.Errorf("recent and running pods must be kept, remaining: %v", remaining)
	}
}

// podTerminated builds a pod whose task container terminated with the given
// reason/exit code, the shape Kubernetes reports for an OOM kill, a non-zero
// exit, or a container the kubelet could not keep alive.
func podTerminated(reason string, exitCode int32, podReason string) *corev1.Pod {
	return &corev1.Pod{Status: corev1.PodStatus{
		Phase:  corev1.PodFailed,
		Reason: podReason,
		ContainerStatuses: []corev1.ContainerStatus{{
			Name: taskContainerName,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				Reason: reason, ExitCode: exitCode,
			}},
		}},
	}}
}

// TestClassifyPodSurfacesAgentBootstrapReason is the heart of the
// operator-blindness fix: when the agent died before registering but managed to
// classify why, that classification — not a generic "pod failed" — is what the
// task instance is failed with.
func TestClassifyPodSurfacesAgentBootstrapReason(t *testing.T) {
	const reason = "the control plane rejected this pod's projected ServiceAccount token; check RBAC."
	enc, err := taskoutcome.FailedBecause(reason).Encode()
	if err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{Status: corev1.PodStatus{
		Phase: corev1.PodFailed,
		ContainerStatuses: []corev1.ContainerStatus{{
			Name:  taskContainerName,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: enc, ExitCode: 1}},
		}},
	}}
	got := classifyPod(pod)
	if got.settle != settleFailed {
		t.Fatalf("settle = %v, want settleFailed", got.settle)
	}
	if got.reason != reason {
		t.Errorf("reason = %q, want the agent's classification %q", got.reason, reason)
	}
}

// TestPodFailureReasonNamesTheKubernetesCause covers the failures the agent can
// never report because it never ran: the reason must name what Kubernetes saw
// instead of collapsing to "pod failed".
func TestPodFailureReasonNamesTheKubernetesCause(t *testing.T) {
	cases := []struct {
		name string
		pod  *corev1.Pod
		want string
	}{
		{"oom killed", podTerminated("OOMKilled", 137, ""), "OOMKilled"},
		{"non-zero exit", podTerminated("Error", 43, ""), "exit 43"},
		{"evicted", podPhase(corev1.PodFailed), "pod failed"},
		{"pod level reason", &corev1.Pod{Status: corev1.PodStatus{
			Phase: corev1.PodFailed, Reason: "Evicted",
		}}, "Evicted"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := podFailureReason(c.pod); !strings.Contains(got, c.want) {
				t.Errorf("podFailureReason = %q, want it to mention %q", got, c.want)
			}
		})
	}
}

// TestPodFailureReasonIsBounded keeps a hostile or runaway kubelet message from
// becoming an unbounded string on an end-user-visible field.
func TestPodFailureReasonIsBounded(t *testing.T) {
	pod := &corev1.Pod{Status: corev1.PodStatus{
		Phase:  corev1.PodFailed,
		Reason: strings.Repeat("x", 5000),
	}}
	if got := podFailureReason(pod); len(got) > taskoutcome.MaxReasonLen {
		t.Errorf("podFailureReason is %d bytes, over the %d cap", len(got), taskoutcome.MaxReasonLen)
	}
}

// TestPodWaitingReasonExplainsItself keeps the unrecoverable-waiting reasons from
// reaching the operator as a bare Kubernetes enum with no guidance.
func TestPodWaitingReasonExplainsItself(t *testing.T) {
	got := classifyPod(podWaiting("ImagePullBackOff"))
	if !strings.Contains(got.reason, "ImagePullBackOff") {
		t.Errorf("reason = %q, want it to name the waiting reason", got.reason)
	}
	if !strings.Contains(got.reason, "image") {
		t.Errorf("reason = %q, want operator-facing context about the image", got.reason)
	}
}

// TestClassifyPodSurfacesNonRootConfigError pins the runAsNonRoot case of
// CreateContainerConfigError: a root task image under the non-root default is
// refused by the kubelet before the container starts, so no agent ever reports a
// cause. The classified reason must name the non-root cause and carry both the
// image-side and the operator-side fixes, not degrade to the generic bucket.
func TestClassifyPodSurfacesNonRootConfigError(t *testing.T) {
	pod := podWaitingMsg("CreateContainerConfigError",
		"container has runAsNonRoot and image will run as root")
	got := classifyPod(pod)
	if !got.terminal || got.settle != settleFailed {
		t.Fatalf("classifyPod = {terminal:%v settle:%v}, want a terminal failure",
			got.terminal, got.settle)
	}
	for _, want := range []string{"runAsNonRoot", "USER 65532", "taskPodSecurity.runAsNonRoot"} {
		if !strings.Contains(got.reason, want) {
			t.Errorf("reason = %q, want it to mention %q", got.reason, want)
		}
	}
}

// TestClassifyPodConfigErrorWithoutNonRoot keeps the generic
// CreateContainerConfigError path (a missing ConfigMap/Secret, a bad env-var
// reference) actionable without misattributing it to the non-root cause.
func TestClassifyPodConfigErrorWithoutNonRoot(t *testing.T) {
	pod := podWaitingMsg("CreateContainerConfigError",
		`configmap "missing" not found`)
	got := classifyPod(pod)
	if !got.terminal || got.settle != settleFailed {
		t.Fatalf("classifyPod = {terminal:%v settle:%v}, want a terminal failure",
			got.terminal, got.settle)
	}
	if !strings.Contains(got.reason, "CreateContainerConfigError") {
		t.Errorf("reason = %q, want it to name the waiting reason", got.reason)
	}
	if strings.Contains(got.reason, "runAsNonRoot") {
		t.Errorf("reason = %q, must not attribute an unrelated config error to non-root", got.reason)
	}
}

// TestReconcileRecordsCompletedSweep: the reconciler stamps the time of its last
// COMPLETED sweep — the task-pod set was listed and every pod visited — and only
// then. A sweep that could not list pods records nothing. The stamp is what the
// reaper's leader-settling gate reads to know a post-leadership sweep has had
// its chance to recover durable outcomes before anything is declared lost.
func TestReconcileRecordsCompletedSweep(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	r := NewReconciler(fake.NewClientset(managedPod("p-ok", "ti-ok", corev1.PodSucceeded)), "leoflow", &fakeReporter{})
	r.now = func() time.Time { return now }
	if !r.LastSweepCompletedAt().IsZero() {
		t.Fatalf("no sweep yet, got %v", r.LastSweepCompletedAt())
	}
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := r.LastSweepCompletedAt(); !got.Equal(now) {
		t.Errorf("LastSweepCompletedAt = %v, want %v", got, now)
	}

	failing := NewReconciler(fake.NewClientset(), "leoflow", &fakeReporter{})
	failing.clientset.(*fake.Clientset).PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("apiserver down")
	})
	if err := failing.Reconcile(context.Background()); err == nil {
		t.Fatal("a failed LIST must surface")
	}
	if !failing.LastSweepCompletedAt().IsZero() {
		t.Errorf("a sweep that could not list pods must record nothing, got %v", failing.LastSweepCompletedAt())
	}
}

// TestReconcileSettlesTimeoutReasonFromRecord closes the loop on #930: an
// execution_timeout whose report never landed is settled from the durable record
// alone, and the operator sees the agent's diagnosis rather than the generic
// "task failed (exit N)". This is the pod the kubelet leaves behind when it kills
// the agent mid-report-retry — phase Failed, no report ever delivered.
func TestReconcileSettlesTimeoutReasonFromRecord(t *testing.T) {
	const reason = "execution_timeout: task exceeded 10s limit"
	// 255, not 137: the agent's own cancel kills the child, a signal death reports
	// exit code -1, and the agent clamps -1 to 255 before recording it.
	pod := withRecord(managedPod("p-timeout", "ti-timeout", corev1.PodFailed),
		taskoutcome.FailedBecauseWith(255, reason))
	reporter := &fakeReporter{}
	r := NewReconciler(fake.NewClientset(pod), "leoflow", reporter)

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got, ok := reporter.settled["ti-timeout"]
	if !ok || got.kind != settleFailed {
		t.Fatalf("timed-out pod's task instance should be settled failed, got %+v (ok=%v)", got, ok)
	}
	if !strings.Contains(got.reason, "execution_timeout:") {
		t.Errorf("reason = %q, want the agent's execution_timeout diagnosis", got.reason)
	}
}

// TestRecordFailureReasonPrefersReasonOverExitCode pins the precedence a
// reason-carrying failure record depends on (#930): with BOTH a reason and an
// exit code present, the classification wins. Flipping this would silently
// regress the timeout diagnosis back to "task failed (exit 255)" (255 is the
// production value: the agent clamps a signal death's -1).
func TestRecordFailureReasonPrefersReasonOverExitCode(t *testing.T) {
	rec := taskoutcome.FailedBecauseWith(255, "execution_timeout: task exceeded 10s limit")
	if got := recordFailureReason(rec); !strings.Contains(got, "execution_timeout:") {
		t.Errorf("recordFailureReason = %q, want the reason, not the exit code", got)
	}
	if rec.ExitCode == nil || *rec.ExitCode != 255 {
		t.Errorf("exit_code = %v, want 255 kept in the record alongside the reason", rec.ExitCode)
	}
}

// TestRecordFailureReasonBoundsTheReason: recordFailureReason is the one reason
// producer here that reads a value the reconciler did not build. Every sibling
// (podFailureReason, waitingFailureReason) bounds its output because kubelet
// fields are unbounded input for an end-user-visible value — and a record's
// reason is no different: the agent bounds what IT writes, but a task can write
// its own termination message before being killed, and the kubelet's ceiling
// there is ~4 KiB, seventeen times the record's own cap. Decode does not bound
// it either, so the reader must.
func TestRecordFailureReasonBoundsTheReason(t *testing.T) {
	msg, err := json.Marshal(map[string]any{
		"v":       taskoutcome.Version,
		"outcome": "failed",
		"reason":  strings.Repeat("A", 4000),
	})
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := taskoutcome.Decode(string(msg))
	if !ok {
		t.Fatalf("a task-written failure record must still decode: %s", msg)
	}
	if got := recordFailureReason(rec); len(got) > taskoutcome.MaxReasonLen {
		t.Errorf("recordFailureReason length = %d, want <= %d", len(got), taskoutcome.MaxReasonLen)
	}
}

// TestClassifyPodPrefersPodOOMOverABareExitCode is the regression test for the
// half of #1216 that turned out to be the real defect.
//
// A surviving agent always writes an outcome record, and the record won
// unconditionally. So when the kernel killed the task for memory, the operator
// read "task failed (exit 255)" while the pod status sitting next to it said
// OOMKilled. The reconciler had the answer and preferred the number.
//
// 255 rather than 137 on purpose: Go reports a signaled child as ExitCode()
// -1, which the agent clamps to 255. 128+signal is a shell convention and shows
// up only when a shell sits between, so the exit code cannot be pattern-matched
// for this and the pod's Reason is the only reliable signal.
func TestClassifyPodPrefersPodOOMOverABareExitCode(t *testing.T) {
	oomPod := func(rec taskoutcome.Record, podReason string) *corev1.Pod {
		enc, err := rec.Encode()
		if err != nil {
			t.Fatal(err)
		}
		return &corev1.Pod{Status: corev1.PodStatus{
			Phase: corev1.PodFailed,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: taskContainerName,
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
					Message: enc, ExitCode: 137, Reason: podReason,
				}},
			}},
		}}
	}

	// THE PRODUCTION SHAPE, and the fixture below used to hide a bug by not
	// being it. On the path that reaches this branch the agent survived its
	// child and exited on its own, so the CONTAINER exits 1 (the agent's clean
	// exit) while the RECORD carries 255 (the task's clamped signal death). The
	// first version of this test set the container to 137, a shape that only
	// occurs in the narrow group-kill-during-report case, and asserted only that
	// the string contained "OOMKilled" — so it passed while the message said
	// "exit 1", presenting the agent's status as the task's.
	realistic := &corev1.Pod{Status: corev1.PodStatus{
		Phase: corev1.PodFailed,
		ContainerStatuses: []corev1.ContainerStatus{{
			Name: taskContainerName,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				Message:  mustEncode(t, taskoutcome.FailedBecauseWith(255, "")),
				ExitCode: 1, Reason: "OOMKilled",
			}},
		}},
	}}
	if got := classifyPod(realistic); !strings.Contains(got.reason, "exit 255") {
		t.Errorf("reason = %q; it reports the CONTAINER's exit code, which here is the agent's clean exit, not the task's", got.reason)
	}

	// The case that was broken: a record with an exit code and no classification,
	// beside a pod that says OOMKilled.
	got := classifyPod(oomPod(taskoutcome.FailedBecauseWith(255, ""), "OOMKilled"))
	if got.settle != settleFailed {
		t.Fatalf("settle = %v, want settleFailed", got.settle)
	}
	if !strings.Contains(got.reason, "OOMKilled") {
		t.Errorf("reason = %q; the pod said OOMKilled and the reconciler reported the exit code instead", got.reason)
	}
	if !strings.Contains(got.reason, "memory limit") {
		t.Errorf("reason = %q; it must tell the operator what to change", got.reason)
	}

	// A classification the AGENT made still wins: it describes a cause the
	// control plane cannot observe, and an execution_timeout that also tripped
	// the memory ceiling is a timeout first.
	const agentReason = "execution_timeout: task exceeded 30s limit"
	got = classifyPod(oomPod(taskoutcome.FailedBecauseWith(255, agentReason), "OOMKilled"))
	if got.reason != agentReason {
		t.Errorf("reason = %q, want the agent's own classification %q", got.reason, agentReason)
	}

	// And an ordinary failure with no OOM on the pod keeps the exit-code string.
	got = classifyPod(oomPod(taskoutcome.FailedBecauseWith(1, ""), "Error"))
	if !strings.Contains(got.reason, "exit 1") {
		t.Errorf("reason = %q, want the plain exit-code rendering for a non-OOM failure", got.reason)
	}
	if strings.Contains(got.reason, "OOMKilled") {
		t.Errorf("reason = %q; a pod that was not OOMKilled must not be described as one", got.reason)
	}
}

func mustEncode(t *testing.T, rec taskoutcome.Record) string {
	t.Helper()
	enc, err := rec.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return enc
}

// epochFencingReporter settles like the storage guard: only the row's
// current (try, epoch).
type epochFencingReporter struct {
	try, epoch int
	settled    []string
}

func (f *epochFencingReporter) match(try, epoch int) bool { return try == f.try && epoch == f.epoch }

func (f *epochFencingReporter) FailTask(_ context.Context, _ string, try, epoch int, _ string) error {
	if f.match(try, epoch) {
		f.settled = append(f.settled, "failed")
	}
	return nil
}

func (f *epochFencingReporter) SucceedTask(_ context.Context, _ string, try, epoch int) error {
	if f.match(try, epoch) {
		f.settled = append(f.settled, "success")
	}
	return nil
}

func (f *epochFencingReporter) RescheduleTask(_ context.Context, _ string, try, epoch int, _ time.Time) error {
	if f.match(try, epoch) {
		f.settled = append(f.settled, "reschedule")
	}
	return nil
}

// TestReconcileDoesNotSettleReplacementFromSupersededPod is #1130: a terminal
// pod of the superseded attempt, carrying a SUCCESS record, labels
// try-number=1 and no epoch, against a TI on try 1 epoch 1 in queued. The
// reconciler reads the attempt from the labels (epoch 0), so the settle is
// fenced and the replacement is not marked success from work it never did.
func TestReconcileDoesNotSettleReplacementFromSupersededPod(t *testing.T) {
	stale := withRecord(managedPod("stale", "ti-1", corev1.PodSucceeded), taskoutcome.Succeeded())
	reporter := &epochFencingReporter{try: 1, epoch: 1}
	r := NewReconciler(fake.NewClientset(stale), "leoflow", reporter)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(reporter.settled) != 0 {
		t.Fatalf("#1130: the superseded pod's record must not settle the replacement, settled %v", reporter.settled)
	}

	// The replacement's own pod, labeled with its epoch, does settle.
	own := withRecord(managedPod("own", "ti-1", corev1.PodSucceeded), taskoutcome.Succeeded())
	own.Labels[podLabelAttemptEpoch] = "1"
	r = NewReconciler(fake.NewClientset(own), "leoflow", reporter)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(reporter.settled) != 1 || reporter.settled[0] != "success" {
		t.Fatalf("the attempt's own pod must settle it, settled %v", reporter.settled)
	}
}

// TestReconcileSkipsUnparseableAttemptEpoch: a pod whose epoch label is
// present but not a number cannot be attributed to an attempt, so it is not
// settled (the same rule as a missing try-number).
func TestReconcileSkipsUnparseableAttemptEpoch(t *testing.T) {
	pod := withRecord(managedPod("odd", "ti-1", corev1.PodSucceeded), taskoutcome.Succeeded())
	pod.Labels[podLabelAttemptEpoch] = "x"
	reporter := &epochFencingReporter{try: 1, epoch: 0}
	r := NewReconciler(fake.NewClientset(pod), "leoflow", reporter)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(reporter.settled) != 0 {
		t.Fatalf("an unparseable epoch label must not settle, settled %v", reporter.settled)
	}
}

// TestReconcileSettlesLegacyPodOnEpochZeroRow pins the upgrade path: a pod
// created by a release without the attempt epoch carries no epoch label and
// counts as epoch 0, so its record still settles a row no post-upgrade
// dispatch has claimed (epoch 0), exactly as before the upgrade.
func TestReconcileSettlesLegacyPodOnEpochZeroRow(t *testing.T) {
	legacy := withRecord(managedPod("legacy", "ti-1", corev1.PodSucceeded), taskoutcome.Succeeded())
	if _, has := legacy.Labels[podLabelAttemptEpoch]; has {
		t.Fatalf("precondition: the legacy pod has no epoch label")
	}
	reporter := &epochFencingReporter{try: 1, epoch: 0}
	r := NewReconciler(fake.NewClientset(legacy), "leoflow", reporter)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(reporter.settled) != 1 || reporter.settled[0] != "success" {
		t.Fatalf("an unlabeled pod must settle an epoch-0 row, settled %v", reporter.settled)
	}
}
