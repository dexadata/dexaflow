package executor

import (
	"context"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	listersv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
)

// warmCreateExpectationTTL bounds how long a warm pod create the cache has not
// observed yet holds back further creates for its dag_version. A watch event is
// normally milliseconds behind the create; the bound only matters when the event
// is lost (the pod was deleted before the watch saw it), so a missed event delays
// refill by at most this long instead of forever.
const warmCreateExpectationTTL = 30 * time.Second

// WarmPodInformer is a dedicated shared-informer read path over the warm worker
// fleet (pods labeled leoflow.io/warm-worker=true), which the task-pod
// PodInformer does not cover: it selects on the run-id label warm pods do not
// carry. It replaces the warm-pool reconciler's per-tick LIST and drives
// event-driven refill: losing a worker (deleted, or reaching a terminal phase)
// signals Changes so the reconciler replaces it at once rather than on its next
// tick.
//
// Its readings are trusted only where staleness is harmless (ADR 0058, #461):
// the reconciler uses the cache to decide what to CREATE and which idle workers
// to trim, and confirms with a live LIST before the one cascading delete it
// performs (a drained version's GC anchor). The failover reapers keep their own
// live LIST; nothing here authorizes failing an attempt.
type WarmPodInformer struct {
	factory   informers.SharedInformerFactory
	informer  cache.SharedIndexInformer
	lister    listersv1.PodLister
	namespace string
	changes   chan struct{}
	stopCh    chan struct{}
	stopOnce  sync.Once

	// Create expectations (see WarmPodCache). inflight counts creates issued but
	// not returned, per dag_version label and per tenant; expected holds accepted
	// creates by pod name until the pod is observed; early holds pods observed
	// while a create of their version was in flight, so a watch event that beats
	// the create call is still matched to it; expired latches an expectation
	// that timed out unobserved until the reconciler reads it.
	mu             sync.Mutex
	inflight       map[string]int
	inflightTenant map[string]int
	expected       map[string]createExpectation
	early          map[string]time.Time
	// deleted holds the pods the informer saw deleted, for warmDeletedMemory,
	// so Covers can tell a pod the cache dropped on a delete event from one it
	// never saw (a cache behind the cluster).
	deleted map[string]time.Time
	// cachedNames lists the names of the cached pods; a seam for tests.
	cachedNames func() map[string]bool
	expired     bool
	now         func() time.Time
}

// createExpectation is one accepted create the cache has not observed yet.
type createExpectation struct {
	versionLabel string
	tenant       string
	at           time.Time
}

// NewWarmPodInformer builds the warm-pod informer for namespace. It does not
// watch until Start is called.
func NewWarmPodInformer(clientset kubernetes.Interface, namespace string) (*WarmPodInformer, error) {
	if namespace == "" {
		namespace = "default"
	}
	factory := informers.NewSharedInformerFactoryWithOptions(
		clientset, 0,
		informers.WithNamespace(namespace),
		informers.WithTweakListOptions(func(o *metav1.ListOptions) { o.LabelSelector = warmPodSelector }),
	)
	pods := factory.Core().V1().Pods()
	w := &WarmPodInformer{
		factory:        factory,
		informer:       pods.Informer(),
		lister:         pods.Lister(),
		namespace:      namespace,
		changes:        make(chan struct{}, 1),
		stopCh:         make(chan struct{}),
		inflight:       map[string]int{},
		inflightTenant: map[string]int{},
		expected:       map[string]createExpectation{},
		early:          map[string]time.Time{},
		deleted:        map[string]time.Time{},
		now:            time.Now,
	}
	w.cachedNames = w.listCachedNames
	if _, err := w.informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			// A create we were waiting for landed: the reconcile it held back
			// can run now.
			if p, ok := obj.(*corev1.Pod); ok && w.observed(p.Name, p.Labels[warmDagVersionLabelKey]) {
				w.Kick()
			}
		},
		UpdateFunc: func(oldObj, newObj any) {
			o, ok1 := oldObj.(*corev1.Pod)
			n, ok2 := newObj.(*corev1.Pod)
			if ok1 && ok2 && !podTerminal(o) && podTerminal(n) {
				w.Kick()
			}
		},
		DeleteFunc: func(obj any) {
			// A pod deleted before the watch reported its add never will be:
			// drop its expectation with it.
			if p, ok := podOf(obj); ok {
				w.forget(p.Name)
			}
			w.Kick()
		},
	}); err != nil {
		return nil, fmt.Errorf("registering warm pod event handler: %w", err)
	}
	return w, nil
}

