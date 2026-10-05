package executor

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/dexadata/dexaflow/internal/taskoutcome"
)

// settleKind is what the reconciler must record for a terminal task pod.
type settleKind int

const (
	// settleNothing means there is nothing to record (a succeeded pod with no
	// outcome record — its report already settled the task; the reconciler only
	// garbage-collects it).
	settleNothing settleKind = iota
	// settleFailed records the task instance as failed.
	settleFailed
	// settleSucceeded records the task instance as succeeded — recovering a success
	// whose report was lost (ADR 0052).
	settleSucceeded
	// settleReschedule parks the task instance in up_for_reschedule.
	settleReschedule
)

// verdict is the reconciler's classification of a task pod: whether it is terminal
// (garbage-collectable on age) and what, if anything, to record for it.
type verdict struct {
	terminal   bool       // the pod is done; eligible for GC on age
	settle     settleKind // what to record for the task instance
	reason     string     // failure reason (settleFailed)
	at         time.Time  // next-poke time (settleReschedule)
	fromRecord bool       // a durable outcome record drove this, not pod phase
}

// unrecoverableWaiting lists container "waiting" reasons that never self-resolve
// and mean the agent never started, so no state will be reported.
var unrecoverableWaiting = map[string]bool{
	"ImagePullBackOff":           true,
	"ErrImagePull":               true,
	"InvalidImageName":           true,
	"CreateContainerError":       true,
	"CreateContainerConfigError": true,
	"CrashLoopBackOff":           true,
}

// taskContainerName is the task pod's single container (see BuildPod).
const taskContainerName = "task"

// classifyPod determines what the reconciler should do with a task pod. It trusts
// a durable outcome record on the terminated task container over pod phase (ADR
// 0052) — recovering a success whose report was lost — and falls back to
// phase-based classification when no decodable record is present.
func classifyPod(pod *corev1.Pod) verdict {
	// The durable outcome record is authoritative when present: the agent wrote it
	// before attempting the report, so it survives a pod killed mid-report.
	if rec, ok := outcomeRecord(pod); ok {
		switch rec.Outcome {
		case taskoutcome.Success:
			return verdict{terminal: true, settle: settleSucceeded, fromRecord: true}
		case taskoutcome.Failed:
			return verdict{terminal: true, settle: settleFailed, reason: failedReason(rec, pod), fromRecord: true}
		case taskoutcome.Reschedule:
			at, _ := rec.At() // Decode guaranteed a parseable time
			return verdict{terminal: true, settle: settleReschedule, at: at, fromRecord: true}
		}
	}

	// No record — fall back to pod phase (pre-0052 behavior). A record-less
	// succeeded pod has nothing to settle; its report already ran. A failed pod is
	// recorded as failed so retries and finalization proceed.
	switch pod.Status.Phase {
	case corev1.PodFailed:
		return verdict{terminal: true, settle: settleFailed, reason: podFailureReason(pod)}
	case corev1.PodSucceeded:
		return verdict{terminal: true, settle: settleNothing}
	case corev1.PodPending, corev1.PodRunning, corev1.PodUnknown:
		for _, cs := range pod.Status.ContainerStatuses {
			if w := cs.State.Waiting; w != nil && unrecoverableWaiting[w.Reason] {
				return verdict{terminal: true, settle: settleFailed, reason: waitingFailureReason(w.Reason, w.Message)}
			}
		}
		return verdict{terminal: false}
	default:
		return verdict{terminal: false}
	}
}

// outcomeRecord decodes the durable outcome record from the terminated task
// container's termination message, if present and valid. A missing, running, or
// undecodable message returns false so the caller falls back to pod phase.
func outcomeRecord(pod *corev1.Pod) (taskoutcome.Record, bool) {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name != taskContainerName {
			continue
		}
		if t := cs.State.Terminated; t != nil && t.Message != "" {
			return taskoutcome.Decode(t.Message)
		}
	}
	return taskoutcome.Record{}, false
}

