//go:build integration

// Package storage_test — the warm busy-window reliability guard (ADR 0058 N1d-b,
// review finding "Hole A").
//
// BindWarmAttempt stamps warm_worker_id the instant a warm worker ACKs an
// assignment as started, while the TI is still `queued`; the child reports
// RUNNING (queued->running) only a moment later. The old ListBusyWarmWorkerPods
// counted a worker busy ONLY when state='running', so between the ack and the
// RUNNING report the starting worker classified as IDLE — and if that tick had
// len(idle) > EffectiveMinIdle (min_idle lowered, a burst subsiding, a draining
// sibling) the excess-idle-tail delete removed a worker that was actively
// starting an attempt: an M1 violation that kills a just-started attempt.
//
// Two integration cases lock the fix against real Postgres, since both turn on a
// SQL predicate a fake-store unit test cannot prove:
//
//   - The full seam (real busy source + real reconciler): a warm pod bound to a
//     queued (not-yet-running) TI must be classified BUSY and NOT deleted even
//     when the idle target says to scale down. RED before the fix (the pod was
//     deleted — the M1 kill), green after.
//   - The widened busy query + the same-row re-dispatch clear:
//     ListBusyWarmWorkerPods returns a worker for a queued+bound TI, and does NOT
//     return one for a row that was bound then re-dispatched (RequeueForRedispatch
//     cleared warm_worker_id so a stale binding can't falsely mark the gone worker
//     busy).
//
// The shared-DB harness means these rows live alongside whatever else the suite
// seeded, so each test seeds its own uniquely-named DAG/pod and asserts only on
// rows and pod names it owns.
package storage_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/executor"
)

// fakeWarmTargetsPG is a canned executor.WarmTargetSource for the full-seam test.
type fakeWarmTargetsPG struct {
	targets []executor.WarmTarget
}

func (f *fakeWarmTargetsPG) ActiveWarmTargets(context.Context) ([]executor.WarmTarget, error) {
	return f.targets, nil
}

// fakeWarmPodsPG is a canned executor.WarmPodClient: it lists a fixed fleet and
// records every delete so the test can assert exactly which workers the
// reconciler removed.
type fakeWarmPodsPG struct {
	existing []executor.WarmPodInfo
	created  []executor.WarmTarget
	deleted  []string
}

func (f *fakeWarmPodsPG) ListWarmPods(context.Context) ([]executor.WarmPodInfo, error) {
	return f.existing, nil
}

func (f *fakeWarmPodsPG) CreateWarmPod(_ context.Context, t executor.WarmTarget, _, _ string) (string, error) {
	f.created = append(f.created, t)
	return "warm-" + t.DagVersionID, nil
}

func (f *fakeWarmPodsPG) DeleteWarmPod(_ context.Context, name string) error {
	f.deleted = append(f.deleted, name)
	return nil
}

// EnsureWarmAnchor / DeleteWarmAnchor satisfy the D11 additions to
// executor.WarmPodClient; this seam test asserts pod deletes, not anchors, so they
// are recording no-ops (EnsureWarmAnchor returns a deterministic UID).
func (f *fakeWarmPodsPG) EnsureWarmAnchor(_ context.Context, dagVersionID string) (string, error) {
	return "uid-" + dagVersionID, nil
}

func (f *fakeWarmPodsPG) DeleteWarmAnchor(_ context.Context, _ string) error {
	return nil
}

