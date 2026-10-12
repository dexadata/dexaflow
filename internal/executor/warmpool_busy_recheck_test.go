package executor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

// orderedWarmPods wraps fakeWarmPods to log every create and delete in order
// and to run onDelete before each delete, so a test can change the busy set
// between the deletes of one tick.
type orderedWarmPods struct {
	*fakeWarmPods
	events   []string
	onDelete func(name string)
}

func (o *orderedWarmPods) CreateWarmPod(ctx context.Context, t WarmTarget, anchorName, anchorUID string) (string, error) {
	o.events = append(o.events, "create "+t.DagVersionID)
	return o.fakeWarmPods.CreateWarmPod(ctx, t, anchorName, anchorUID)
}

func (o *orderedWarmPods) DeleteWarmPod(ctx context.Context, name string) error {
	if o.onDelete != nil {
		o.onDelete(name)
	}
	o.events = append(o.events, "delete "+name)
	return o.fakeWarmPods.DeleteWarmPod(ctx, name)
}

// liveBusy is a busy source whose answer can change during a tick, the way
// ListBusyWarmWorkerPods does once a binding lands. errAfter, when positive,
// makes every call after the first errAfter fail.
type liveBusy struct {
	mu       sync.Mutex
	busy     map[string]bool
	calls    int
	errAfter int
}

func (b *liveBusy) ListBusyWarmWorkerPods(context.Context) (map[string]bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	if b.errAfter > 0 && b.calls > b.errAfter {
		return nil, errors.New("db blip")
	}
	out := make(map[string]bool, len(b.busy))
	for k, v := range b.busy {
		out[k] = v
	}
	return out, nil
}

func (b *liveBusy) set(name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.busy[name] = true
}

func idleNames(prefix string, n int) []string {
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("%s-%d", prefix, i)
	}
	return names
}

// TestWarmReconcileKeepsWorkerThatBecameBusyDuringTick (#1601): a scale-down
// deletes excess idle workers one by one, paced by the client's rate limiter.
// A worker that takes an attempt (its binding lands) after the tick read the
// busy set but before its own delete must be kept.
func TestWarmReconcileKeepsWorkerThatBecameBusyDuringTick(t *testing.T) {
	names := idleNames("warm-B", 8)
	victim := names[7]
	busy := &liveBusy{busy: map[string]bool{}}
	pods := &orderedWarmPods{fakeWarmPods: &fakeWarmPods{existing: warmPods("B", names...)}}
	pods.onDelete = func(string) { busy.set(victim) } // bound while the first delete is in flight
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "B", Image: "img", EffectiveMinIdle: 1, MaxPoolSize: 8}}}

	r := NewWarmPoolReconciler(targets, pods, busy, 0, nil, nil)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	for _, d := range pods.deleted {
		if d == victim {
			t.Fatalf("deleted %s, which became busy during the tick; deleted=%v", victim, pods.deleted)
		}
	}
	if len(pods.deleted) != 6 {
		t.Fatalf("deleted %d workers, want the 6 still idle: %v", len(pods.deleted), pods.deleted)
	}
}

// TestWarmReconcileKeepsWorkersWhenBusyRecheckFails: when the busy set cannot be
// re-read before a delete, the worker is kept (do-no-harm), and an inactive
// version keeps its anchor because a pod still references it.
func TestWarmReconcileKeepsWorkersWhenBusyRecheckFails(t *testing.T) {
	busy := &liveBusy{busy: map[string]bool{}, errAfter: 1}
	pods := &orderedWarmPods{fakeWarmPods: &fakeWarmPods{existing: warmPods("old", idleNames("warm-old", 3)...)}}
	r := NewWarmPoolReconciler(&fakeWarmTargets{}, pods, busy, 0, nil, nil)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(pods.deleted) != 0 {
		t.Fatalf("deleted %v with the busy re-read failing, want none", pods.deleted)
	}
	if len(pods.deletedAnchors) != 0 {
		t.Fatalf("deleted anchors %v while the version still has pods", pods.deletedAnchors)
	}
}

// TestWarmReconcileDeletesBeforeCreates (#1601): creates are paced by the
// client's rate limiter and take minutes on a large tick, so every delete of
// the tick runs before the first create. Version A (listed first) needs
// creates; version B and the inactive version C have idle workers to delete.
func TestWarmReconcileDeletesBeforeCreates(t *testing.T) {
	existing := append(warmPods("B", idleNames("warm-B", 4)...), warmPods("C", "warm-C-0")...)
	pods := &orderedWarmPods{fakeWarmPods: &fakeWarmPods{existing: existing}}
	targets := &fakeWarmTargets{targets: []WarmTarget{
		{DagVersionID: "A", Image: "img", EffectiveMinIdle: 3, MaxPoolSize: 3},
		{DagVersionID: "B", Image: "img", EffectiveMinIdle: 1, MaxPoolSize: 4},
	}}
	r := NewWarmPoolReconciler(targets, pods, &liveBusy{busy: map[string]bool{}}, 0, nil, nil)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(pods.created) != 3 || len(pods.deleted) != 4 {
		t.Fatalf("created %d, deleted %d; want 3 and 4 (events %v)", len(pods.created), len(pods.deleted), pods.events)
	}
	seenCreate := false
	for _, ev := range pods.events {
		switch ev[:6] {
		case "create":
			seenCreate = true
		case "delete":
			if seenCreate {
				t.Fatalf("a delete ran after a create: %v", pods.events)
			}
		}
	}
}