// failedReason picks the better of the two descriptions of one failure.
//
// The record wins when it carries a classification, because that is a cause the
// control plane cannot otherwise observe. It LOSES to the pod when it carries
// only an exit code and the pod says the container was OOMKilled, and that case
// is the reason this function exists (#1216).
//
// A task killed for memory produced "task failed (exit 255)" while the pod
// status next to it said OOMKilled. Both halves are needed to see why. On
// cgroup v2 the kubelet sets memory.oom.group per container, so the kernel kills
// the whole group and the agent usually dies with the task, leaving no record at
// all: the pod-phase path below then runs and the operator gets the good
// message. But when the agent DOES survive, which is cgroup v1 or
// singleProcessOOMKill, it writes a record, the record wins, and the OOMKilled
// branch it should have reached is never consulted. The pod knew and the
// reconciler ignored it.
//
// It is not 137, either. Go reports a signaled child as ExitCode() -1, which
// the agent clamps to 255; 128+signal is a shell convention and only appears
// when a shell sits between. So the exit code cannot be pattern-matched for
// this, and the pod's own Reason is the only reliable signal.
func failedReason(rec taskoutcome.Record, pod *corev1.Pod) string {
	if rec.Reason == "" && podWasOOMKilled(pod) {
		// The RECORD's exit code, not the container's, and the difference is the
		// whole reason this takes an argument. On the path that reaches here the
		// agent survived its child and exited on its own, so the CONTAINER's exit
		// code is the agent's clean exit (1) and says nothing about the task.
		// Measured against a production-shaped fixture, the container's code
		// rendered "OOMKilled (exit 1)", which is the agent's status presented as
		// the task's.
		//
		// podFailureReason is right to use the container's, because there the
		// container IS the corpse. This caller inherited that line without
		// inheriting the conditions that make it true.
		if rec.ExitCode != nil {
			return oomReason(*rec.ExitCode)
		}
		return oomReason(containerExitCode(pod))
	}
	return recordFailureReason(rec)
}

// oomReason is the one place the operator-facing OOM string is built, so the
// two callers cannot drift apart in wording while needing different exit codes.
func oomReason(exitCode int32) string {
	return boundReason(fmt.Sprintf(
		"the task container was OOMKilled (exit %d); raise the task's memory limit.", exitCode))
}

// podWasOOMKilled reports whether the task container's terminated status says
// the kernel took it.
func podWasOOMKilled(pod *corev1.Pod) bool {
	return taskTerminated(pod) != nil && taskTerminated(pod).Reason == "OOMKilled"
}

// containerExitCode is the fallback when the record carries none.
func containerExitCode(pod *corev1.Pod) int32 {
	if t := taskTerminated(pod); t != nil {
		return t.ExitCode
	}
	return 0
}

// taskTerminated returns the task container's terminated state, or nil.
func taskTerminated(pod *corev1.Pod) *corev1.ContainerStateTerminated {
	if pod == nil {
		return nil
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == taskContainerName {
			return cs.State.Terminated
		}
	}
	return nil
}

// recordFailureReason renders a failure reason from an outcome record. A record
// carrying an explicit classification (a failure the agent diagnosed before it
// could report anything, e.g. a refused bootstrap) wins: it is the only
// description of a cause the control plane cannot otherwise observe. Otherwise
// the reason names the exit code, as before.
//
// The record's own reason is bounded here for the same fact the sibling producers
// bound theirs for: this is a value the reconciler READS rather than builds. The
// agent bounds what it writes, but the termination message is a file the task's
// own process can write before it is killed, the kubelet's ceiling there is
// ~4 KiB — seventeen times taskoutcome.MaxReasonLen — and Decode imposes no
// length limit of its own.
func recordFailureReason(rec taskoutcome.Record) string {
	if rec.Reason != "" {
		return boundReason(rec.Reason)
	}
	if rec.ExitCode != nil {
		return fmt.Sprintf("task failed (exit %d)", *rec.ExitCode)
	}
	return "task failed"
}

// podFailureReason describes a failed pod from what Kubernetes observed, for the
// failures no agent can ever report because it never ran — an image that would
// not pull, an OOM kill at startup, an evicted or node-lost pod. Without this the
// operator sees a task that failed with no cause anywhere: the pod is gone, and
// there are no logs because nothing ever streamed.
//
// It prefers the most specific signal available: the task container's terminated
// reason and exit code, then the pod-level reason, then a plain fallback. Every
// result is bounded — the strings are copied from kubelet-supplied fields, which
// are an untrusted, unbounded source for a value that ends up end-user visible.
func podFailureReason(pod *corev1.Pod) string {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name != taskContainerName {
			continue
		}
		t := cs.State.Terminated
		if t == nil {
			continue
		}
		switch {
		case t.Reason == "OOMKilled":
			return oomReason(t.ExitCode)
		case t.Reason != "" && t.Reason != "Error":
			return boundReason(fmt.Sprintf("the task container terminated: %s (exit %d).", t.Reason, t.ExitCode))
		case t.ExitCode != 0:
			return boundReason(fmt.Sprintf("the task container exited with exit %d.", t.ExitCode))
		}
	}
	if pod.Status.Reason != "" {
		return boundReason(fmt.Sprintf("the task pod failed: %s.", pod.Status.Reason))
	}
	return "pod failed"
}

