package executor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeWarmCache is a canned WarmPodCache: a cached fleet, whether it has synced,
// per-version pending creates, pending creates per tenant, whether an
// expectation expired, and a record of the creates it was told about.
type fakeWarmCache struct {
	mu              sync.Mutex
	pods            []WarmPodInfo
	synced          bool
	pending         map[string]bool
	pendingByTenant map[string]int
	expired         bool
	begun           map[string]int
	ended           map[string][]string // dag_version -> returned pod names ("" = failed)
}

func (c *fakeWarmCache) CachedWarmPods() ([]WarmPodInfo, bool) { return c.pods, c.synced }

func (c *fakeWarmCache) BeginCreate(t WarmTarget) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.begun == nil {
		c.begun = map[string]int{}
	}
	c.begun[t.DagVersionID]++
}

func (c *fakeWarmCache) EndCreate(t WarmTarget, podName string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ended == nil {
		c.ended = map[string][]string{}
	}
	c.ended[t.DagVersionID] = append(c.ended[t.DagVersionID], podName)
}

func (c *fakeWarmCache) CreatesPending(dv string) bool { return c.pending[dv] }

func (c *fakeWarmCache) PendingCreatesByTenant() map[string]int { return c.pendingByTenant }

func (c *fakeWarmCache) ExpectationExpired() bool {
	e := c.expired
	c.expired = false
	return e
}

// countingWarmPods wraps fakeWarmPods to count live LIST calls and to make
// creates concurrency-safe, recording the peak number in flight. gate, when set,
// holds every create until it is closed.
type countingWarmPods struct {
	*fakeWarmPods
	mu        sync.Mutex
	listCalls int
	inFlight  int
	peak      int
	gate      chan struct{}
}

func (c *countingWarmPods) ListWarmPods(ctx context.Context) ([]WarmPodInfo, error) {
	c.mu.Lock()
	c.listCalls++
	c.mu.Unlock()
	return c.fakeWarmPods.ListWarmPods(ctx)
}

