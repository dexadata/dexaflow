package executor

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

// TestWarmCacheBehindLiveListIsNotTrusted: the informer has synced, then its
// watch silently delivers nothing (a hung watch; the reflector only restarts it
// after its 5 to 10 minute timeout). The creates' expectations expire after 30s
// and one tick reads the fleet live. The cache still lacks the pods that live
// LIST returned, so it must not be trusted again until it catches up: trusting
// it made the reconciler create the shortfall again and trim it on the next live
// read, a create/trim churn above MaxPoolSize every minute.
func TestWarmCacheBehindLiveListIsNotTrusted(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cs.PrependWatchReactor("pods", func(ktesting.Action) (bool, watch.Interface, error) {
		return true, watch.NewFake(), nil // never emits
	})
	ns := "leoflow"
	wi, err := NewWarmPodInformer(cs, ns)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wi.Start(ctx)
	if !wi.WaitForCacheSync(ctx) {
		t.Fatal("warm pod informer did not sync")
	}
	clock := time.Now()
	now := func() time.Time { return clock }
	wi.now = now

	k := NewKubernetesWarmPods(cs, ns, func(t WarmTarget) (WarmPodSpec, error) {
		return WarmPodSpec{DagVersionID: t.DagVersionID, Image: "img", TenantID: t.TenantID}, nil
	})
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", Image: "img", EffectiveMinIdle: 2, MaxPoolSize: 2, TenantID: "t1"}}}
	r := NewWarmPoolReconciler(targets, k, busySet(), 0, nil, nil)
	r.now = now
	r.SetCache(wi, 4)

	live := func() int {
		l, _ := cs.CoreV1().Pods(ns).List(context.Background(), metav1.ListOptions{LabelSelector: warmPodSelector})
		return len(l.Items)
	}
	peak := 0
	for i := range 8 {
		if err := r.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
		peak = max(peak, live())
		t.Logf("tick %d (+%ds): live warm pods = %d", i, i*31, live())
		clock = clock.Add(31 * time.Second)
	}
	if peak > 2 {
		t.Fatalf("peak live warm pods = %d, want <= MaxPoolSize 2 (a stale cache drove create/trim churn)", peak)
	}
}

// TestWarmPodInformerCovers: a pod name is covered when the cache holds it or
// the informer saw it deleted; a name it never saw is not.
func TestWarmPodInformerCovers(t *testing.T) {
	w := &WarmPodInformer{deleted: map[string]time.Time{}, now: time.Now}
	w.cachedNames = func() map[string]bool { return map[string]bool{"a": true} }
	w.forget("gone")
	if !w.Covers([]string{"a", "gone"}) {
		t.Error("a cached pod and a pod seen deleted must be covered")
	}
	if w.Covers([]string{"a", "never-seen"}) {
		t.Error("a pod the cache never saw must not be covered")
	}
	if !w.Covers(nil) {
		t.Error("nothing to cover is covered")
	}
}

// TestWarmCacheTrustedAgainOnceItCatchesUp: after a live read, a cache that
// holds every pod that read returned is trusted again (no extra live LIST).
func TestWarmCacheTrustedAgainOnceItCatchesUp(t *testing.T) {
	pods := &countingWarmPods{fakeWarmPods: &fakeWarmPods{existing: warmPods("dv1", "w1", "w2")}}
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", EffectiveMinIdle: 2, MaxPoolSize: 2}}}
	r := NewWarmPoolReconciler(targets, pods, busySet(), 0, nil, nil)
	cache := &fakeWarmCache{synced: true, pods: nil, expired: true, uncovered: map[string]bool{"w1": true, "w2": true}}
	r.SetCache(cache, 4)

	reconcileN(t, r, 1) // expectation expired: live read records w1, w2
	listsAfterLive := pods.listCalls
	reconcileN(t, r, 1) // cache lacks w1, w2: must read live again, not create
	if pods.listCalls != listsAfterLive+1 {
		t.Fatalf("live LISTs = %d, want %d: a cache behind the last live LIST must not be trusted", pods.listCalls, listsAfterLive+1)
	}
	if len(pods.created) != 0 {
		t.Fatalf("created %d pods off a cache that lags the live fleet", len(pods.created))
	}
	cache.uncovered = nil
	cache.pods = warmPods("dv1", "w1", "w2")
	before := pods.listCalls
	reconcileN(t, r, 1)
	if pods.listCalls != before {
		t.Fatalf("live LISTs = %d, want %d: a cache that caught up must be trusted again", pods.listCalls, before)
	}
}

func reconcileN(t *testing.T, r *WarmPoolReconciler, n int) {
	t.Helper()
	for range n {
		if err := r.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}
