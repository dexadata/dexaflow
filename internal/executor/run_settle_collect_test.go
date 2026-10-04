package executor

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/dexadata/dexaflow/internal/taskoutcome"
)

// fakeSettledRuns answers SettledRuns from a fixed set of run ids (in tenant
// t1 unless the key names another tenant as "tenant/run") and records each ask.
type fakeSettledRuns struct {
	settled map[string]bool
	asked   [][]RunRef
	err     error
}

func (f *fakeSettledRuns) SettledRuns(_ context.Context, refs []RunRef) (map[RunRef]bool, error) {
	f.asked = append(f.asked, refs)
	if f.err != nil {
		return nil, f.err
	}
	out := map[RunRef]bool{}
	for _, r := range refs {
		if (r.Tenant == "t1" && f.settled[r.Run]) || f.settled[r.Tenant+"/"+r.Run] {
			out[r] = true
		}
	}
	return out, nil
}

// runPod is a young task pod of run in tenant t1, so the age-based GC never
// collects it.
func runPod(name, run string, phase corev1.PodPhase, now time.Time) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "leoflow", UID: types.UID("uid-" + name), CreationTimestamp: metav1.NewTime(now.Add(-time.Minute)),
			Labels:      map[string]string{"leoflow.io/run-id": run, "leoflow.io/tenant-id": "t1", "leoflow.io/try-number": "1"},
			Annotations: map[string]string{"leoflow.io/task-instance-id": "ti-" + name},
		},
		Status: corev1.PodStatus{Phase: phase},
	}
}

func settleCollectReconciler(cs *fake.Clientset, now time.Time, runs SettledRunChecker) *Reconciler {
	r := NewReconciler(cs, "leoflow", &fakeReporter{})
	r.now = func() time.Time { return now }
	r.ttl = 10 * time.Minute
	r.SetSettledRunCollection(runs)
	return r
}

func actionsOf(cs *fake.Clientset, verb string) []ktesting.Action {
	var out []ktesting.Action
	for _, a := range cs.Actions() {
		if a.GetVerb() == verb && a.GetResource().Resource == "pods" {
			out = append(out, a)
		}
	}
	return out
}

var settleNow = time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

// Off by default: without a checker the reconciler issues no collection
// delete and keeps young finished pods for the grace period, as before.
func TestSettledRunCollectionOffByDefault(t *testing.T) {
	cs := fake.NewClientset(runPod("a", "run-1", corev1.PodSucceeded, settleNow))
	r := settleCollectReconciler(cs, settleNow, nil)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(actionsOf(cs, "delete-collection")) + len(actionsOf(cs, "delete")); n != 0 {
		t.Fatalf("%d deletes with the collection off, want 0", n)
	}
}

// A settled run whose pods are all finished goes in one DeleteCollection by
// its run and tenant labels, restricted to finished phases and served from
// the watch cache (ResourceVersion "0"), with no per-pod delete. Runs that are
// not settled, or still have a live pod, are left alone.
func TestSettledRunPodsGoInOneDeleteCollection(t *testing.T) {
	cs := fake.NewClientset(
		runPod("a1", "run-1", corev1.PodSucceeded, settleNow),
		runPod("a2", "run-1", corev1.PodFailed, settleNow),
		runPod("a3", "run-1", corev1.PodSucceeded, settleNow),
		runPod("b1", "run-2", corev1.PodSucceeded, settleNow),
		runPod("c1", "run-3", corev1.PodSucceeded, settleNow),
		runPod("c2", "run-3", corev1.PodRunning, settleNow),
	)
	runs := &fakeSettledRuns{settled: map[string]bool{"run-1": true, "run-3": true}}
	r := settleCollectReconciler(cs, settleNow, runs)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runs.asked) != 1 || len(runs.asked[0]) != 2 || runs.asked[0][0] != (RunRef{Tenant: "t1", Run: "run-1"}) {
		t.Fatalf("asked = %v, want one batched ask for t1/run-1 and t1/run-2 (run-3 has a live pod)", runs.asked)
	}
	dcs := actionsOf(cs, "delete-collection")
	if len(dcs) != 1 {
		t.Fatalf("delete-collection calls = %d, want 1", len(dcs))
	}
	dc, ok := dcs[0].(ktesting.DeleteCollectionAction)
	if !ok {
		t.Fatalf("action %T is not a DeleteCollectionAction", dcs[0])
	}
	restr := dc.GetListRestrictions()
	if got := restr.Labels.String(); got != "leoflow.io/run-id=run-1,leoflow.io/tenant-id=t1" {
		t.Errorf("label selector = %q, want the run and its tenant", got)
	}
	if impl, ok := dcs[0].(ktesting.DeleteCollectionActionImpl); !ok || impl.ListOptions.ResourceVersion != "0" {
		t.Errorf("list options = %+v, want ResourceVersion 0 (served from the watch cache)", impl.ListOptions)
	}
	for _, phase := range []string{"Pending", "Running", "Unknown"} {
		if restr.Fields.Matches(fieldSet{"status.phase": phase}) {
			t.Errorf("field selector %q matches a %s pod; a live pod must never match", restr.Fields, phase)
		}
	}
	for _, phase := range []string{"Succeeded", "Failed"} {
		if !restr.Fields.Matches(fieldSet{"status.phase": phase}) {
			t.Errorf("field selector %q does not match a %s pod", restr.Fields, phase)
		}
	}
	if n := len(actionsOf(cs, "delete")); n != 0 {
		t.Errorf("per-pod deletes = %d, want 0", n)
	}
}

