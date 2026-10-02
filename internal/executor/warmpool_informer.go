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

	mu      sync.Mutex
	pending map[string][]time.Time // dag_version -> unobserved creates, oldest first
	now     func() time.Time
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
		factory:   factory,
		informer:  pods.Informer(),
		lister:    pods.Lister(),
		namespace: namespace,
		changes:   make(chan struct{}, 1),
		stopCh:    make(chan struct{}),
		pending:   map[string][]time.Time{},
		now:       time.Now,
	}
	if _, err := w.informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			// A create we were waiting for landed: the reconcile it held back
			// can run now.
			if p, ok := obj.(*corev1.Pod); ok && w.observed(p.Labels[warmDagVersionLabelKey]) {
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
		DeleteFunc: func(any) { w.Kick() },
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

// ExpectCreate records a create about to be issued for dagVersionID, before the
// call so a watch event that beats the caller is still matched.
func (w *WarmPodInformer) ExpectCreate(dagVersionID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending[sanitizeLabel(dagVersionID)] = append(w.pending[sanitizeLabel(dagVersionID)], w.now())
}

// CreateFailed withdraws one expectation after a create the apiserver rejected.
func (w *WarmPodInformer) CreateFailed(dagVersionID string) {
	_ = w.observed(sanitizeLabel(dagVersionID))
}

// CreatesPending reports whether dagVersionID has creates the cache has not
// observed yet (younger than warmCreateExpectationTTL).
func (w *WarmPodInformer) CreatesPending(dagVersionID string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	key := sanitizeLabel(dagVersionID)
	live := w.pending[key][:0]
	for _, at := range w.pending[key] {
		if w.now().Sub(at) < warmCreateExpectationTTL {
			live = append(live, at)
		}
	}
	if len(live) == 0 {
		delete(w.pending, key)
		return false
	}
	w.pending[key] = live
	return true
}

// observed consumes the oldest expectation of a (sanitized) dag_version label
// and reports whether there was one.
func (w *WarmPodInformer) observed(versionLabel string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	q := w.pending[versionLabel]
	if len(q) == 0 {
		return false
	}
	w.pending[versionLabel] = q[1:]
	return true
}

// podTerminal reports whether a pod reached Succeeded or Failed. Warm pods are
// RestartPolicy:Never, so a terminal warm pod can never serve again.
func podTerminal(p *corev1.Pod) bool {
	return p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed
}

var _ WarmPodCache = (*WarmPodInformer)(nil)
