package executor

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestIsDispatchLost: the pure decision returns true iff the gap between the
// candidate's queued_at and now reaches the threshold. A zero QueuedAt is
// treated as alive — defensive: a TI without a queued_at stamp is too poorly
// observed to reap. Future timestamps (clock skew) are alive.
//
// Mirrors IsAgentLost's "do no harm" shape (ADR 0031): the reaper requires a
// positive observable signal (queued_at older than threshold) before failing
// the TI as `dispatch_lost`.
func TestIsDispatchLost(t *testing.T) {
	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	const threshold = 3 * time.Minute

	tests := []struct {
		name string
		qed  time.Time
		want bool
	}{
		{"fresh queue is alive", now.Add(-1 * time.Second), false},
		{"under threshold is alive", now.Add(-2 * time.Minute), false},
		{"exactly at threshold is lost", now.Add(-3 * time.Minute), true},
		{"well past threshold is lost", now.Add(-30 * time.Minute), true},
		{"future queued (clock skew) is alive", now.Add(1 * time.Second), false},
		{"zero queued_at is alive (out of scope)", time.Time{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsDispatchLost(StaleQueuedCandidate{QueuedAt: tc.qed}, threshold, now)
			if got != tc.want {
				t.Errorf("IsDispatchLost(queued=%v) = %v, want %v", tc.qed, got, tc.want)
			}
		})
	}
}

// fakeStaleQueuedStore is the minimal store the reaper needs in unit tests.
type fakeStaleQueuedStore struct {
	candidates []StaleQueuedCandidate
	listErr    error
	failed     []string
	failErr    error
}

func (f *fakeStaleQueuedStore) ListStaleQueuedCandidates(context.Context) ([]StaleQueuedCandidate, error) {
	return f.candidates, f.listErr
}

func (f *fakeStaleQueuedStore) MarkTaskDispatchLost(_ context.Context, tiID string, _, _ int) error {
	if f.failErr != nil {
		return f.failErr
	}
	f.failed = append(f.failed, tiID)
	return nil
}

// TestReapDispatchLost_MarksStaleTIs covers the success path: only TIs queued
// past the threshold get failed; freshly-queued ones are left alone. This is
// the contract that makes "stuck dag_run because a scheduler crashed
// mid-dispatch" recoverable (issue #202): once these TIs are failed, the
// no-active-TI guard on the orphan-run reaper passes and the run gets cleaned
// up on the next tick (two-step recovery via two backstops).
func TestReapDispatchLost_MarksStaleTIs(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeStaleQueuedStore{candidates: []StaleQueuedCandidate{
		{TaskInstanceID: "fresh-ti", QueuedAt: now.Add(-10 * time.Second)},
		{TaskInstanceID: "stuck-ti", QueuedAt: now.Add(-10 * time.Minute)},
	}}
	rec := &capturingRecorder{}
	r := newDispatchLostReaper(store, reapTestLogger(), 3*time.Minute, rec)

	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run err = %v", err)
	}
	if len(store.failed) != 1 || store.failed[0] != "stuck-ti" {
		t.Errorf("failed = %v, want [stuck-ti]", store.failed)
	}
	if got := rec.count("dispatch_lost"); got != 1 {
		t.Errorf("dispatch_lost decisions = %d, want 1", got)
	}
}

// TestReapDispatchLost_ListErrorSurfaces: a list failure is returned so the
// caller can log it. Must never panic.
func TestReapDispatchLost_ListErrorSurfaces(t *testing.T) {
	r := newDispatchLostReaper(&fakeStaleQueuedStore{listErr: errors.New("db down")},
		reapTestLogger(), 3*time.Minute, nil)
	if err := r.run(context.Background()); err == nil {
		t.Error("expected list error to be returned")
	}
}

// TestReapDispatchLost_PerTIErrorIsolated: a failure on one TI does not stall
// the rest — same isolation pattern as the agent-lost and orphan reapers.
func TestReapDispatchLost_PerTIErrorIsolated(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeStaleQueuedStore{
		candidates: []StaleQueuedCandidate{
			{TaskInstanceID: "a", QueuedAt: now.Add(-10 * time.Minute)},
			{TaskInstanceID: "b", QueuedAt: now.Add(-10 * time.Minute)},
		},
		failErr: errors.New("write failed"),
	}
	r := newDispatchLostReaper(store, reapTestLogger(), 3*time.Minute, nil)
	if err := r.run(context.Background()); err != nil {
		t.Errorf("run err = %v, want nil (per-TI errors isolated)", err)
	}
}