// Without the deletecollection verb the reconciler falls back to deleting the
// run's finished pods one by one, each pinned to its UID, and stops asking for
// the verb afterwards.
func TestSettledRunCollectionFallsBackWhenForbidden(t *testing.T) {
	cs := fake.NewClientset(
		runPod("a1", "run-1", corev1.PodSucceeded, settleNow),
		runPod("a2", "run-1", corev1.PodFailed, settleNow),
	)
	cs.PrependReactor("delete-collection", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", errors.New("no deletecollection"))
	})
	r := settleCollectReconciler(cs, settleNow, &fakeSettledRuns{settled: map[string]bool{"run-1": true}})
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(actionsOf(cs, "delete")); n != 2 {
		t.Fatalf("per-pod deletes = %d, want 2", n)
	}
	for _, a := range actionsOf(cs, "delete") {
		d, ok := a.(ktesting.DeleteActionImpl)
		if !ok || d.DeleteOptions.Preconditions == nil || d.DeleteOptions.Preconditions.UID == nil ||
			string(*d.DeleteOptions.Preconditions.UID) != "uid-"+d.Name {
			t.Errorf("delete of %s carries no UID precondition: %+v", d.Name, d.DeleteOptions)
		}
	}
	pods, _ := cs.CoreV1().Pods("leoflow").List(context.Background(), metav1.ListOptions{})
	if len(pods.Items) != 0 {
		t.Fatalf("%d pods remain after the fallback", len(pods.Items))
	}
	if _, err := cs.CoreV1().Pods("leoflow").Create(context.Background(), runPod("a3", "run-1", corev1.PodSucceeded, settleNow), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	cs.ClearActions()
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(actionsOf(cs, "delete-collection")); n != 0 {
		t.Errorf("delete-collection retried after a 403: %d calls", n)
	}
	if n := len(actionsOf(cs, "delete")); n != 1 {
		t.Errorf("per-pod deletes = %d, want 1", n)
	}
}

// The fallback deletes exactly what the field selector would: a pod the
// reconciler judged terminal but whose phase is still Running (its task
// container wrote an outcome record while a sidecar runs) is left alone.
func TestSettledRunCollectionFallbackKeepsNonFinishedPhases(t *testing.T) {
	sidecar := withRecord(runPod("a2", "run-1", corev1.PodRunning, settleNow), taskoutcome.Succeeded())
	cs := fake.NewClientset(runPod("a1", "run-1", corev1.PodSucceeded, settleNow), sidecar)
	cs.PrependReactor("delete-collection", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", errors.New("no deletecollection"))
	})
	r := settleCollectReconciler(cs, settleNow, &fakeSettledRuns{settled: map[string]bool{"run-1": true}})
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.CoreV1().Pods("leoflow").Get(context.Background(), "a2", metav1.GetOptions{}); err != nil {
		t.Fatalf("the Running pod was deleted by the fallback: %v", err)
	}
	if _, err := cs.CoreV1().Pods("leoflow").Get(context.Background(), "a1", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the Succeeded pod survived the fallback: %v", err)
	}
}

