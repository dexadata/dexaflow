//go:build integration

package storage_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
	"github.com/dexadata/dexaflow/internal/storage"
)

// registerWarmVersion registers version of a one-task DAG on image.
func registerWarmVersion(t *testing.T, repo *storage.Repository, ctx context.Context, dagID, version, image string) {
	t.Helper()
	spec := domain.DAGSpec{
		SchemaVersion: "1.0", DagID: dagID, DagVersion: version, Image: image,
		Tasks: []domain.TaskSpec{{TaskID: "load", Type: domain.TaskTypePython}},
	}
	hash, err := spec.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	if created, err := repo.RegisterDagVersion(ctx, "default", spec, hash); err != nil || !created {
		t.Fatalf("register %s %s: created=%v err=%v", dagID, version, created, err)
	}
}

// targetByImage is the warm target for image, if any.
func targetByImage(t *testing.T, sched *storage.SchedulerStore, ctx context.Context, image string) (executor.WarmTarget, bool) {
	t.Helper()
	targets, err := sched.ActiveWarmTargets(ctx)
	if err != nil {
		t.Fatalf("ActiveWarmTargets: %v", err)
	}
	for _, tgt := range targets {
		if tgt.Image == image {
			return tgt, true
		}
	}
	return executor.WarmTarget{}, false
}

// ownTargets is the real SchedulerStore as the reconciler's target source,
// narrowed to the images this test owns, since the shared database holds
// other tests' unpaused DAGs whose current versions are prewarm targets too.
type ownTargets struct {
	src    *storage.SchedulerStore
	images map[string]bool
}

func (o ownTargets) ActiveWarmTargets(ctx context.Context) ([]executor.WarmTarget, error) {
	all, err := o.src.ActiveWarmTargets(ctx)
	if err != nil {
		return nil, err
	}
	var out []executor.WarmTarget
	for _, tgt := range all {
		if o.images[tgt.Image] {
			out = append(out, tgt)
		}
	}
	return out, nil
}

// TestPrewarmFollowsTheCurrentVersionIntegration drives the warm-pool
// reconciler over the real target and busy sources through a deploy and a
// rollback (#1707, ADR 0082): an unpaused DAG's current version is a target
// before any run; a register makes the new version the target and drops the
// previous one, whose idle worker is deleted at once while the worker bound
// to its in-flight attempt is kept until that attempt ends; a rollback makes
// the previous version the target again.
func TestPrewarmFollowsTheCurrentVersionIntegration(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)
	sched.SetWarmExecution(config.ExecutionSection{WarmPoolsEnabled: true, MinIdleWorkers: 1, MaxPoolSize: 4}, nil)
	stamp := time.Now().UnixNano()
	dagID := fmt.Sprintf("warm_prewarm_%d", stamp)
	img1, img2 := fmt.Sprintf("prewarm-%d:v1", stamp), fmt.Sprintf("prewarm-%d:v2", stamp)

	registerWarmVersion(t, repo, ctx, dagID, "v1", img1)
	if _, err := repo.SetPaused(ctx, "default", dagID, true); err != nil {
		t.Fatal(err)
	}
	if _, ok := targetByImage(t, sched, ctx, img1); ok {
		t.Fatal("a paused DAG with no run has a warm target; it must not be prewarmed")
	}
	if _, err := repo.SetPaused(ctx, "default", dagID, false); err != nil {
		t.Fatal(err)
	}
	v1, ok := targetByImage(t, sched, ctx, img1)
	if !ok || !v1.Prewarm || v1.EffectiveMinIdle != 1 {
		t.Fatalf("unpaused DAG, no run: target = %+v (present %v), want a Prewarm target of 1", v1, ok)
	}

	// A run of v1 starts an attempt on a warm worker of v1.
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runUUID := resolveRunUUID(t, sched, ctx, dagID)
	tasks := []domain.TaskSpec{{TaskID: "load", Type: domain.TaskTypePython}}
	if err := sched.MaterializeTasks(ctx, runUUID, tasks); err != nil {
		t.Fatal(err)
	}
	if err := sched.ApplyTransition(ctx, runUUID, "load", domain.TaskStateQueued); err != nil {
		t.Fatal(err)
	}
	busyPod, idlePod := fmt.Sprintf("warm-busy-%d", stamp), fmt.Sprintf("warm-idle-%d", stamp)
	if err := exec.BindWarmAttempt(ctx, runUUID, "load", 1, 0, busyPod); err != nil {
		t.Fatal(err)
	}
	if err := sched.ApplyTransition(ctx, runUUID, "load", domain.TaskStateRunning); err != nil {
		t.Fatal(err)
	}
	if got, present := targetByImage(t, sched, ctx, img1); !present || got.Prewarm {
		t.Fatalf("v1 with an active run: target = %+v (present %v), want a run target", got, present)
	}

	// Deploy v2 while the attempt runs.
	registerWarmVersion(t, repo, ctx, dagID, "v2", img2)
	if got, present := targetByImage(t, sched, ctx, img1); present {
		t.Fatalf("superseded v1 is still a target %+v; its pool must drain", got)
	}
	v2, ok := targetByImage(t, sched, ctx, img2)
	if !ok || !v2.Prewarm || v2.EffectiveMinIdle != 1 {
		t.Fatalf("v2 after the register: target = %+v (present %v), want a Prewarm target of 1", v2, ok)
	}

	pods := &fakeWarmPodsPG{existing: []executor.WarmPodInfo{
		{Name: busyPod, DagVersionID: v1.DagVersionID},
		{Name: idlePod, DagVersionID: v1.DagVersionID},
	}}
	r := executor.NewWarmPoolReconciler(ownTargets{sched, map[string]bool{img1: true, img2: true}}, pods, sched, 0, nil, nil)
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if len(pods.deleted) != 1 || pods.deleted[0] != idlePod {
		t.Fatalf("after the register deleted %v, want only the idle v1 worker %s", pods.deleted, idlePod)
	}
	if len(pods.created) != 1 || pods.created[0].DagVersionID != v2.DagVersionID {
		t.Fatalf("after the register created %+v, want one v2 worker", pods.created)
	}

	// The attempt ends: its worker is no longer busy and goes too.
	if err := sched.ApplyTransition(ctx, runUUID, "load", domain.TaskStateSuccess); err != nil {
		t.Fatal(err)
	}
	pods.existing = []executor.WarmPodInfo{
		{Name: busyPod, DagVersionID: v1.DagVersionID},
		{Name: "warm-v2", DagVersionID: v2.DagVersionID},
	}
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if len(pods.deleted) != 2 || pods.deleted[1] != busyPod {
		t.Fatalf("after the attempt ended deleted %v, want the v1 worker %s too", pods.deleted, busyPod)
	}

	// Roll back to v1: it is the target again, v2 drains.
	if _, err := repo.SetCurrentDagVersion(ctx, "default", dagID, domain.DagVersionSelector{Version: "v1"}, "", nil); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if _, ok := targetByImage(t, sched, ctx, img1); !ok {
		t.Error("v1 after the rollback is not a target")
	}
	if got, ok := targetByImage(t, sched, ctx, img2); ok {
		t.Errorf("v2 after the rollback is still a target %+v", got)
	}
}
