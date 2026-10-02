package executor

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

// fakeSettledRuns answers SettledRuns from a fixed set and records each ask.
type fakeSettledRuns struct {
	settled map[string]bool
	asked   [][]string
	err     error
}

func (f *fakeSettledRuns) SettledRuns(_ context.Context, ids []string) (map[string]bool, error) {
	f.asked = append(f.asked, ids)
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]bool{}
	for _, id := range ids {
		if f.settled[id] {
			out[id] = true
		}
	}
	return out, nil
}

// runPod is a young task pod of run, so the age-based GC never collects it.
func runPod(name, run string, phase corev1.PodPhase, now time.Time) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "leoflow", CreationTimestamp: metav1.NewTime(now.Add(-time.Minute)),
			Labels:      map[string]string{"leoflow.io/run-id": run, "leoflow.io/try-number": "1"},
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
// its run label, restricted to finished phases, with no per-pod delete.
// Runs that are not settled, or still have a live pod, are left alone.
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
	if len(runs.asked) != 1 || len(runs.asked[0]) != 2 {
		t.Fatalf("asked = %v, want one batched ask for run-1 and run-2 (run-3 has a live pod)", runs.asked)
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
	if got := restr.Labels.String(); got != "leoflow.io/run-id=run-1" {
		t.Errorf("label selector = %q, want leoflow.io/run-id=run-1", got)
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
// run's finished pods one by one, and stops asking for the verb afterwards.
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

func (failingReporter) FailTask(context.Context, string, int, string) error {
	return errors.New("db down")
}
func (failingReporter) SucceedTask(context.Context, string, int) error { return errors.New("db down") }
func (failingReporter) RescheduleTask(context.Context, string, int, time.Time) error {
	return errors.New("db down")
}