func (c *countingWarmPods) CreateWarmPod(ctx context.Context, t WarmTarget, anchorName, anchorUID string) (string, error) {
	c.mu.Lock()
	c.inFlight++
	if c.inFlight > c.peak {
		c.peak = c.inFlight
	}
	c.mu.Unlock()
	if c.gate != nil {
		<-c.gate
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inFlight--
	return c.fakeWarmPods.CreateWarmPod(ctx, t, anchorName, anchorUID)
}

// TestWarmReconcileReadsTheFleetFromASyncedCache: with a synced cache the tick
// makes no LIST call, and it acts on what the cache holds.
func TestWarmReconcileReadsTheFleetFromASyncedCache(t *testing.T) {
	pods := &countingWarmPods{fakeWarmPods: &fakeWarmPods{listErr: errors.New("LIST must not be called")}}
	cache := &fakeWarmCache{synced: true, pods: warmPods("dv-1", "w-1")}
	r := NewWarmPoolReconciler(&fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv-1", EffectiveMinIdle: 3, MaxPoolSize: 8}}}, pods, busySet(), 0, nil, nil)
	r.SetCache(cache, 1)

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if pods.listCalls != 0 {
		t.Errorf("live LIST calls = %d, want 0 with a synced cache", pods.listCalls)
	}
	if len(pods.created) != 2 {
		t.Errorf("created = %d, want 2 (target 3, one cached idle worker)", len(pods.created))
	}
	if cache.begun["dv-1"] != 2 {
		t.Errorf("expectations raised = %d, want one per create", cache.begun["dv-1"])
	}
	for _, name := range cache.ended["dv-1"] {
		if name == "" {
			t.Errorf("an accepted create must hand the cache its pod name, got %v", cache.ended["dv-1"])
		}
	}
}

// TestWarmReconcileFallsBackToLiveListOnAColdCache: a cache that has not synced
// is never read as an empty fleet.
func TestWarmReconcileFallsBackToLiveListOnAColdCache(t *testing.T) {
	pods := &countingWarmPods{fakeWarmPods: &fakeWarmPods{existing: warmPods("dv-1", "w-1", "w-2")}}
	cache := &fakeWarmCache{synced: false}
	r := NewWarmPoolReconciler(&fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv-1", EffectiveMinIdle: 2, MaxPoolSize: 8}}}, pods, busySet(), 0, nil, nil)
	r.SetCache(cache, 4)

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if pods.listCalls != 1 {
		t.Errorf("live LIST calls = %d, want 1 on a cold cache", pods.listCalls)
	}
	if len(pods.created) != 0 {
		t.Errorf("created = %d, want 0: the live LIST shows the target already met", len(pods.created))
	}
}

// TestWarmReconcileHoldsCreatesWhileExpectationsPending: a version whose earlier
// creates the cache has not observed yet gets no new creates, so a lagging cache
// cannot push the pool past its target or its MaxPoolSize.
func TestWarmReconcileHoldsCreatesWhileExpectationsPending(t *testing.T) {
	pods := &countingWarmPods{fakeWarmPods: &fakeWarmPods{}}
	cache := &fakeWarmCache{synced: true, pending: map[string]bool{"dv-1": true}}
	r := NewWarmPoolReconciler(&fakeWarmTargets{targets: []WarmTarget{
		{DagVersionID: "dv-1", EffectiveMinIdle: 2, MaxPoolSize: 8},
		{DagVersionID: "dv-2", EffectiveMinIdle: 1, MaxPoolSize: 8},
	}}, pods, busySet(), 0, nil, nil)
	r.SetCache(cache, 1)

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	for _, c := range pods.created {
		if c.DagVersionID == "dv-1" {
			t.Fatal("created a dv-1 worker while its earlier creates were still pending")
		}
	}
	if len(pods.created) != 1 {
		t.Errorf("created = %d, want 1 (dv-2 only)", len(pods.created))
	}
}

// TestWarmReconcileConfirmsAnchorDeleteLive: the anchor delete cascades to every
// pod that still references it, so the cached "drained" reading is never enough
// to authorize it. A live LIST must confirm zero pods first.
func TestWarmReconcileConfirmsAnchorDeleteLive(t *testing.T) {
	pods := &countingWarmPods{fakeWarmPods: &fakeWarmPods{existing: warmPods("dv-old", "w-new")}}
	// The cache still holds a terminal pod of the inactive version (so the
	// version is visited) but has not seen the live one yet.
	cache := &fakeWarmCache{synced: true, pods: []WarmPodInfo{{Name: "w-dead", DagVersionID: "dv-old", Terminal: true}}}
	r := NewWarmPoolReconciler(&fakeWarmTargets{}, pods, busySet(), 0, nil, nil)
	r.SetCache(cache, 1)

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(pods.deletedAnchors) != 0 {
		t.Errorf("deleted anchors %v although a live pod still references it", pods.deletedAnchors)
	}
	if pods.listCalls != 1 {
		t.Errorf("live LIST calls = %d, want exactly the one confirming the anchor delete", pods.listCalls)
	}

	pods.existing = nil
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(pods.deletedAnchors) != 1 {
		t.Errorf("deleted anchors = %v, want dv-old once the live LIST confirms it drained", pods.deletedAnchors)
	}
}

// TestWarmReconcileCreatesConcurrentlyWithABound: refill creates run in parallel,
// never more than the configured bound at once, and all of them complete.
func TestWarmReconcileCreatesConcurrentlyWithABound(t *testing.T) {
	gate := make(chan struct{})
	pods := &countingWarmPods{fakeWarmPods: &fakeWarmPods{}, gate: gate}
	cache := &fakeWarmCache{synced: true}
	r := NewWarmPoolReconciler(&fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv-1", EffectiveMinIdle: 7, MaxPoolSize: 8}}}, pods, busySet(), 0, nil, nil)
	r.SetCache(cache, 3)

	done := make(chan error, 1)
	go func() { done <- r.Reconcile(context.Background()) }()
	if !waitFor(t, func() bool {
		pods.mu.Lock()
		defer pods.mu.Unlock()
		return pods.inFlight == 3
	}) {
		t.Fatal("creates did not run concurrently up to the bound")
	}
	close(gate)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Reconcile did not finish")
	}
	if pods.peak != 3 {
		t.Errorf("peak concurrent creates = %d, want the bound 3", pods.peak)
	}
	if len(pods.created) != 7 {
		t.Errorf("created = %d, want 7", len(pods.created))
	}
}

// TestWarmReconcileReadsLiveAfterAnExpectationExpired: an expectation that
// expired unobserved means the cache missed a create it should have seen, so it
// may be missing more. The next reconcile reads the fleet live before creating,
// so a stale cache cannot keep creating workers that already exist.
func TestWarmReconcileReadsLiveAfterAnExpectationExpired(t *testing.T) {
	pods := &countingWarmPods{fakeWarmPods: &fakeWarmPods{existing: warmPods("dv-1", "w-1", "w-2", "w-3")}}
	cache := &fakeWarmCache{synced: true, expired: true}
	r := NewWarmPoolReconciler(&fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv-1", EffectiveMinIdle: 3, MaxPoolSize: 8}}}, pods, busySet(), 0, nil, nil)
	r.SetCache(cache, 1)

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if pods.listCalls != 1 {
		t.Errorf("live LIST calls = %d, want 1 after an expectation expired", pods.listCalls)
	}
	if len(pods.created) != 0 {
		t.Errorf("created = %d, want 0: the live fleet already meets the target", len(pods.created))
	}
}

// TestWarmReconcileReadsLivePeriodically: even with no expired expectation, the
// reconciler checks the cache against a live LIST every warmCacheLiveListInterval,
// so a cache that silently lost pods cannot drive creates for long.
func TestWarmReconcileReadsLivePeriodically(t *testing.T) {
	pods := &countingWarmPods{fakeWarmPods: &fakeWarmPods{existing: warmPods("dv-1", "w-1", "w-2")}}
	cache := &fakeWarmCache{synced: true}
	r := NewWarmPoolReconciler(&fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv-1", EffectiveMinIdle: 2, MaxPoolSize: 8}}}, pods, busySet(), 0, nil, nil)
	now := time.Now()
	r.now = func() time.Time { return now }
	r.SetCache(cache, 1)

	now = now.Add(warmCacheLiveListInterval / 2)
	cache.pods = warmPods("dv-1", "w-1", "w-2")
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if pods.listCalls != 0 {
		t.Fatalf("live LIST calls = %d, want 0 before the interval", pods.listCalls)
	}

	now = now.Add(warmCacheLiveListInterval)
	cache.pods = nil // the cache lost both workers
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if pods.listCalls != 1 {
		t.Errorf("live LIST calls = %d, want 1 once the interval passed", pods.listCalls)
	}
	if len(pods.created) != 0 {
		t.Errorf("created = %d, want 0: the live fleet still meets the target", len(pods.created))
	}
}

// TestWarmReconcileCountsPendingCreatesAgainstTheTenantCap: creates the cache
// has not observed yet still count toward their tenant's aggregate budget,
// whatever version they belong to (a draining one included), so another version
// of the same tenant cannot overshoot MaxWarmPodsPerTenant off a lagging cache.
func TestWarmReconcileCountsPendingCreatesAgainstTheTenantCap(t *testing.T) {
	pods := &countingWarmPods{fakeWarmPods: &fakeWarmPods{}}
	cache := &fakeWarmCache{synced: true, pendingByTenant: map[string]int{"acme": 3}}
	r := NewWarmPoolReconciler(&fakeWarmTargets{targets: []WarmTarget{
		{DagVersionID: "dv-2", TenantID: "acme", EffectiveMinIdle: 1, MaxPoolSize: 8},
	}}, pods, busySet(), 3, nil, nil)
	r.SetCache(cache, 1)

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(pods.created) != 0 {
		t.Errorf("created = %d, want 0: three pending creates already fill acme's cap of 3", len(pods.created))
	}
}
