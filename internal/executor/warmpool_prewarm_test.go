package executor

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

// TestWarmPoolLogsPrewarmStartOnce: a Prewarm target (a DAG's current version
// with no active run, ADR 0082) is created like any other, and the reconciler
// logs and meters the prewarm start once, on the tick the version first
// appears, not on every tick that keeps it.
func TestWarmPoolLogsPrewarmStartOnce(t *testing.T) {
	var logs bytes.Buffer
	rec := &capturingRecorder{}
	targets := &fakeWarmTargets{targets: []WarmTarget{
		{DagVersionID: "dv2", Image: "img:v2", EffectiveMinIdle: 2, MaxPoolSize: 4, Prewarm: true},
		{DagVersionID: "dvRun", Image: "img:r", EffectiveMinIdle: 1, MaxPoolSize: 4},
	}}
	pods := &fakeWarmPods{}
	r := NewWarmPoolReconciler(targets, pods, busySet(), 0, slog.New(slog.NewTextHandler(&logs, nil)), rec)

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	created := 0
	for _, c := range pods.created {
		if c.DagVersionID == "dv2" {
			created++
		}
	}
	if created != 2 {
		t.Errorf("created %d workers for the prewarm target, want 2 on the first tick", created)
	}
	for i := 0; i < 2; i++ {
		if err := r.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if got := rec.count("warm_pool_prewarm_started"); got != 1 {
		t.Errorf("warm_pool_prewarm_started = %d, want 1 (one prewarm target, logged on its first tick)", got)
	}
	if n := strings.Count(logs.String(), "prewarming"); n != 1 {
		t.Errorf("prewarm log lines = %d, want 1:\n%s", n, logs.String())
	}
	if !strings.Contains(logs.String(), "dag_version=dv2") {
		t.Errorf("prewarm log does not name dv2:\n%s", logs.String())
	}
}

// TestWarmPoolLogsDrainStartOnceAndKeepsBusy: when the current version
// changes, the previous version drops out of the targets. The reconciler
// logs and meters the drain once, deletes its idle worker at once, keeps the
// busy one until its attempt ends, then deletes it and the anchor.
func TestWarmPoolLogsDrainStartOnceAndKeepsBusy(t *testing.T) {
	var logs bytes.Buffer
	rec := &capturingRecorder{}
	targets := &fakeWarmTargets{targets: []WarmTarget{
		{DagVersionID: "dv2", Image: "img:v2", EffectiveMinIdle: 1, MaxPoolSize: 4, Prewarm: true},
	}}
	pods := &fakeWarmPods{existing: []WarmPodInfo{
		{Name: "w-v1-idle", DagVersionID: "dv1"},
		{Name: "w-v1-busy", DagVersionID: "dv1"},
		{Name: "w-v2", DagVersionID: "dv2"},
	}}
	busy := busySet("w-v1-busy")
	r := NewWarmPoolReconciler(targets, pods, busy, 0, slog.New(slog.NewTextHandler(&logs, nil)), rec)

	ctx := context.Background()
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if len(pods.deleted) != 1 || pods.deleted[0] != "w-v1-idle" {
		t.Fatalf("first tick deleted %v, want only the idle worker of the previous version", pods.deleted)
	}
	// The busy worker is still serving: a second tick keeps it and does not
	// log the drain again.
	pods.existing = []WarmPodInfo{{Name: "w-v1-busy", DagVersionID: "dv1"}, {Name: "w-v2", DagVersionID: "dv2"}}
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if len(pods.deleted) != 1 {
		t.Fatalf("second tick deleted %v while the attempt still runs, want nothing more", pods.deleted)
	}
	if got := rec.count("warm_pool_drain_started"); got != 1 {
		t.Errorf("warm_pool_drain_started = %d, want 1", got)
	}
	if n := strings.Count(logs.String(), "draining the warm pool"); n != 1 {
		t.Errorf("drain log lines = %d, want 1:\n%s", n, logs.String())
	}
	// The attempt ended: the worker is idle, deleted, and the pool is drained.
	busy.busy = map[string]bool{}
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if len(pods.deleted) != 2 || pods.deleted[1] != "w-v1-busy" {
		t.Fatalf("after the attempt ended deleted %v, want the formerly busy worker too", pods.deleted)
	}
	pods.existing = []WarmPodInfo{{Name: "w-v2", DagVersionID: "dv2"}}
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if len(pods.deletedAnchors) != 1 || pods.deletedAnchors[0] != "dv1" {
		t.Errorf("anchors deleted %v, want dv1 once the version has no pod", pods.deletedAnchors)
	}
	if !strings.Contains(logs.String(), "warm pool drained") {
		t.Errorf("no drained log line:\n%s", logs.String())
	}
}