// TestWarmPoolReconcileKeepsQueuedBoundWorkerIntegration is the load-bearing
// busy-window regression (Hole A). It drives the REAL warm-pool reconciler with
// the REAL busy source (SchedulerStore.ListBusyWarmWorkerPods) over a TI that a
// warm worker has bound while still `queued` — the exact starting-but-not-yet-
// running window. The reconciler's target says to scale the idle buffer to 0
// (EffectiveMinIdle=0), so the ONLY thing keeping the pod alive is being seen as
// busy.
//
// Before the fix the busy query filtered state='running', so a queued+bound TI
// produced an EMPTY busy set: the reconciler saw one IDLE worker over a target of
// 0 and DELETED it — killing an attempt the worker was starting. After the fix
// the busy set spans queued+running, so the worker is BUSY and is left to finish.
func TestWarmPoolReconcileKeepsQueuedBoundWorkerIntegration(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)
	stamp := time.Now().UnixNano()

	// A warm worker acked an assignment as started: the TI is `queued` and bound
	// to this pod, but has not yet reported RUNNING.
	dagID := fmt.Sprintf("warm_busywin_%d", stamp)
	warmPod := fmt.Sprintf("leoflow-warm-busywin-%d", stamp)
	seedQueuedWarmTask(t, repo, sched, exec, ctx, dagID, "load", warmPod)

	// The fleet the reconciler sees is exactly this one starting worker, on a
	// version whose idle target is 0 (a scale-down / burst-subsiding tick). If the
	// worker is classified idle it is the excess-idle tail and gets deleted.
	dagVersion := fmt.Sprintf("dv-busywin-%d", stamp)
	pods := &fakeWarmPodsPG{existing: []executor.WarmPodInfo{{Name: warmPod, DagVersionID: dagVersion}}}
	targets := &fakeWarmTargetsPG{targets: []executor.WarmTarget{
		{DagVersionID: dagVersion, Image: "img", EffectiveMinIdle: 0, MaxPoolSize: 8},
	}}

	r := executor.NewWarmPoolReconciler(targets, pods, sched, 0, nil, nil)
	if err := r.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	for _, d := range pods.deleted {
		if d == warmPod {
			t.Fatalf("reconciler deleted %q — a warm worker STARTING a queued+bound attempt "+
				"must be classified busy and never deleted (M1 / Hole A)", warmPod)
		}
	}
}

// TestListBusyWarmWorkerPodsQueuedBoundIntegration locks the widened busy query
// and the same-row re-dispatch clear directly against Postgres:
//
//   - a queued+bound TI's worker IS in the busy set (the widened predicate), and
//   - a worker whose bound row was re-dispatched via RequeueForRedispatch is NOT
//     in the busy set (the clear worked — the re-dispatched row carries no stale
//     binding, so the gone worker is not falsely marked busy).
func TestListBusyWarmWorkerPodsQueuedBoundIntegration(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)
	stamp := time.Now().UnixNano()

	// (1) queued + bound — its worker MUST be in the busy set.
	liveDag := fmt.Sprintf("busy_queued_live_%d", stamp)
	livePod := fmt.Sprintf("leoflow-warm-bqlive-%d", stamp)
	seedQueuedWarmTask(t, repo, sched, exec, ctx, liveDag, "load", livePod)

	// (2) bound-then-re-dispatched — its worker must NOT be in the busy set. Bind
	// while queued, then RequeueForRedispatch moves the SAME row queued->scheduled
	// and (with the fix) clears warm_worker_id.
	reDag := fmt.Sprintf("busy_queued_redispatch_%d", stamp)
	rePod := fmt.Sprintf("leoflow-warm-bqredis-%d", stamp)
	reRun := seedQueuedWarmTask(t, repo, sched, exec, ctx, reDag, "load", rePod)
	if err := exec.RequeueForRedispatch(ctx, reRun, "load", 1, 0); err != nil {
		t.Fatalf("RequeueForRedispatch: %v", err)
	}

	busy, err := sched.ListBusyWarmWorkerPods(ctx)
	if err != nil {
		t.Fatalf("ListBusyWarmWorkerPods: %v", err)
	}

	if !busy[livePod] {
		t.Errorf("busy set is missing %q — a queued+bound worker (starting an attempt) "+
			"must be counted busy so the reconciler never deletes it", livePod)
	}
	if busy[rePod] {
		t.Errorf("busy set contains %q — a re-dispatched row must clear warm_worker_id, "+
			"so the gone worker is not falsely marked busy (stale-binding vector)", rePod)
	}
}