// Start begins the watch and stops it when ctx is canceled.
func (w *WarmPodInformer) Start(ctx context.Context) {
	w.factory.Start(w.stopCh)
	go func() {
		<-ctx.Done()
		w.stopOnce.Do(func() { close(w.stopCh) })
		w.factory.Shutdown()
	}()
}

// WaitForCacheSync blocks until the initial LIST has populated the cache or ctx
// ends, and reports whether it synced. Not syncing is survivable: the reconciler
// reads the fleet live until CachedWarmPods reports synced.
func (w *WarmPodInformer) WaitForCacheSync(ctx context.Context) bool {
	return cache.WaitForCacheSync(ctx.Done(), w.informer.HasSynced)
}

// Changes delivers a signal each time the fleet lost a worker (or Kick was
// called). Signals coalesce: one pending signal stands for any number of events.
func (w *WarmPodInformer) Changes() <-chan struct{} { return w.changes }

// Kick requests a reconcile without blocking; the pool's claim hook uses it too.
func (w *WarmPodInformer) Kick() {
	select {
	case w.changes <- struct{}{}:
	default:
	}
}

// CachedWarmPods returns the cached warm fleet, mapped as ListWarmPods maps it,
// and whether the cache has synced. Before the first sync it returns false, so a
// cold cache is never read as an empty fleet.
func (w *WarmPodInformer) CachedWarmPods() ([]WarmPodInfo, bool) {
	if !w.informer.HasSynced() {
		return nil, false
	}
	pods, err := w.lister.Pods(w.namespace).List(labels.Everything())
	if err != nil {
		return nil, false
	}
	out := make([]WarmPodInfo, 0, len(pods))
	for _, p := range pods {
		out = append(out, warmPodInfoOf(p))
	}
	return out, true
}

// BeginCreate records a create about to be issued for t, before the call so a
// watch event that beats the caller is still matched.
func (w *WarmPodInformer) BeginCreate(t WarmTarget) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.inflight[sanitizeLabel(t.DagVersionID)]++
	w.inflightTenant[t.TenantID]++
}

// EndCreate records that the create BeginCreate announced returned, with the
// created pod's name, or "" when it failed (no expectation is kept). A pod the
// watch already reported is matched here and leaves no expectation behind.
func (w *WarmPodInformer) EndCreate(t WarmTarget, podName string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	key := sanitizeLabel(t.DagVersionID)
	w.inflight[key]--
	if w.inflight[key] <= 0 {
		delete(w.inflight, key)
	}
	w.inflightTenant[t.TenantID]--
	if w.inflightTenant[t.TenantID] <= 0 {
		delete(w.inflightTenant, t.TenantID)
	}
	if podName == "" {
		return
	}
	if _, seen := w.early[podName]; seen {
		delete(w.early, podName)
		return
	}
	w.expected[podName] = createExpectation{versionLabel: key, tenant: t.TenantID, at: w.now()}
}

// CreatesPending reports whether dagVersionID has a create in flight or one the
// cache has not observed yet (younger than warmCreateExpectationTTL).
func (w *WarmPodInformer) CreatesPending(dagVersionID string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pruneLocked()
	key := sanitizeLabel(dagVersionID)
	if w.inflight[key] > 0 {
		return true
	}
	for _, e := range w.expected {
		if e.versionLabel == key {
			return true
		}
	}
	return false
}