// waitingFailureReason explains a container stuck in an unrecoverable waiting
// state. It names the Kubernetes reason (what an operator greps for and what
// `kubectl describe` shows) and adds the context that makes it actionable, since
// the bare enum alone is what left operators reaching for kubectl. The kubelet
// message is consulted only to distinguish reasons that share one enum but need
// different guidance (CreateContainerConfigError); it is never echoed, so an
// unbounded, operator-controlled string cannot reach the end-user field.
func waitingFailureReason(reason, message string) string {
	switch reason {
	case "ImagePullBackOff", "ErrImagePull", "InvalidImageName":
		return boundReason(fmt.Sprintf(
			"%s: the task image could not be pulled; check the image reference and the pull secret.", reason))
	case "CreateContainerError":
		return boundReason(fmt.Sprintf(
			"%s: the task container could not be created; check the pod spec, mounts, and image entrypoint.", reason))
	case "CreateContainerConfigError":
		// The kubelet raises this both for a missing ConfigMap/Secret and for the
		// runAsNonRoot admission refusal. The latter is a distinct, common cause
		// under the non-root default and needs its own fix, so we match the
		// kubelet's signature substring rather than lump it into the generic bucket.
		if strings.Contains(message, "runAsNonRoot") {
			return boundReason(fmt.Sprintf(
				"%s: the DAG image runs as root, but task pods run as non-root by default; "+
					"end the Dockerfile with a numeric `USER 65532`, "+
					"or set `taskPodSecurity.runAsNonRoot=false` for the cluster.", reason))
		}
		return boundReason(fmt.Sprintf(
			"%s: the task container could not be configured; check its ConfigMap, Secret, and env-var references.", reason))
	case "CrashLoopBackOff":
		return boundReason(fmt.Sprintf(
			"%s: the task container keeps exiting at startup; inspect the task image entrypoint.", reason))
	default:
		return boundReason(reason)
	}
}

// boundReason clamps a reason to the outcome record's cap. The cap applies here
// too because these reasons land in the same end-user-visible field as the
// agent's own classifications, and they are copied from kubelet-supplied fields.
func boundReason(reason string) string {
	return taskoutcome.TruncateReason(reason, taskoutcome.MaxReasonLen)
}

// attemptOf reads the attempt the pod ran from the labels BuildPod sets: the
// try from leoflow.io/try-number and the epoch from leoflow.io/attempt-epoch
// (ADR 0051 amendment). They guard the settle against clobbering a different
// attempt, so a pod whose try label is missing or unparseable, or whose epoch
// label is present but unparseable, is not settled (ok is false). A missing
// epoch label is epoch 0: the pod was created before the epoch existed, and it
// can only settle a row that no post-upgrade dispatch has claimed (#1130).
//
// The reconciler fences on the label, never on the outcome record's own epoch:
// the control plane wrote the label, and the task cannot change it.
func attemptOf(pod *corev1.Pod) (try, epoch int, ok bool) {
	s := pod.Labels[podLabelTryNumber]
	if s == "" {
		return 0, 0, false
	}
	try, err := strconv.Atoi(s)
	if err != nil {
		return 0, 0, false
	}
	epoch, ok = podEpoch(pod)
	if !ok {
		return 0, 0, false
	}
	return try, epoch, true
}

// OutcomeReporter records a terminal task-instance outcome the reconciler
// recovered from a pod (its durable outcome record, or its phase). Every method is
// guarded by the attempt (try_number and attempt_epoch, from the pod's labels) so
// a stale reconciler acting on a previous attempt's pod never clobbers a live
// retry or re-place (#1130), and is idempotent: a settle on an already-terminal
// or superseded instance is a no-op, not an error.
type OutcomeReporter interface {
	FailTask(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int, reason string) error
	SucceedTask(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int) error
	RescheduleTask(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int, at time.Time) error
}

// PodSnapshotter supplies the reconciler's task-pod set from a local cache instead
// of a live LIST every tick (PR-10). It is safe here without a live confirm: the
// signal the reconciler acts on is presence of a terminal pod, which is monotonic
// (a pod that reached Failed/Succeeded stays terminal), and every settle is
// attempt- and state-guarded (ADR 0052), so at worst cache lag delays a settle by
// a tick. A nil snapshotter (Lite/subprocess, or a cold start) keeps the live LIST.
type PodSnapshotter interface {
	// SnapshotTaskPods returns the managed task pods currently known, or an error
	// (e.g. the cache has not synced) so the reconciler retries next tick rather
	// than treating an unsynced cache as an empty cluster.
	SnapshotTaskPods() ([]*corev1.Pod, error)
}

