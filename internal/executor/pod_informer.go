package executor

import (
	"context"
	"errors"
	"strconv"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	listersv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
)

// Pod label keys the informer selects and filters on. They mirror exactly the
// keys BuildPod stamps and TaskPodPresence selects, labelValue-transformed;
// reusing the same transform is load-bearing: a lookup built from a different key
// would silently miss every pod and quietly return the storm PR-10 removes.
const (
	podLabelRunID     = "leoflow.io/run-id"
	podLabelTaskID    = "leoflow.io/task-id"
	podLabelTryNumber = "leoflow.io/try-number"
	// podLabelAttemptEpoch is not part of any server-side selector: a pod
	// stamped before the epoch existed has no such label and is epoch 0, and a
	// label selector cannot say "equals 0 or absent". It is filtered in Go by
	// podMatchesEpoch instead.
	podLabelAttemptEpoch = "leoflow.io/attempt-epoch"
)

// errCacheNotSynced is returned by SnapshotTaskPods before the informer's initial
// LIST has completed. It makes an unsynced snapshot a retriable condition rather
// than an authoritative "no pods exist" — a cold cache must never look empty to a
// consumer that could act on absence.
var errCacheNotSynced = errors.New("pod informer cache not synced")

// PodInformer is a shared-informer read-path over task pods, scoped by namespace
// and the leoflow.io/run-id label (ADR 0002 pods). It replaces the reapers'
// per-running-TI-per-second apiserver LIST storm and the reconciler's 30s LIST
// with one long-lived watch feeding a local cache.
//
// Its readings are trusted only in the safe direction (#461): CachedPodActive is
// consulted ONLY to DEFER a reap when a pod is present and Pending/Running — a
// cache "absent" reading is never authoritative and callers MUST fall through to
// the live TaskPodPresence (quorum) read before any destructive action. Cache lag
// can therefore only ever delay a reap by a tick, never cause a false-positive
// one. SnapshotTaskPods is safe for the reconciler because presence of a terminal
// pod is monotonic and every settle is attempt/state-guarded (ADR 0052).
//
// It is constructed only in the scheduler/all role (ADR 0049), never api-only,
// and is nil in Lite/subprocess (no pods), where consumers keep their live paths.
type PodInformer struct {
	factory   informers.SharedInformerFactory
	informer  cache.SharedIndexInformer
	lister    listersv1.PodLister
	namespace string
	stopCh    chan struct{}
	stopOnce  sync.Once
}

// NewPodInformer builds a shared pod informer over the given cluster, scoped to
// namespace and to pods carrying the leoflow.io/run-id label (managed task pods).
// Resync is 0: reads are level-triggered on demand, so there are no logic-bearing
// handlers to re-fire. It does not start watching until Start is called.
func NewPodInformer(clientset kubernetes.Interface, namespace string) *PodInformer {
	if namespace == "" {
		namespace = "default"
	}
	factory := informers.NewSharedInformerFactoryWithOptions(
		clientset, 0,
		informers.WithNamespace(namespace),
		informers.WithTweakListOptions(func(o *metav1.ListOptions) {
			// Existence selector: the reflector's chunked initial LIST and its
			// long-lived watch only ever carry managed task pods, keeping the
			// cache scoped and small.
			o.LabelSelector = podLabelRunID
		}),
	)
	pods := factory.Core().V1().Pods()
	informer := pods.Informer()
	// A source-mode pod carries its dag.py (up to 128 KiB) in an annotation
	// nothing here reads; keep it out of the cache (ADR 0067 §3). SetTransform
	// only fails once the informer has started, which it has not.
	_ = informer.SetTransform(dropSourceAnnotation) //nolint:errcheck // cannot fail before Start
	return &PodInformer{
		factory:   factory,
		informer:  informer,
		lister:    pods.Lister(),
		namespace: namespace,
		stopCh:    make(chan struct{}),
	}
}