// A sweep collects at most maxSettledRunsPerSweep runs; the rest wait for the
// next sweep.
func TestSettledRunCollectionIsCappedPerSweep(t *testing.T) {
	cs := fake.NewClientset()
	settled := map[string]bool{}
	for i := range maxSettledRunsPerSweep + 10 {
		run := fmt.Sprintf("run-%03d", i)
		settled[run] = true
		if _, err := cs.CoreV1().Pods("leoflow").Create(context.Background(), runPod("p"+run, run, corev1.PodSucceeded, settleNow), metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	r := settleCollectReconciler(cs, settleNow, &fakeSettledRuns{settled: settled})
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(actionsOf(cs, "delete-collection")); n != maxSettledRunsPerSweep {
		t.Fatalf("delete-collection calls = %d, want the cap %d", n, maxSettledRunsPerSweep)
	}
}

// The sweep is stamped complete before the collection starts, so a slow
// apiserver during the collection never delays the reaper's settling gate.
func TestSettledRunCollectionRunsAfterTheSweepStamp(t *testing.T) {
	cs := fake.NewClientset(runPod("a1", "run-1", corev1.PodSucceeded, settleNow))
	r := settleCollectReconciler(cs, settleNow, &fakeSettledRuns{settled: map[string]bool{"run-1": true}})
	var stamped bool
	cs.PrependReactor("delete-collection", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		stamped = !r.LastSweepCompletedAt().IsZero()
		return false, nil, nil
	})
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !stamped {
		t.Fatal("the collection ran before the sweep was stamped complete")
	}
}

// A pod without a tenant label is never collected early: the tenant is part
// of both the settled check and the selector.
func TestSettledRunCollectionNeedsTheTenantLabel(t *testing.T) {
	pod := runPod("a1", "run-1", corev1.PodSucceeded, settleNow)
	delete(pod.Labels, "leoflow.io/tenant-id")
	cs := fake.NewClientset(pod)
	runs := &fakeSettledRuns{settled: map[string]bool{"run-1": true, "/run-1": true}}
	r := settleCollectReconciler(cs, settleNow, runs)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runs.asked) != 0 || len(actionsOf(cs, "delete-collection")) != 0 {
		t.Fatalf("collected a pod with no tenant label: asked=%v", runs.asked)
	}
}

// A pod whose outcome could not be recorded keeps its run out of the
// collection, so the pod stays the signal a later sweep settles from.
func TestSettledRunCollectionWaitsForEverySettle(t *testing.T) {
	cs := fake.NewClientset(
		runPod("a1", "run-1", corev1.PodSucceeded, settleNow),
		runPod("a2", "run-1", corev1.PodFailed, settleNow),
	)
	r := NewReconciler(cs, "leoflow", failingReporter{})
	r.now = func() time.Time { return settleNow }
	runs := &fakeSettledRuns{settled: map[string]bool{"run-1": true}}
	r.SetSettledRunCollection(runs)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runs.asked) != 0 || len(actionsOf(cs, "delete-collection")) != 0 {
		t.Fatalf("collected a run with an unsettled pod: asked=%v", runs.asked)
	}
}

// A failing settled-run lookup collects nothing and does not fail the sweep.
func TestSettledRunCollectionLookupErrorCollectsNothing(t *testing.T) {
	cs := fake.NewClientset(runPod("a1", "run-1", corev1.PodSucceeded, settleNow))
	r := settleCollectReconciler(cs, settleNow, &fakeSettledRuns{err: errors.New("db down")})
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile = %v, want the sweep to complete", err)
	}
	if n := len(actionsOf(cs, "delete-collection")) + len(actionsOf(cs, "delete")); n != 0 {
		t.Fatalf("%d deletes after a failed lookup", n)
	}
}

// fieldSet adapts a map to fields.Fields for selector matching.
type fieldSet map[string]string

func (f fieldSet) Has(k string) bool   { _, ok := f[k]; return ok }
func (f fieldSet) Get(k string) string { return f[k] }

// failingReporter fails every settle, as on a database outage.
type failingReporter struct{}

func (failingReporter) FailTask(context.Context, string, int, int, string) error {
	return errors.New("db down")
}
func (failingReporter) SucceedTask(context.Context, string, int, int) error { return errors.New("db down") }
func (failingReporter) RescheduleTask(context.Context, string, int, int, time.Time) error {
	return errors.New("db down")
}