// podGCGracePeriod is how long a finished pod is kept before garbage collection,
// leaving a window to inspect a failed pod with kubectl.
const podGCGracePeriod = 10 * time.Minute

// Reconciler detects task pods whose task instance was never settled by the agent
// (a pod killed before or during its report) and records the true outcome — from
// the pod's durable outcome record where present, else its phase — so retries and
// run finalization proceed instead of stranding the task. It also garbage-collects
// finished pods once they age out.
//
// Outcome recovery is best-effort (ADR 0052 is a School-A optimization on the
// School-B re-drive floor): the settle guards on the active states, so if a reaper
// settles the still-running TI failed first (heartbeat timeout, ~90s) before this
// loop recovers the success record (~30s), the recovered success is dropped and the
// task degrades to the safe retry path. The 30s vs 90s cadence makes the reconciler
// win the common case; the loss is a correctness-safe re-run, not a wrong result.
type Reconciler struct {
	clientset kubernetes.Interface
	namespace string
	reporter  OutcomeReporter
	now       func() time.Time
	ttl       time.Duration
	// snapshot, when set, replaces the per-tick live LIST with a cache read
	// (PR-10). Nil keeps the live LIST (Lite/subprocess, or before the informer
	// is wired). GC deletes still go straight to the apiserver.
	snapshot PodSnapshotter
	// lastSweepCompleted is the unix-nano stamp of the last sweep that listed
	// the task-pod set and visited every pod; 0 = none yet. Read by the reaper's
	// leader-settling gate (see LastSweepCompletedAt) from another goroutine
	// than the one that writes it at the end of Reconcile, hence atomic.
	lastSweepCompleted atomic.Int64
}

// NewReconciler builds a Reconciler over the given cluster and outcome reporter.
func NewReconciler(clientset kubernetes.Interface, namespace string, reporter OutcomeReporter) *Reconciler {
	return &Reconciler{clientset: clientset, namespace: namespace, reporter: reporter, now: time.Now, ttl: podGCGracePeriod}
}

// SetPodSnapshotter wires a cache-backed pod source so Reconcile reads its
// task-pod set from the shared informer instead of a live LIST every tick
// (PR-10). Left unset, the reconciler keeps the live LIST — today's behavior.
func (r *Reconciler) SetPodSnapshotter(s PodSnapshotter) { r.snapshot = s }

