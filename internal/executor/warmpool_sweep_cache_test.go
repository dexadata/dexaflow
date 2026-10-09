package executor

import "testing"

// TestOrphanSweepOnCachedFleetConfirmsLive: an inactive dag_version still has a
// BUSY warm pod the synced cache does not show (a hung watch, a missed event).
// The orphan anchor sweep must not trust the cached fleet: deleting the anchor
// cascades (ownerReference) to the busy pod and kills its running attempt. It
// confirms with a live LIST first, as the drain's anchor delete does.
func TestOrphanSweepOnCachedFleetConfirmsLive(t *testing.T) {
	pods := &fakeWarmPods{
		existing: warmPods("dv-old", "busy1"), // live: the busy pod exists
		anchors:  []string{"dv-old"},
	}
	r := NewWarmPoolReconciler(&fakeWarmTargets{}, pods, busySet("busy1"), 0, nil, nil)
	r.SetCache(&fakeWarmCache{synced: true}, 4) // stale: shows no pod
	reconcileN(t, r, 1)
	if anchorDeleted(pods, "dv-old") {
		t.Fatalf("orphan sweep deleted dv-old's anchor while busy1 still references it; deletedAnchors=%v", pods.deletedAnchors)
	}
}

// TestOrphanSweepOnCachedFleetDeletesConfirmedOrphan: with the cached fleet and
// a live LIST that agrees no pod of the inactive version remains, the anchor is
// deleted.
func TestOrphanSweepOnCachedFleetDeletesConfirmedOrphan(t *testing.T) {
	pods := &fakeWarmPods{anchors: []string{"dv-old"}}
	r := NewWarmPoolReconciler(&fakeWarmTargets{}, pods, busySet(), 0, nil, nil)
	r.SetCache(&fakeWarmCache{synced: true}, 4)
	reconcileN(t, r, 1)
	if !anchorDeleted(pods, "dv-old") {
		t.Fatalf("deletedAnchors = %v, want dv-old (inactive, no pod live)", pods.deletedAnchors)
	}
}

// TestDrainedAnchorIsDeletedOnceThePodIsGone: on the event-refill path the drain
// deletes the last idle pod of an inactive version, but the live confirm right
// after still lists it (Terminating), so the anchor is kept that tick. Once the
// pod is gone from the cache and the live LIST, the orphan sweep deletes the
// anchor: it does not leak.
func TestDrainedAnchorIsDeletedOnceThePodIsGone(t *testing.T) {
	pods := &countingWarmPods{fakeWarmPods: &fakeWarmPods{existing: warmPods("dv-old", "w1"), anchors: []string{"dv-old"}}}
	r := NewWarmPoolReconciler(&fakeWarmTargets{}, pods, busySet(), 0, nil, nil)
	cache := &fakeWarmCache{synced: true, pods: warmPods("dv-old", "w1")}
	r.SetCache(cache, 4)
	reconcileN(t, r, 1) // w1 deleted, still listed (Terminating): anchor kept
	if anchorDeleted(pods.fakeWarmPods, "dv-old") {
		t.Fatal("anchor deleted while its pod is still listed")
	}
	pods.existing, cache.pods = nil, nil // grace period over
	reconcileN(t, r, 1)
	if !anchorDeleted(pods.fakeWarmPods, "dv-old") {
		t.Fatalf("deletedAnchors = %v, want dv-old once its last pod is gone (anchor leaked)", pods.deletedAnchors)
	}
}
