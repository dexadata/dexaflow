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
	registerWarmTasks(t, repo, ctx, dagID, image, minIdle, []domain.TaskSpec{{TaskID: "a", Type: domain.TaskTypePython}})
}

// registerWarmTasks registers a DAG version like registerWarmSpec, with the
// given tasks.
func registerWarmTasks(t *testing.T, repo *storage.Repository, ctx context.Context, dagID, image string, minIdle int, tasks []domain.TaskSpec) {
	t.Helper()
	spec := domain.DAGSpec{
		SchemaVersion:  "1.0",
		DagID:          dagID,
		DagVersion:     "v1",
		Image:          image,
		MinIdleWorkers: minIdle,
		Tasks:          tasks,
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
	sched.SetWarmExecution(config.ExecutionSection{WarmPoolsEnabled: true, MinIdleWorkers: 1, MaxPoolSize: 8}, nil, domain.WarmPolicy{})

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
	sched.SetWarmExecution(config.ExecutionSection{WarmPoolsEnabled: false, MinIdleWorkers: 5, MaxPoolSize: 8}, nil, domain.WarmPolicy{})
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

// TestActiveWarmTargetsSkipsDisallowedImageIntegration: a DAG registered on an
// image before the operator set the image allowlist (issue #537 C2) gets an
// active run, yet surfaces no warm target, so no warm pod starts on its image.
func TestActiveWarmTargetsSkipsDisallowedImageIntegration(t *testing.T) {
	repo, sched, ctx := openRepo(t)
	policy, err := domain.ParseExecutionPolicy(domain.ExecutionPolicyConfig{Images: []string{"registry.example.com/team-a/*"}})
	if err != nil {
		t.Fatal(err)
	}
	sched.SetWarmExecution(config.ExecutionSection{WarmPoolsEnabled: true, MinIdleWorkers: 1, MaxPoolSize: 8}, policy, domain.WarmPolicy{})

	suffix := time.Now().UnixNano()
	rogueDag, rogueImage := fmt.Sprintf("warm_rogue_%d", suffix), fmt.Sprintf("ghcr.io/someone/tool-%d:latest", suffix)
	okDag, okImage := fmt.Sprintf("warm_ok_%d", suffix), fmt.Sprintf("registry.example.com/team-a/etl-%d:v1", suffix)
	registerWarmSpec(t, repo, ctx, rogueDag, rogueImage, 2)
	registerWarmSpec(t, repo, ctx, okDag, okImage, 2)
	for _, dagID := range []string{rogueDag, okDag} {
		if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
			RunID: "r1", State: domain.DagRunStateQueued, RunType: "manual", LogicalDate: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("create run for %s: %v", dagID, err)
		}
	}

	targets, err := sched.ActiveWarmTargets(ctx)
	if err != nil {
		t.Fatalf("ActiveWarmTargets: %v", err)
	}
	var sawOK bool
	for _, tgt := range targets {
		switch tgt.Image {
		case rogueImage:
			t.Errorf("warm target for disallowed image %s: %+v", rogueImage, tgt)
		case okImage:
			sawOK = true
		}
	}
	if !sawOK {
		t.Errorf("no warm target for allowed image %s in %+v", okImage, targets)
	}
}

// TestActiveWarmTargetsSkipsVersionWithNoWarmTaskIntegration (#2065): with
// executor.unit set a warm pod is one unit, so a DAG whose tasks all declare
// resources, or are all of size 2, has an active run yet surfaces no warm
// target, while a DAG with one size-1 task without resources keeps its pool.
func TestActiveWarmTargetsSkipsVersionWithNoWarmTaskIntegration(t *testing.T) {
	repo, sched, ctx := openRepo(t)
	unit, err := domain.ParseResourceUnit(domain.ResourceUnitConfig{CPU: "250m", Memory: "512Mi"})
	if err != nil {
		t.Fatal(err)
	}
	sched.SetWarmExecution(config.ExecutionSection{WarmPoolsEnabled: true, MinIdleWorkers: 1, MaxPoolSize: 8}, nil,
		domain.WarmPolicy{Enabled: true, Unit: unit})

	suffix := time.Now().UnixNano()
	sized := &domain.Resources{Requests: &domain.ResourceQuantity{CPU: "100m"}}
	dags := map[string][]domain.TaskSpec{
		"sized": {{TaskID: "a", Type: domain.TaskTypePython, Resources: sized}, {TaskID: "b", Type: domain.TaskTypeBash, Resources: sized}},
		"wide":  {{TaskID: "a", Type: domain.TaskTypePython, PoolSlots: 2}},
		"mixed": {{TaskID: "a", Type: domain.TaskTypePython, PoolSlots: 2}, {TaskID: "b", Type: domain.TaskTypeBash}},
	}
	images := make(map[string]string, len(dags))
	for name, tasks := range dags {
		dagID, image := fmt.Sprintf("warm_%s_%d", name, suffix), fmt.Sprintf("warm-%s-%d:v1", name, suffix)
		images[image] = name
		registerWarmTasks(t, repo, ctx, dagID, image, 2, tasks)
		if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
			RunID: "r1", State: domain.DagRunStateQueued, RunType: "manual", LogicalDate: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("create run for %s: %v", dagID, err)
		}
	}

	targets, err := sched.ActiveWarmTargets(ctx)
	if err != nil {
		t.Fatalf("ActiveWarmTargets: %v", err)
	}
	got := map[string]bool{}
	for _, tgt := range targets {
		if name, ok := images[tgt.Image]; ok {
			got[name] = true
		}
	}
	if got["sized"] || got["wide"] || !got["mixed"] {
		t.Errorf("warm targets for %v, want only mixed (the only DAG with a warm-eligible task)", got)
	}
}