// LastSweepCompletedAt reports when the last COMPLETED sweep finished: the
// task-pod set was listed and every pod visited, so every terminal pod present
// at that moment had its chance to settle its task instance. Zero means no
// sweep has completed yet. It is the record the reaper's leader-settling gate
// compares against the leadership stamp — the reapers act on signals a
// control-plane restart manufactures (a task pod that exited during the outage
// looks lost), and only a sweep completed under the new leader separates
// "finished during the outage, outcome recoverable" from "genuinely lost".
//
// A sweep that could not list pods records nothing. A sweep whose individual
// settle failed on a DB error still counts: that pod is retried next sweep, and
// the gate's grace leaves room for the retry before any reaper may act.
func (r *Reconciler) LastSweepCompletedAt() time.Time {
	ns := r.lastSweepCompleted.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// Reconcile lists managed task pods, records each terminal one's outcome against
// its task instance, and garbage-collects finished pods older than the grace
// period. A completed sweep is stamped for LastSweepCompletedAt.
func (r *Reconciler) Reconcile(ctx context.Context) error {
	pods, err := r.listTaskPods(ctx)
	if err != nil {
		return err
	}
	for _, pod := range pods {
		v := classifyPod(pod)
		if !v.terminal {
			continue
		}
		// A terminal pod must have its outcome durably recorded before it is
		// collected: while a settle can still be retried, the pod is the only signal,
		// so deleting it after a failed settle would strand the task instance until
		// the heartbeat reaper catches it. On a genuine DB error, leave the pod and
		// try again next tick.
		if v.settle != settleNothing {
			if err := r.settlePod(ctx, pod, v); err != nil {
				continue
			}
			// A reschedule pod's record must NOT linger to be re-applied. Reschedule
			// redispatch reuses the same try_number (it consumes no attempt, #380). The
			// attempt epoch now tells a stale poke pod from the re-dispatched live one
			// (ADR 0051 amendment), but a poke pod stamped before the epoch existed
			// is epoch 0, as is a legacy row's next poke during a rolling upgrade, and
			// a lingering one would re-park the live attempt with an already-elapsed
			// reschedule_at and flap the sensor. It exited cleanly (nothing to
			// inspect), so collect it now instead of after the grace period, well
			// before the next redispatch. (ADR 0052)
			if v.settle == settleReschedule {
				r.collect(ctx, pod)
				continue
			}
		}
		if r.now().Sub(pod.CreationTimestamp.Time) > r.ttl {
			r.collect(ctx, pod)
		}
	}
	r.lastSweepCompleted.Store(r.now().UnixNano())
	return nil
}

// listTaskPods returns the managed task-pod set to reconcile, from the cache
// snapshot when a PodSnapshotter is wired (PR-10) and from a live LIST otherwise.
// The live path selects on the leoflow.io/run-id label, matching the informer's
// scope, and copies to pointers so both paths share one loop shape.
func (r *Reconciler) listTaskPods(ctx context.Context) ([]*corev1.Pod, error) {
	if r.snapshot != nil {
		pods, err := r.snapshot.SnapshotTaskPods()
		if err == nil {
			return pods, nil
		}
		// The snapshot is unavailable (e.g. the informer never synced — RBAC
		// drift denying watch, or a broken watch). Degrade to the live LIST
		// rather than propagate: a returned error would stall Reconcile every
		// tick and silently disable ADR-0052 lost-outcome recovery and
		// finished-pod GC. This mirrors the reapers' cold-cache fallback; at the
		// ~30s reconcile cadence a live LIST during an informer outage is
		// O(P)/30s, not the per-second storm this read-path removed.
		slog.DebugContext(ctx, "task-pod snapshot unavailable; falling back to live LIST", "err", err)
	}
	list, err := r.clientset.CoreV1().Pods(r.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "leoflow.io/run-id",
	})
	if err != nil {
		return nil, fmt.Errorf("listing task pods: %w", err)
	}
	out := make([]*corev1.Pod, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, &list.Items[i])
	}
	return out, nil
}

// settlePod records the pod's recovered outcome against its task instance,
// attempt-guarded. It returns nil when there is nothing to record (an orphan pod
// with no task-instance annotation) so the caller may collect it, and an error
// when the outcome could not be durably recorded, so the caller defers collection
// until a later tick succeeds.
func (r *Reconciler) settlePod(ctx context.Context, pod *corev1.Pod, v verdict) error {
	tiID := pod.Annotations["leoflow.io/task-instance-id"]
	if tiID == "" {
		return nil
	}
	tryNumber, epoch, ok := attemptOf(pod)
	if !ok {
		// Anomalous pod: BuildPod always sets the try label and never an
		// unparseable epoch, so this is a hand-created or old-version pod. Without the attempt we cannot guard the settle, and
		// settling unguarded could clobber a different attempt. Skip the settle and
		// let the pod age out normally (the heartbeat reaper is the task instance's
		// backstop); returning an error here would leak the pod forever, since the
		// label will never appear on a later tick.
		slog.Error("cannot settle task pod without a valid try-number and attempt epoch; leaving the TI to the reaper",
			"pod", pod.Name, "task_instance", tiID)
		return nil
	}
	if err := r.recordOutcome(ctx, tiID, tryNumber, epoch, v); err != nil {
		slog.Error("recording task pod outcome", "pod", pod.Name, "task_instance", tiID,
			"settle", v.settle, "from_record", v.fromRecord, "error", err)
		return err
	}
	return nil
}

func (r *Reconciler) recordOutcome(ctx context.Context, tiID string, tryNumber, epoch int, v verdict) error {
	switch v.settle {
	case settleSucceeded:
		return r.reporter.SucceedTask(ctx, tiID, tryNumber, epoch)
	case settleReschedule:
		return r.reporter.RescheduleTask(ctx, tiID, tryNumber, epoch, v.at)
	case settleFailed:
		return r.reporter.FailTask(ctx, tiID, tryNumber, epoch, v.reason)
	case settleNothing:
		return nil
	default:
		return nil
	}
}

func (r *Reconciler) collect(ctx context.Context, pod *corev1.Pod) {
	if err := r.clientset.CoreV1().Pods(r.namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{}); err != nil {
		slog.Error("garbage-collecting finished pod", "pod", pod.Name, "error", err)
	}
}
