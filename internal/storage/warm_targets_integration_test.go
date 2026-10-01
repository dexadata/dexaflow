//go:build integration

package storage_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
)

// registerWarmSpec registers a one-task DAG version carrying an author-declared
// min_idle_workers and a distinct image, so ActiveWarmTargets has a real
// dag_version to project.
func registerWarmSpec(t *testing.T, repo *storage.Repository, ctx context.Context, dagID, image string, minIdle int) {
	t.Helper()
	spec := domain.DAGSpec{
		SchemaVersion:  "1.0",
		DagID:          dagID,
		DagVersion:     "v1",
		Image:          image,
		MinIdleWorkers: minIdle,
		Tasks:          []domain.TaskSpec{{TaskID: "a", Type: domain.TaskTypePython}},
	}
	hash, err := spec.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	if created, rerr := repo.RegisterDagVersion(ctx, "default", spec, hash); rerr != nil || !created {
		t.Fatalf("register %s: created=%v err=%v", dagID, created, rerr)
	}
}

// TestActiveWarmTargetsIntegration exercises the real DB seam: an active dag_version
// with an author-declared min_idle_workers surfaces as one warm target whose
// effective count is resolved through the operator's clamp (min_idle 20, cap 8 => 8).
func TestActiveWarmTargetsIntegration(t *testing.T) {
	repo, sched, ctx := openRepo(t)
	sched.SetWarmExecution(config.ExecutionSection{WarmPoolsEnabled: true, MinIdleWorkers: 1, MaxPoolSize: 8})

	dagID := fmt.Sprintf("warm_tgt_%d", time.Now().UnixNano())
	image := fmt.Sprintf("warm-img-%d:v1", time.Now().UnixNano())
	registerWarmSpec(t, repo, ctx, dagID, image, 20) // author over-asks -> clamped to cap 8
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateQueued, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}

	targets, err := sched.ActiveWarmTargets(ctx)
	if err != nil {
		t.Fatalf("ActiveWarmTargets: %v", err)
	}
	var found bool
	for _, tgt := range targets {
		if tgt.Image != image {
			continue // another test's active DAG in the shared database
		}
		found = true
		if tgt.EffectiveMinIdle != 8 {
			t.Errorf("effective min_idle = %d, want 8 (author 20 clamped to cap 8)", tgt.EffectiveMinIdle)
		}
		if tgt.DagVersionID == "" {
			t.Error("warm target carries no dag_version_id")
		}
	}
	if !found {
		t.Fatalf("no warm target for image %s in %+v", image, targets)
	}

	// With warm pools off the same active version reports a zero target (the gate).
	sched.SetWarmExecution(config.ExecutionSection{WarmPoolsEnabled: false, MinIdleWorkers: 5, MaxPoolSize: 8})
	offTargets, err := sched.ActiveWarmTargets(ctx)
	if err != nil {
		t.Fatalf("ActiveWarmTargets (off): %v", err)
	}
	for _, tgt := range offTargets {
		if tgt.Image == image && tgt.EffectiveMinIdle != 0 {
			t.Errorf("warm pools off: effective min_idle = %d, want 0", tgt.EffectiveMinIdle)
		}
	}
}