// PendingCreatesByTenant counts, per tenant, the creates in flight or not yet
// observed, across every dag_version.
func (w *WarmPodInformer) PendingCreatesByTenant() map[string]int {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pruneLocked()
	out := make(map[string]int, len(w.inflightTenant))
	for tenant, n := range w.inflightTenant {
		out[tenant] += n
	}
	for _, e := range w.expected {
		out[e.tenant]++
	}
	return out
}

// ExpectationExpired reports whether an expectation timed out unobserved since
// the last call, and clears the latch.
func (w *WarmPodInformer) ExpectationExpired() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pruneLocked()
	e := w.expired
	w.expired = false
	return e
}

// pruneLocked drops expectations and early sightings older than
// warmCreateExpectationTTL, latching expired when an expectation goes. w.mu held.
func (w *WarmPodInformer) pruneLocked() {
	now := w.now()
	for name, e := range w.expected {
		if now.Sub(e.at) >= warmCreateExpectationTTL {
			delete(w.expected, name)
			w.expired = true
		}
	}
	for name, at := range w.early {
		if now.Sub(at) >= warmCreateExpectationTTL {
			delete(w.early, name)
		}
	}
	for name, at := range w.deleted {
		if now.Sub(at) >= warmDeletedMemory {
			delete(w.deleted, name)
		}
	}
}

// warmDeletedMemory is how long the informer remembers a pod it saw deleted.
// It outlasts warmCacheLiveListInterval, so a pod a live LIST returned and that
// was deleted since still counts as covered by a healthy cache.
const warmDeletedMemory = 2 * warmCacheLiveListInterval

// Covers reports whether the cache has caught up with the named pods (the ones
// the reconciler's last live LIST returned): each is cached, or the informer saw
// it deleted. A name it never saw means the cache is behind the cluster (a hung
// watch), so the reconciler reads live instead of creating off a stale count.
func (w *WarmPodInformer) Covers(names []string) bool {
	if len(names) == 0 {
		return true
	}
	have := w.cachedNames()
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pruneLocked()
	for _, n := range names {
		if have[n] {
			continue
		}
		if _, gone := w.deleted[n]; gone {
			continue
		}
		return false
	}
	return true
}

// listCachedNames returns the names of the pods in the informer's cache.
func (w *WarmPodInformer) listCachedNames() map[string]bool {
	pods, err := w.lister.Pods(w.namespace).List(labels.Everything())
	if err != nil {
		return nil
	}
	out := make(map[string]bool, len(pods))
	for _, p := range pods {
		out[p.Name] = true
	}
	return out
}

// observed matches an added pod to its create expectation and reports whether
// it was one. A pod whose create is still in flight for its version is kept as
// an early sighting for EndCreate; any other pod is unrelated and consumes
// nothing.
func (w *WarmPodInformer) observed(podName, versionLabel string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.expected[podName]; ok {
		delete(w.expected, podName)
		return true
	}
	if w.inflight[versionLabel] > 0 {
		w.early[podName] = w.now()
	}
	return false
}

// forget drops any expectation or early sighting of a deleted pod and
// remembers the delete for Covers.
func (w *WarmPodInformer) forget(podName string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.expected, podName)
	delete(w.early, podName)
	w.deleted[podName] = w.now()
}

// podOf unwraps a delete notification, which may be a tombstone.
func podOf(obj any) (*corev1.Pod, bool) {
	if t, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = t.Obj
	}
	p, ok := obj.(*corev1.Pod)
	return p, ok
}

// podTerminal reports whether a pod reached Succeeded or Failed. Warm pods are
// RestartPolicy:Never, so a terminal warm pod can never serve again.
func podTerminal(p *corev1.Pod) bool {
	return p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed
}

var _ WarmPodCache = (*WarmPodInformer)(nil)
