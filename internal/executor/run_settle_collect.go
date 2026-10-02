package executor

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Settled-run collection (performance item 7.4, opt-in through
// executor.collect_settled_run_pods). Without it the reconciler deletes each
// finished task pod on its own once the grace period passes: one apiserver
// call per pod, each pod lingering for ten minutes. With it, a run the
// database reports settled (success or failed) whose observed pods are all
// finished and recorded goes in ONE DeleteCollection by the run's label.
//
// The collection never touches a pod that may still run user code. The label
// selector names one run (the run id is a per-run UUID), and the field
// selector keeps only Succeeded and Failed pods, so a pod a clear redispatched
// for the same run after the database check is Pending or Running and cannot
// match. A pod whose outcome could not be recorded blocks its run, because the
// pod is still the only signal a later sweep can settle from. The reapers'
// live pod read is untouched.
//
// The chart grants deletecollection on pods only when the collection is on. A
// Role without the verb (an older chart, or one managed by hand) answers 403;
// the reconciler then deletes the run's finished pods one by one, which is
// today's call pattern without the wait, and stops asking for the verb until
// restart.

// finishedPhases selects pods with no container left to stop.
const finishedPhases = "status.phase!=Pending,status.phase!=Running,status.phase!=Unknown"

// SettledRunChecker reports which of the given runs are settled (success or
// failed) in the database. Runs it leaves out are not collected.
type SettledRunChecker interface {
	SettledRuns(ctx context.Context, runIDs []string) (map[string]bool, error)
}

// SetSettledRunCollection turns on collecting a settled run's finished pods in
// one DeleteCollection. Nil (the default) keeps the age-based GC only.
func (r *Reconciler) SetSettledRunCollection(c SettledRunChecker) { r.settledRuns = c }

// runTracker groups one sweep's finished, recorded pods by run and remembers
// the runs that must not be collected yet.
type runTracker struct {
	on      bool
	pods    map[string][]*corev1.Pod
	blocked map[string]bool
}

func newRunTracker(on bool) *runTracker {
	return &runTracker{on: on, pods: map[string][]*corev1.Pod{}, blocked: map[string]bool{}}
}

// add records a finished pod whose outcome is recorded.
func (t *runTracker) add(pod *corev1.Pod) {
	if !t.on {
		return
	}
	if run := pod.Labels[podLabelRunID]; run != "" {
		t.pods[run] = append(t.pods[run], pod)
	}
}

// block keeps a pod's run out of this sweep's collection.
func (t *runTracker) block(pod *corev1.Pod) {
	if t.on {
		t.blocked[pod.Labels[podLabelRunID]] = true
	}
}

// candidates lists the runs with only finished, recorded pods, sorted.
func (t *runTracker) candidates() []string {
	out := make([]string, 0, len(t.pods))
	for run := range t.pods {
		if !t.blocked[run] {
			out = append(out, run)
		}
	}
	sort.Strings(out)
	return out
}

// collectSettledRuns deletes the finished pods of every candidate run the
// database reports settled. A failed lookup collects nothing; the age-based
// GC still applies on later sweeps.
func (r *Reconciler) collectSettledRuns(ctx context.Context, t *runTracker) {
	if !t.on {
		return
	}
	ids := t.candidates()
	if len(ids) == 0 {
		return
	}
	settled, err := r.settledRuns.SettledRuns(ctx, ids)
	if err != nil {
		slog.ErrorContext(ctx, "looking up settled runs for pod collection", "runs", len(ids), "error", err)
		return
	}
	for _, run := range ids {
		if settled[run] {
			r.collectRun(ctx, run, t.pods[run])
		}
	}
}

// collectRun deletes one settled run's finished pods: in one DeleteCollection
// when allowed, else one delete per observed pod.
func (r *Reconciler) collectRun(ctx context.Context, run string, pods []*corev1.Pod) {
	if !r.deleteCollectionForbidden.Load() {
		err := r.clientset.CoreV1().Pods(r.namespace).DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{
			LabelSelector: fmt.Sprintf("%s=%s", podLabelRunID, sanitizeLabel(run)),
			FieldSelector: finishedPhases,
		})
		switch {
		case err == nil:
			return
		case apierrors.IsForbidden(err):
			r.deleteCollectionForbidden.Store(true)
			slog.WarnContext(ctx, "deletecollection on pods is forbidden; collecting settled runs pod by pod (grant the verb in the executor Role)",
				"namespace", r.namespace, "error", err)
		default:
			slog.ErrorContext(ctx, "collecting a settled run's pods", "run", run, "error", err)
			return
		}
	}
	for _, pod := range pods {
		if err := r.clientset.CoreV1().Pods(r.namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			slog.ErrorContext(ctx, "collecting a settled run's pod", "pod", pod.Name, "run", run, "error", err)
		}
	}
}
