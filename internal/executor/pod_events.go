package executor

import (
	"context"
	"log/slog"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

// podEventSettleTimeout bounds one event settle: the outcome write and, for a
// pod the verdict collects, its delete.
const podEventSettleTimeout = 10 * time.Second

// podEventMaxRetries is how often a failed event settle is retried, backing
// off from podEventRetryBase to podEventRetryMax (about 30 s in all), before
// the attempt is left to the next sweep, or for a deleted pod to the reapers.
const (
	podEventMaxRetries = 5
	podEventRetryBase  = time.Second
	podEventRetryMax   = 30 * time.Second
)

// PodEventSettler settles a task pod's attempt as soon as the pod informer
// sees it turn terminal (#2330), instead of on the reconciler's next 30 s
// sweep: an OOMKilled container, an eviction or a pod the cluster stopped is
// recorded within the watch's latency. It is an accelerator, not a second
// source of truth: it runs the sweep's own per-pod settle (SettleFromEvent),
// every settle stays attempt- and state-guarded (ADR 0052), and the sweep
// keeps running as the resync that repairs a missed or failed event.
//
// Every settle runs under the lock the maintenance cycle holds for its sweep
// and reap, so the order between the reconciler and the reapers is unchanged,
// and only while the replica leads; a follower drops the event, and the
// leader's first sweep covers it. It reads pods from the informer's cache
// only, so it adds no apiserver read: the watch is already open.
type PodEventSettler struct {
	rec     *Reconciler
	pods    *PodInformer
	leading func() bool
	mu      sync.Locker
	queue   workqueue.TypedRateLimitingInterface[string]
	// gone holds the last state of a terminal pod deleted before the worker
	// read it, by key, so the delete does not lose its outcome.
	gone sync.Map
}

// NewPodEventSettler registers update and delete handlers on the informer's
// pods that queue a pod's key when it turns terminal. mu is the maintenance
// cycle's lock; leading gates every settle on leadership. Run starts the
// worker.
func NewPodEventSettler(rec *Reconciler, pods *PodInformer, leading func() bool, mu sync.Locker) *PodEventSettler {
	s := &PodEventSettler{
		rec:     rec,
		pods:    pods,
		leading: leading,
		mu:      mu,
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.NewTypedItemExponentialFailureRateLimiter[string](podEventRetryBase, podEventRetryMax),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: "task-pod-events"},
		),
	}
	// Handlers only fail once the informer has stopped.
	_, _ = pods.informer.AddEventHandler(cache.ResourceEventHandlerFuncs{ //nolint:errcheck // cannot fail on a live informer
		UpdateFunc: s.onUpdate,
		DeleteFunc: s.onDelete,
	})
	return s
}

// eventSettles reports whether a verdict is one the event path settles: a
// failure, which is what waits on the sweep otherwise. A success or a
// reschedule was reported by the agent itself, and a reschedule must not be
// applied twice (see Reconcile), so those stay with the sweep.
func eventSettles(v verdict) bool {
	return v.terminal && (v.settle == settleFailed || v.settle == settleInfra)
}

// onUpdate queues a pod that failed now, and a pod waiting on a pull for
// when its grace runs out. A pod that was already terminal is queued again
// only when its status changed, so the informer's relists do not resettle it.
func (s *PodEventSettler) onUpdate(oldObj, newObj any) {
	pod, ok := newObj.(*corev1.Pod)
	if !ok {
		return
	}
	key, err := cache.MetaNamespaceKeyFunc(pod)
	if err != nil {
		return
	}
	if eventSettles(classifyPod(pod)) {
		if old, ok := oldObj.(*corev1.Pod); ok && classifyPod(old).terminal && old.ResourceVersion == pod.ResourceVersion {
			return
		}
		s.queue.Add(key)
		return
	}
	if pod.Status.Phase == corev1.PodPending && pullWaiting(pod) != nil {
		s.queue.AddAfter(key, max(time.Until(placedAt(pod).Add(imagePullGrace)), 0))
	}
}

// onDelete keeps a failed pod's last state and queues it, for a pod deleted
// before its update was settled (a node's pod garbage collector). Most deletes
// are the reconciler's own collections of pods it settled already; settling
// those again is an attempt-guarded no-op. A pod deleted while it ran is the
// reaper's, which reads liveness.
func (s *PodEventSettler) onDelete(obj any) {
	if tomb, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = tomb.Obj
	}
	pod, ok := obj.(*corev1.Pod)
	if !ok || !eventSettles(classifyPod(pod)) {
		return
	}
	key, err := cache.MetaNamespaceKeyFunc(pod)
	if err != nil {
		return
	}
	s.gone.Store(key, pod)
	s.queue.Add(key)
}

// Run settles queued pods until ctx ends.
func (s *PodEventSettler) Run(ctx context.Context) {
	go func() {
		<-ctx.Done()
		s.queue.ShutDown()
	}()
	for s.next(ctx) {
	}
}

// next settles one queued pod and reports whether the queue is still open.
func (s *PodEventSettler) next(ctx context.Context) bool {
	key, shutdown := s.queue.Get()
	if shutdown {
		return false
	}
	defer s.queue.Done(key)
	if err := s.settle(ctx, key); err != nil && s.queue.NumRequeues(key) < podEventMaxRetries {
		s.queue.AddRateLimited(key)
		return true
	}
	s.queue.Forget(key)
	s.gone.Delete(key)
	return true
}

// settle runs the sweep's per-pod settle on the cached pod, or on the last
// state of a deleted one, under the maintenance lock and only while leading.
// The pod is read under the lock, so a sweep that ran meanwhile is seen.
func (s *PodEventSettler) settle(ctx context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.leading() {
		s.gone.Delete(key)
		return nil
	}
	pod, present := s.pods.cachedPod(key)
	if !present {
		v, ok := s.gone.LoadAndDelete(key)
		if !ok {
			return nil
		}
		if pod, ok = v.(*corev1.Pod); !ok {
			return nil
		}
	} else {
		s.gone.Delete(key)
	}
	sctx, cancel := context.WithTimeout(ctx, podEventSettleTimeout)
	defer cancel()
	settled, err := s.rec.SettleFromEvent(sctx, pod, present)
	if err != nil {
		if !present {
			// Keep the deleted pod's state for the retry: nothing else holds
			// it, as the sweep no longer lists the pod.
			s.gone.LoadOrStore(key, pod)
		}
		slog.Warn("settling a failed task pod from its event; retrying", "pod", key, "error", err)
		return err
	}
	if settled {
		s.rec.recordDecision("pod_event_settled")
	}
	return nil
}

// cachedPod returns the informer's cached pod for a namespace/name key.
func (p *PodInformer) cachedPod(key string) (*corev1.Pod, bool) {
	obj, ok, err := p.informer.GetIndexer().GetByKey(key)
	if err != nil || !ok {
		return nil, false
	}
	pod, ok := obj.(*corev1.Pod)
	return pod, ok
}