// Start begins the watch in the background and stops it when ctx is canceled, so
// the informer is always-on for the process lifetime (warming the cache before
// leadership). It is idempotent-safe to call once per informer.
func (p *PodInformer) Start(ctx context.Context) {
	p.factory.Start(p.stopCh)
	go func() {
		<-ctx.Done()
		p.Shutdown()
	}()
}

// WaitForCacheSync blocks until the initial LIST has populated the cache or ctx is
// canceled, returning whether the sync completed. A false return (canceled or
// timed out) is not fatal: CachedPodActive independently gates on HasSynced and
// returns false until warm, so consumers simply keep using their live read paths.
func (p *PodInformer) WaitForCacheSync(ctx context.Context) bool {
	return cache.WaitForCacheSync(ctx.Done(), p.informer.HasSynced)
}

// HasSynced reports whether the initial LIST has populated the cache. It is the
// live form of WaitForCacheSync's one-shot answer, for a caller that must keep
// asking — the reaper's leader-settling gate — so a cache that synced late (a
// watch recovered after an RBAC fix) is seen, and one that never synced is not
// mistaken for an empty cluster.
func (p *PodInformer) HasSynced() bool {
	return p.informer.HasSynced()
}

// Shutdown stops the watch and waits for the informer goroutines to exit. Safe to
// call more than once (Start's ctx-cancel path and an explicit caller may race).
func (p *PodInformer) Shutdown() {
	p.stopOnce.Do(func() { close(p.stopCh) })
	p.factory.Shutdown()
}

// CachedPodActive reports whether the cache holds a pod for exactly the
// (run, task, try, epoch) attempt that is Pending or Running: the exact
// predicate TaskPodPresence uses, pinned to the same attempt (#723, ADR 0051
// amendment). It is the safe
// direction of the asymmetric-trust contract: a true return may DEFER a reap; a
// false return is NEVER authoritative and the caller must fall through to the live
// read. Before the cache has synced it returns false, so a cold cache degrades to
// the live path rather than misreporting absence.
func (p *PodInformer) CachedPodActive(a Attempt) bool {
	if !p.informer.HasSynced() {
		return false
	}
	selector := labels.SelectorFromSet(labels.Set{
		podLabelRunID:     labelValue(a.RunID),
		podLabelTaskID:    labelValue(a.TaskID),
		podLabelTryNumber: strconv.Itoa(a.TryNumber),
	})
	pods, err := p.lister.Pods(p.namespace).List(selector)
	if err != nil {
		return false
	}
	for _, pod := range pods {
		if !podMatchesEpoch(pod, a.AttemptEpoch) {
			continue
		}
		if phase := pod.Status.Phase; phase == corev1.PodPending || phase == corev1.PodRunning {
			return true
		}
	}
	return false
}

// SnapshotTaskPods returns the managed task pods currently in the cache — the
// reconciler's read replacement for its 30s LIST. It errors (errCacheNotSynced)
// before the initial sync so the reconciler retries next tick instead of acting on
// a cold cache that looks empty.
func (p *PodInformer) SnapshotTaskPods() ([]*corev1.Pod, error) {
	if !p.informer.HasSynced() {
		return nil, errCacheNotSynced
	}
	// The cache is already scoped to the run-id label by the factory's tweak, so
	// every pod it holds is a managed task pod.
	return p.lister.Pods(p.namespace).List(labels.Everything())
}

// dropSourceAnnotation is the pod informer's cache transform: it removes
// SourceAnnotation from a pod before the pod is stored, so the control plane
// does not hold every source-mode task's dag.py in memory. The informer hands
// the transform its own decoded copy, so editing it in place is safe. Anything
// that is not a pod (a tombstone) passes through unchanged.
func dropSourceAnnotation(obj any) (any, error) {
	if pod, ok := obj.(*corev1.Pod); ok {
		delete(pod.Annotations, SourceAnnotation)
	}
	return obj, nil
}
