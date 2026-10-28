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
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// registerWarmVersion registers version of a one-task DAG on image, declaring
// minIdle min_idle_workers (0: none).
func registerWarmVersion(t *testing.T, repo *storage.Repository, ctx context.Context, dagID, version, image string, minIdle int) {
	t.Helper()
	spec := domain.DAGSpec{
		SchemaVersion: "1.0", DagID: dagID, DagVersion: version, Image: image, MinIdleWorkers: minIdle,
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
// rollback (#1707, ADR 0082): the current version of an unpaused DAG that
// declares min_idle_workers is a target before any run; a register makes the new version the target and drops the
// previous one, whose idle worker is deleted at once while the worker bound
// to its in-flight attempt is kept until that attempt ends; a rollback makes
// the previous version the target again.
func TestPrewarmFollowsTheCurrentVersionIntegration(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)
	sched.SetWarmExecution(config.ExecutionSection{WarmPoolsEnabled: true, MinIdleWorkers: 1, MaxPoolSize: 4}, nil, domain.WarmPolicy{})
	stamp := time.Now().UnixNano()
	dagID := fmt.Sprintf("warm_prewarm_%d", stamp)
	img1, img2 := fmt.Sprintf("prewarm-%d:v1", stamp), fmt.Sprintf("prewarm-%d:v2", stamp)

	registerWarmVersion(t, repo, ctx, dagID, "v1", img1, 1)
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
	if !ok || !v1.Prewarm || v1.EffectiveMinIdle != 1 || v1.DagID != dagID {
		t.Fatalf("unpaused DAG, no run: target = %+v (present %v), want a Prewarm target of 1 for %s", v1, ok, dagID)
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
	registerWarmVersion(t, repo, ctx, dagID, "v2", img2, 1)
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

// TestPrewarmNeedsADeclaredMinIdleIntegration: with the operator floor set,
// the current version of an unpaused DAG that declares no min_idle_workers is
// neither a row of ListWarmCurrentVersions marked prewarm nor a target (ADR
// 0082 D3), while one that declares it is both. The filter runs in SQL, so
// the undeclared DAG's spec is never loaded.
func TestPrewarmNeedsADeclaredMinIdleIntegration(t *testing.T) {
	pg, repo, sched, _, ctx := openExecPG(t)
	sched.SetWarmExecution(config.ExecutionSection{WarmPoolsEnabled: true, MinIdleWorkers: 2, MaxPoolSize: 4}, nil, domain.WarmPolicy{})
	stamp := time.Now().UnixNano()
	plainDag, plainImg := fmt.Sprintf("warm_plain_%d", stamp), fmt.Sprintf("plain-%d:v1", stamp)
	declDag, declImg := fmt.Sprintf("warm_decl_%d", stamp), fmt.Sprintf("decl-%d:v1", stamp)
	registerWarmVersion(t, repo, ctx, plainDag, "v1", plainImg, 0)
	registerWarmVersion(t, repo, ctx, declDag, "v1", declImg, 3)
	for _, d := range []string{plainDag, declDag} {
		if _, err := repo.SetPaused(ctx, "default", d, false); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := pg.Queries.ListWarmCurrentVersions(ctx, queries.ListWarmCurrentVersionsParams{Prewarm: true})
	if err != nil {
		t.Fatal(err)
	}
	prewarm := map[string]bool{}
	for _, r := range rows {
		prewarm[r.DagID] = r.Prewarm
	}
	if _, ok := prewarm[plainDag]; ok {
		t.Errorf("DAG %s with no declared min_idle_workers is a ListWarmCurrentVersions row", plainDag)
	}
	if !prewarm[declDag] {
		t.Errorf("DAG %s declaring min_idle_workers is not a prewarm row", declDag)
	}
	if got, ok := targetByImage(t, sched, ctx, plainImg); ok {
		t.Errorf("undeclared DAG under the operator floor has a target %+v, want none", got)
	}
	if got, ok := targetByImage(t, sched, ctx, declImg); !ok || !got.Prewarm || got.EffectiveMinIdle != 3 {
		t.Errorf("declared DAG: target = %+v (present %v), want a Prewarm target of 3", got, ok)
	}
}

// TestPausedDagRunningRunKeepsItsTargetIntegration: a paused DAG is not
// prewarmed, but a running run on its current version keeps that version's
// target (from the run, not prewarm), as before ADR 0082.
func TestPausedDagRunningRunKeepsItsTargetIntegration(t *testing.T) {
	repo, sched, _, ctx := openExec(t)
	sched.SetWarmExecution(config.ExecutionSection{WarmPoolsEnabled: true, MinIdleWorkers: 1, MaxPoolSize: 4}, nil, domain.WarmPolicy{})
	stamp := time.Now().UnixNano()
	dagID, img := fmt.Sprintf("warm_paused_run_%d", stamp), fmt.Sprintf("paused-run-%d:v1", stamp)
	registerWarmVersion(t, repo, ctx, dagID, "v1", img, 1)
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SetPaused(ctx, "default", dagID, true); err != nil {
		t.Fatal(err)
	}
	got, ok := targetByImage(t, sched, ctx, img)
	if !ok || got.Prewarm || got.EffectiveMinIdle != 1 {
		t.Fatalf("paused DAG with a running run: target = %+v (present %v), want a run target of 1", got, ok)
	}
}
