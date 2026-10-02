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
// database reports settled whose observed pods are all finished and recorded
// goes in ONE DeleteCollection by the run's and tenant's labels.
//
// "Settled" is the predicate the retention janitor uses too: the run is in
// success or failed and no task instance of it is outside success, failed,
// skipped and upstream_failed. A run marked failed while a task still runs is
// therefore not settled, so a pod whose outcome the reconciler has not yet
// recorded (an informer snapshot can lag) is never collected with its record.
//
// The collection never touches a pod that may still run user code. The label
// selector names one run (a per-run UUID) in one tenant, and the field
// selector keeps only Succeeded and Failed pods, so a pod a clear redispatched
// for the same run after the database check is Pending or Running and cannot
// match. A pod whose outcome could not be recorded blocks its run. The list
// behind the DeleteCollection is served from the apiserver's watch cache
// (ResourceVersion "0") instead of a full etcd range read per run; a stale
// cache can only miss a just-finished pod, which a later sweep or the
// age-based GC collects, and pod names carry a random suffix, so nothing is
// ever reused. A sweep collects at most maxSettledRunsPerSweep runs, after the
// sweep is stamped complete. The reapers' live pod read is untouched.
//
// The chart grants deletecollection on pods only when the collection is on. A
// Role without the verb (an older chart, or one managed by hand) answers 403;
// the reconciler then deletes the same pods the selector would match (phase
// Succeeded or Failed) one by one, each pinned to its UID, and stops asking
// for the verb until restart.

// finishedPhases selects pods with no container left to stop.
const finishedPhases = "status.phase!=Pending,status.phase!=Running,status.phase!=Unknown"

// podLabelTenantID is the tenant label BuildPod stamps.
const podLabelTenantID = "leoflow.io/tenant-id"

// maxSettledRunsPerSweep bounds the DeleteCollection calls one sweep makes;
// the rest wait for the next sweep (30s later).
const maxSettledRunsPerSweep = 50

// RunRef names a run in its tenant, as the pod labels carry them.
type RunRef struct {
	Tenant string
	Run    string
}

// SettledRunChecker reports which of the given runs are settled in their
// tenant. Runs it leaves out are not collected.
type SettledRunChecker interface {
	SettledRuns(ctx context.Context, refs []RunRef) (map[RunRef]bool, error)
}

// SetSettledRunCollection turns on collecting a settled run's finished pods in
// one DeleteCollection. Nil (the default) keeps the age-based GC only.
func (r *Reconciler) SetSettledRunCollection(c SettledRunChecker) { r.settledRuns = c }

// runTracker groups one sweep's finished, recorded pods by run and remembers
// the runs that must not be collected yet.
type runTracker struct {
	on      bool
	pods    map[RunRef][]*corev1.Pod
	blocked map[RunRef]bool
}

func newRunTracker(on bool) *runTracker {
	return &runTracker{on: on, pods: map[RunRef][]*corev1.Pod{}, blocked: map[RunRef]bool{}}
}

func refOf(pod *corev1.Pod) RunRef {
	return RunRef{Tenant: pod.Labels[podLabelTenantID], Run: pod.Labels[podLabelRunID]}
}

// add records a finished pod whose outcome is recorded. A pod without both
// labels is left to the age-based GC.
func (t *runTracker) add(pod *corev1.Pod) {
	if !t.on {
		return
	}
	if ref := refOf(pod); ref.Run != "" && ref.Tenant != "" {
		t.pods[ref] = append(t.pods[ref], pod)
	}
}

// block keeps a pod's run out of this sweep's collection.
func (t *runTracker) block(pod *corev1.Pod) {
	if t.on {
		t.blocked[refOf(pod)] = true
	}
}

// candidates lists the runs with only finished, recorded pods, sorted.
func (t *runTracker) candidates() []RunRef {
	out := make([]RunRef, 0, len(t.pods))
	for ref := range t.pods {
		if !t.blocked[ref] {
			out = append(out, ref)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tenant != out[j].Tenant {
			return out[i].Tenant < out[j].Tenant
		}
		return out[i].Run < out[j].Run
	})
	return out
}

// collectSettledRuns deletes the finished pods of up to maxSettledRunsPerSweep
// candidate runs the database reports settled. A failed lookup collects
// nothing; the age-based GC still applies on later sweeps.
func (r *Reconciler) collectSettledRuns(ctx context.Context, t *runTracker) {
	if !t.on {
		return
	}
	refs := t.candidates()
	if len(refs) == 0 {
		return
	}
	settled, err := r.settledRuns.SettledRuns(ctx, refs)
	if err != nil {
		slog.ErrorContext(ctx, "looking up settled runs for pod collection", "runs", len(refs), "error", err)
		return
	}
	collected := 0
	for _, ref := range refs {
		if collected == maxSettledRunsPerSweep {
			return
		}
		if settled[ref] {
			r.collectRun(ctx, ref, t.pods[ref])
			collected++
		}
	}
}

// collectRun deletes one settled run's finished pods: in one DeleteCollection
// when allowed, else one UID-pinned delete per observed finished pod.
func (r *Reconciler) collectRun(ctx context.Context, ref RunRef, pods []*corev1.Pod) {
	if !r.deleteCollectionForbidden.Load() {
		err := r.clientset.CoreV1().Pods(r.namespace).DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{
			LabelSelector:   fmt.Sprintf("%s=%s,%s=%s", podLabelRunID, sanitizeLabel(ref.Run), podLabelTenantID, sanitizeLabel(ref.Tenant)),
			FieldSelector:   finishedPhases,
			ResourceVersion: "0",
		})
		switch {
		case err == nil:
			return
		case apierrors.IsForbidden(err):
			r.deleteCollectionForbidden.Store(true)
			slog.WarnContext(ctx, "deletecollection on pods is forbidden; collecting settled runs pod by pod (grant the verb in the executor Role, then restart)",
				"namespace", r.namespace, "error", err)
		default:
			slog.ErrorContext(ctx, "collecting a settled run's pods", "run", ref.Run, "error", err)
			return
		}
	}
	for _, pod := range pods {
		if pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
			continue
		}
		uid := pod.UID
		err := r.clientset.CoreV1().Pods(r.namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
		if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
			slog.ErrorContext(ctx, "collecting a settled run's pod", "pod", pod.Name, "run", ref.Run, "error", err)
		}
	}
}
