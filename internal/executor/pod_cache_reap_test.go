package executor

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// fakePresenceCache is a PodPresenceCache whose readings a test controls. It is
// the safe-direction seam (#461): a true reading may DEFER a reap; a false
// reading is never authoritative and the reaper must fall through to the live
// TaskPodPresence read.
type fakePresenceCache struct {
	active map[string]bool // "runID/taskID/try" -> cached Pending/Running
}

func (c *fakePresenceCache) CachedPodActive(a Attempt) bool {
	return c.active[fmt.Sprintf("%s/%s/%d", a.RunID, a.TaskID, a.TryNumber)]
}

// --- pod-lost reaper -------------------------------------------------------

// TestPodLostReaper_CacheAbsentButLiveRunning_DoesNotReap is the #461 regression
// lock, mechanized. The cache says the pod is ABSENT (a lagged/cold read), but the
// live TaskPodPresence read says it is Running. The reaper MUST fall through to the
// live read and DEFER — cache absence is never authoritative, so cache lag can
// only delay a reap, never cause a false-positive one. Reaping here would kill a
// live task.
func TestPodLostReaper_CacheAbsentButLiveRunning_DoesNotReap(t *testing.T) {
	past := time.Now().UTC().Add(-2 * time.Minute)
	store := &fakePodLostStore{candidates: []PodLostCandidate{
		{TaskInstanceID: "live", DagRunID: "run-a", TaskID: "work", TryNumber: 1, RunningSince: past},
	}}
	pods := &fakePodManager{active: map[string]bool{"run-a/work/1": true}} // live says Running
	r := newPodLostReaper(store, reapTestLogger(), 60*time.Second, nil)
	r.pods = pods
	r.cache = &fakePresenceCache{active: map[string]bool{}} // cache says absent (lag)

	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.marked) != 0 {
		t.Fatalf("#461: cache-absent must fall through to live; a live Running pod must NOT be reaped, got %v", store.marked)
	}
	if len(pods.deletedTasks) != 0 {
		t.Fatalf("#461: no pod delete when the live read defers, got %v", pods.deletedTasks)
	}
	if pods.activeCalls != 1 {
		t.Fatalf("#461: cache-absent MUST consult the live read, got %d live calls", pods.activeCalls)
	}
}

// TestPodLostReaper_CacheActive_SkipsLiveList: a cache-present Pending/Running pod
// defers the reap without touching the apiserver — the whole point of PR-10. The
// live TaskPodPresence read must not be called.
func TestPodLostReaper_CacheActive_SkipsLiveList(t *testing.T) {
	past := time.Now().UTC().Add(-2 * time.Minute)
	store := &fakePodLostStore{candidates: []PodLostCandidate{
		{TaskInstanceID: "live", DagRunID: "run-a", TaskID: "work", TryNumber: 1, RunningSince: past},
	}}
	pods := &fakePodManager{active: map[string]bool{}}
	r := newPodLostReaper(store, reapTestLogger(), 60*time.Second, nil)
	r.pods = pods
	r.cache = &fakePresenceCache{active: map[string]bool{"run-a/work/1": true}}

	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.marked) != 0 {
		t.Fatalf("a cache-active pod must be deferred, got %v", store.marked)
	}
	if pods.activeCalls != 0 {
		t.Fatalf("a cache-active defer must skip the live LIST, got %d live calls", pods.activeCalls)
	}
}

// TestPodLostReaper_CacheAbsentAndLiveAbsent_Reaps: cache absent AND the live read
// confirms absent — the pod is genuinely gone, so the TI is failed pod_lost and
// its pod torn down, pinned to (run, task, try).
func TestPodLostReaper_CacheAbsentAndLiveAbsent_Reaps(t *testing.T) {
	past := time.Now().UTC().Add(-2 * time.Minute)
	store := &fakePodLostStore{candidates: []PodLostCandidate{
		{TaskInstanceID: "gone", DagRunID: "run-a", TaskID: "work", TryNumber: 2, RunningSince: past},
	}}
	pods := &fakePodManager{active: map[string]bool{}} // live also absent
	r := newPodLostReaper(store, reapTestLogger(), 60*time.Second, nil)
	r.pods = pods
	r.cache = &fakePresenceCache{active: map[string]bool{}}

	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.marked) != 1 || store.marked[0] != "gone" {
		t.Fatalf("cache-absent + live-absent must reap, got %v", store.marked)
	}
	if pods.activeCalls != 1 {
		t.Fatalf("the destructive path must confirm via the live read, got %d live calls", pods.activeCalls)
	}
	if len(pods.deletedTasks) != 1 || pods.deletedTasks[0] != (deletedTask{"run-a", "work", 2}) {
		t.Fatalf("teardown must be pinned to (run-a,work,try 2), got %v", pods.deletedTasks)
	}
}

// TestPodLostReaper_NilCache_UsesLivePathForAll: with no cache wired (Lite, or
// before the informer warms), every candidate goes straight to the live read —
// today's behavior, unchanged.
func TestPodLostReaper_NilCache_UsesLivePathForAll(t *testing.T) {
	past := time.Now().UTC().Add(-2 * time.Minute)
	store := &fakePodLostStore{candidates: []PodLostCandidate{
		{TaskInstanceID: "gone", DagRunID: "run-a", TaskID: "work", TryNumber: 1, RunningSince: past},
	}}
	pods := &fakePodManager{active: map[string]bool{}}
	r := newPodLostReaper(store, reapTestLogger(), 60*time.Second, nil)
	r.pods = pods
	// r.cache stays nil.

	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.marked) != 1 {
		t.Fatalf("nil cache must use the live path and reap a genuinely-absent pod, got %v", store.marked)
	}
	if pods.activeCalls != 1 {
		t.Fatalf("nil cache must consult the live read, got %d live calls", pods.activeCalls)
	}
}

