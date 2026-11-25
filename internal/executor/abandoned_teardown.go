package executor

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Abandoned pod teardown (#2430). A verdict that ends an attempt (a run or a
// task instance marked failed or success through the API, a tenant stopped)
// only writes the row; the attempt stops when its agent's next heartbeat is
// answered with should_terminate. An agent that cannot reach the control plane
// never hears it, and no reaper looks at the pod: the agent-lost and pod-lost
// reapers list running rows only, and the settled-run collection takes
// finished pods only. In split mode the API role has no pod access by design
// (ADR 0049), so it cannot stop the pod at the verdict either.
//
// The leader closes that gap, as Argo Workflows' controller does for a
// Workflow's shutdown intent: every sweep, it asks the database which of the
// live task pods it listed still belong to their task instance's live attempt
// (the same predicate a heartbeat is answered by: same try and epoch, state
// queued or running), and stops a pod whose attempt has stayed abandoned for
// abandonedPodGrace. The grace leaves a reachable agent the time to hear
// should_terminate on its own and exit with its outcome record; the stop is
// the one the reapers use (stopOrDelete), so a started pod keeps its record.
// Abandonment is monotonic, an attempt never becomes live again, so the stop
// needs no second read.

// AttemptState is what the database holds for one task instance: the attempt
// it is on and whether that attempt may still run (state queued or running).
type AttemptState struct {
	TryNumber    int
	AttemptEpoch int
	Live         bool
}

// AttemptLiveness reads the attempt each task instance is on. The scheduler
// store implements it.
type AttemptLiveness interface {
	// TaskAttempts returns the state of each listed task instance that still
	// exists, keyed by its id. A missing id has no row any more.
	TaskAttempts(ctx context.Context, taskInstanceIDs []string) (map[string]AttemptState, error)
}

// PodStopper stops one live task pod the way a reaper's teardown does. The
// Kubernetes executor implements it.
type PodStopper interface {
	StopTaskPod(ctx context.Context, pod *corev1.Pod) error
}

// abandonedTeardown is the reconciler's state for the pass.
type abandonedTeardown struct {
	store   AttemptLiveness
	stopper PodStopper
	gate    destructiveGate
	grace   time.Duration
	rec     DecisionRecorder
	// since is when each live pod was first seen abandoned, by UID. A pod a
	// sweep does not list again, or finds live, is forgotten.
	since map[types.UID]time.Time
	// stopped holds the pods already stopped, so a pod inside its termination
	// grace is not stopped again on every sweep.
	stopped map[types.UID]bool
}

// SetAbandonedPodTeardown turns on the abandoned pod teardown (#2430): every
// sweep stops the live task pods whose attempt the database no longer runs,
// once they stayed so for grace. gate is the reapers' destructive gate. Left
// unset, no pod is stopped here.
func (r *Reconciler) SetAbandonedPodTeardown(store AttemptLiveness, stopper PodStopper, gate func(context.Context) bool, grace time.Duration, rec DecisionRecorder) {
	r.abandoned = &abandonedTeardown{
		store: store, stopper: stopper, gate: gate, grace: grace, rec: rec,
		since: map[types.UID]time.Time{}, stopped: map[types.UID]bool{},
	}
}

// livePodAttempt is one live task pod with the attempt its labels name.
type livePodAttempt struct {
	pod      *corev1.Pod
	tiID     string
	try      int
	epoch    int
	hasEpoch bool
}

// liveTaskPods returns the pods of the sweep whose task container may still
// run user code and that name their task instance and attempt. A pod whose
// task instance id is not a UUID was not made by BuildPod and is left alone:
// no row could ever match it, so it would always read as abandoned.
func liveTaskPods(pods []*corev1.Pod) []livePodAttempt {
	var out []livePodAttempt
	for _, pod := range pods {
		if terminalForTeardown(pod) || taskContainerTerminated(pod) {
			continue
		}
		tiID := pod.Annotations["leoflow.io/task-instance-id"]
		try, epoch, ok := attemptOf(pod)
		if _, err := uuid.Parse(tiID); err != nil || !ok {
			continue
		}
		_, hasEpoch := pod.Labels[podLabelAttemptEpoch]
		out = append(out, livePodAttempt{pod: pod, tiID: tiID, try: try, epoch: epoch, hasEpoch: hasEpoch})
	}
	return out
}

// abandoned reports whether the pod's attempt is no longer its task
// instance's live one: the row is gone, ended, or on a later try or epoch. A
// row on an earlier attempt than the pod cannot happen and is left alone. A
// pod stamped before the epoch existed is matched on its try.
func (p livePodAttempt) abandoned(row AttemptState, found bool) bool {
	switch {
	case !found:
		return true
	case row.TryNumber != p.try:
		return row.TryNumber > p.try
	case p.hasEpoch && row.AttemptEpoch != p.epoch:
		return row.AttemptEpoch > p.epoch
	default:
		return !row.Live
	}
}

// teardownAbandoned is the pass of one sweep, over the pods it listed.
func (r *Reconciler) teardownAbandoned(ctx context.Context, pods []*corev1.Pod) {
	t := r.abandoned
	if t == nil {
		return
	}
	live := liveTaskPods(pods)
	if len(live) == 0 {
		clear(t.since)
		clear(t.stopped)
		return
	}
	ids := make([]string, 0, len(live))
	for _, p := range live {
		ids = append(ids, p.tiID)
	}
	rows, err := t.store.TaskAttempts(ctx, ids)
	if err != nil {
		slog.WarnContext(ctx, "reading task attempts for the abandoned pod teardown; retrying next sweep", "pods", len(live), "error", err)
		t.record("abandoned_pod_read_error")
		return
	}
	now := r.now()
	seen := make(map[types.UID]bool, len(live))
	for _, p := range live {
		row, found := rows[p.tiID]
		if !p.abandoned(row, found) {
			continue
		}
		uid := p.pod.UID
		seen[uid] = true
		first, ok := t.since[uid]
		if !ok {
			t.since[uid] = now
			continue
		}
		if t.stopped[uid] || now.Sub(first) < t.grace || !gateOpen(t.gate, ctx) {
			continue
		}
		t.stop(ctx, p, now.Sub(first))
	}
	for uid := range t.since {
		if !seen[uid] {
			delete(t.since, uid)
			delete(t.stopped, uid)
		}
	}
}

// stop stops one abandoned pod and meters the outcome.
func (t *abandonedTeardown) stop(ctx context.Context, p livePodAttempt, abandonedFor time.Duration) {
	if err := t.stopper.StopTaskPod(ctx, p.pod); err != nil {
		slog.ErrorContext(ctx, "stopping a task pod whose attempt was ended", "pod", p.pod.Name,
			"task_instance", p.tiID, "try", p.try, "attempt_epoch", p.epoch, "error", err)
		t.record("abandoned_pod_teardown_error")
		return
	}
	t.stopped[p.pod.UID] = true
	slog.WarnContext(ctx, "stopped a task pod whose attempt was ended while its agent did not stop it",
		"pod", p.pod.Name, "task_instance", p.tiID, "try", p.try, "attempt_epoch", p.epoch,
		"abandoned_for", abandonedFor)
	t.record("abandoned_pod_teardown")
}

func (t *abandonedTeardown) record(decision string) {
	if t.rec != nil {
		t.rec.RecordSchedulerDecision(decision)
	}
}

// StopTaskPod stops one live task pod listed by the caller, the teardown a
// reaper applies (stopOrDelete): a started pod is stopped in place and keeps
// its outcome record, a pod that never started is deleted, and a pod already
// terminal is left alone. The write pins the pod's UID.
func (e *KubernetesExecutor) StopTaskPod(ctx context.Context, pod *corev1.Pod) error {
	if terminalForTeardown(pod) {
		return nil
	}
	return e.stopOrDelete(ctx, pod)
}