// TestReapDispatchLost_H3_WarmLiveDefers is the double-run regression (ADR 0058
// N1d-a2, review finding H3). A warm attempt can sit in `queued` past the
// dispatch threshold while its serving warm worker is alive and merely slow to
// transition queued->running. A warm attempt has NO task pod, so the existing
// TaskPodPresence gate cannot protect it — the pure time threshold would fail it,
// and once the live worker DOES transition the attempt, the task runs twice.
//
// The fix: if the candidate's WarmWorkerID is in the live warm-pod set, DEFER.
// This test pins that. Remove the defer (the pre-fix behavior) and the reaper
// marks the TI dispatch-lost → the double-run.
func TestReapDispatchLost_H3_WarmLiveDefers(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeStaleQueuedStore{candidates: []StaleQueuedCandidate{
		{TaskInstanceID: "warm-slow", DagRunID: "run-1", TaskID: "load", QueuedAt: now.Add(-10 * time.Minute), WarmWorkerID: "pod-live"},
	}}
	lister := &fakeWarmLister{pods: []WarmPodInfo{{Name: "pod-live", Terminal: false}}}
	rec := &capturingRecorder{}
	r := newDispatchLostReaper(store, reapTestLogger(), 3*time.Minute, rec)
	r.warmPods = lister

	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.failed) != 0 {
		t.Fatalf("a warm attempt whose worker is LIVE must be deferred, not failed — the double-run bug; got %v", store.failed)
	}
	if got := rec.count("dispatch_lost_warm_deferred"); got != 1 {
		t.Errorf("dispatch_lost_warm_deferred meter = %d, want 1", got)
	}
}

// TestReapDispatchLost_H3_WarmGoneReaped: the other side of the guard — a warm
// attempt whose bound worker is NOT in the live set (the worker really died)
// still gets reaped. The defer protects only LIVE workers; it must not turn the
// reaper into a no-op for genuinely lost warm dispatches.
func TestReapDispatchLost_H3_WarmGoneReaped(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeStaleQueuedStore{candidates: []StaleQueuedCandidate{
		{TaskInstanceID: "warm-gone", DagRunID: "run-1", TaskID: "load", QueuedAt: now.Add(-10 * time.Minute), WarmWorkerID: "pod-dead"},
	}}
	lister := &fakeWarmLister{pods: []WarmPodInfo{{Name: "pod-other", Terminal: false}}}
	r := newDispatchLostReaper(store, reapTestLogger(), 3*time.Minute, nil)
	r.warmPods = lister

	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.failed) != 1 || store.failed[0] != "warm-gone" {
		t.Fatalf("a warm attempt whose worker is GONE must still be reaped, got %v", store.failed)
	}
}

// TestReapDispatchLost_H3_DedicatedUnchanged: an attempt with empty WarmWorkerID
// (a dedicated, non-warm task) is untouched by the warm defer — it flows through
// the existing pod-liveness path. With no pods wired it falls back to the pure
// time threshold and is reaped, exactly as before N1d-a2. This proves warm-off /
// dedicated behavior is byte-for-byte unchanged.
func TestReapDispatchLost_H3_DedicatedUnchanged(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeStaleQueuedStore{candidates: []StaleQueuedCandidate{
		{TaskInstanceID: "dedicated", DagRunID: "run-1", TaskID: "load", QueuedAt: now.Add(-10 * time.Minute), WarmWorkerID: ""},
	}}
	// A live warm fleet is present, but this candidate is not warm-bound, so the
	// warm defer must not fire regardless.
	lister := &fakeWarmLister{pods: []WarmPodInfo{{Name: "pod-live"}}}
	r := newDispatchLostReaper(store, reapTestLogger(), 3*time.Minute, nil)
	r.warmPods = lister

	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.failed) != 1 || store.failed[0] != "dedicated" {
		t.Fatalf("a dedicated (unbound) TI must be reaped by the existing path, got %v", store.failed)
	}
}