// --- dispatch-lost reaper (#461 path) --------------------------------------

// TestDispatchLostReaper_CacheAbsentButLiveActive_DoesNotReap is the #461 lock on
// the queued path: cache says absent (lag), the live read says the pod is
// Pending/Running (slow image pull), so the reaper defers. Cache absence never
// authorizes failing a queued TI whose dispatch actually landed.
func TestDispatchLostReaper_CacheAbsentButLiveActive_DoesNotReap(t *testing.T) {
	past := time.Now().UTC().Add(-10 * time.Minute)
	store := &fakeStaleQueuedStore{candidates: []StaleQueuedCandidate{
		{TaskInstanceID: "slow", DagRunID: "run-a", TaskID: "work", TryNumber: 1, QueuedAt: past},
	}}
	pods := &fakePodManager{active: map[string]bool{"run-a/work/1": true}}
	r := newDispatchLostReaper(store, reapTestLogger(), 3*time.Minute, nil)
	r.pods = pods
	r.cache = &fakePresenceCache{active: map[string]bool{}} // cache absent

	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.failed) != 0 {
		t.Fatalf("#461: cache-absent must fall through to live; a live pod must NOT be reaped, got %v", store.failed)
	}
	if pods.activeCalls != 1 {
		t.Fatalf("#461: cache-absent MUST consult the live read, got %d live calls", pods.activeCalls)
	}
}

// TestDispatchLostReaper_CacheActive_SkipsLiveList: a cache-present pod defers the
// dispatch-lost decision without an apiserver read.
func TestDispatchLostReaper_CacheActive_SkipsLiveList(t *testing.T) {
	past := time.Now().UTC().Add(-10 * time.Minute)
	store := &fakeStaleQueuedStore{candidates: []StaleQueuedCandidate{
		{TaskInstanceID: "slow", DagRunID: "run-a", TaskID: "work", TryNumber: 1, QueuedAt: past},
	}}
	pods := &fakePodManager{active: map[string]bool{}}
	r := newDispatchLostReaper(store, reapTestLogger(), 3*time.Minute, nil)
	r.pods = pods
	r.cache = &fakePresenceCache{active: map[string]bool{"run-a/work/1": true}}

	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.failed) != 0 {
		t.Fatalf("a cache-active pod must defer, got %v", store.failed)
	}
	if pods.activeCalls != 0 {
		t.Fatalf("a cache-active defer must skip the live LIST, got %d live calls", pods.activeCalls)
	}
}

// TestDispatchLostReaper_CacheAbsentAndLiveAbsent_Reaps: cache absent AND live
// confirms absent — the dispatch is genuinely lost, so the TI is failed and any
// lingering pod for the attempt torn down.
func TestDispatchLostReaper_CacheAbsentAndLiveAbsent_Reaps(t *testing.T) {
	past := time.Now().UTC().Add(-10 * time.Minute)
	store := &fakeStaleQueuedStore{candidates: []StaleQueuedCandidate{
		{TaskInstanceID: "lost", DagRunID: "run-a", TaskID: "work", TryNumber: 4, QueuedAt: past},
	}}
	pods := &fakePodManager{active: map[string]bool{}}
	r := newDispatchLostReaper(store, reapTestLogger(), 3*time.Minute, nil)
	r.pods = pods
	r.cache = &fakePresenceCache{active: map[string]bool{}}

	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.failed) != 1 || store.failed[0] != "lost" {
		t.Fatalf("cache-absent + live-absent must reap, got %v", store.failed)
	}
	if pods.activeCalls != 1 {
		t.Fatalf("the destructive path must confirm via the live read, got %d live calls", pods.activeCalls)
	}
	if len(pods.deletedTasks) != 1 || pods.deletedTasks[0] != (deletedTask{"run-a", "work", 4}) {
		t.Fatalf("teardown must be pinned to (run-a,work,try 4), got %v", pods.deletedTasks)
	}
}

// TestDispatchLostReaper_NilCache_UsesLivePathForAll: with no cache wired, the
// reaper uses the live read for every candidate — unchanged behavior.
func TestDispatchLostReaper_NilCache_UsesLivePathForAll(t *testing.T) {
	past := time.Now().UTC().Add(-10 * time.Minute)
	store := &fakeStaleQueuedStore{candidates: []StaleQueuedCandidate{
		{TaskInstanceID: "lost", DagRunID: "run-a", TaskID: "work", TryNumber: 1, QueuedAt: past},
	}}
	pods := &fakePodManager{active: map[string]bool{}}
	r := newDispatchLostReaper(store, reapTestLogger(), 3*time.Minute, nil)
	r.pods = pods
	// r.cache stays nil.

	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.failed) != 1 {
		t.Fatalf("nil cache must use the live path and reap, got %v", store.failed)
	}
	if pods.activeCalls != 1 {
		t.Fatalf("nil cache must consult the live read, got %d live calls", pods.activeCalls)
	}
}
