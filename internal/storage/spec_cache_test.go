package storage

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// countingVersionGetter is a versionGetter that counts fetches per id and serves
// a fixed spec, standing in for GetDagVersionByID without a database.
type countingVersionGetter struct {
	spec  domain.DAGSpec
	calls map[pgtype.UUID]int
}

func (g *countingVersionGetter) GetDagVersionByID(_ context.Context, id pgtype.UUID) (queries.DagVersion, error) {
	if g.calls == nil {
		g.calls = map[pgtype.UUID]int{}
	}
	g.calls[id]++
	raw, err := json.Marshal(g.spec)
	if err != nil {
		return queries.DagVersion{}, err
	}
	return queries.DagVersion{ID: id, ImageReference: "img:v1", Spec: raw}, nil
}

func versionUUID(b byte) pgtype.UUID {
	var u pgtype.UUID
	u.Valid = true
	for i := range u.Bytes {
		u.Bytes[i] = b
	}
	return u
}

// TestSpecCacheMemoizesPerVersion pins requirement (a): a repeated dag_version_id
// is fetched and decoded exactly once, and distinct ids each fetch once. This is
// what removes the per-run GetDagVersionByID round-trip + json.Unmarshal from a
// scheduler tick over many runs of the same DAG version.
func TestSpecCacheMemoizesPerVersion(t *testing.T) {
	getter := &countingVersionGetter{spec: domain.DAGSpec{
		DagID: "d", Tasks: []domain.TaskSpec{{TaskID: "a"}},
	}}
	cache := newSpecCache()
	ctx := context.Background()
	v1 := versionUUID(0x11)
	v2 := versionUUID(0x22)

	for i := 0; i < 5; i++ {
		if _, _, err := cache.get(ctx, getter, v1); err != nil {
			t.Fatalf("get v1 #%d: %v", i, err)
		}
	}
	if _, _, err := cache.get(ctx, getter, v2); err != nil {
		t.Fatalf("get v2: %v", err)
	}

	if getter.calls[v1] != 1 {
		t.Errorf("v1 fetched %d times, want exactly 1 (memoized)", getter.calls[v1])
	}
	if getter.calls[v2] != 1 {
		t.Errorf("v2 fetched %d times, want exactly 1", getter.calls[v2])
	}
}

// TestSpecCacheReturnsEqualContent pins that repeated reads return the same spec
// content (same DagID, same tasks), the object the scheduler and dispatch paths
// both consume.
func TestSpecCacheReturnsEqualContent(t *testing.T) {
	getter := &countingVersionGetter{spec: domain.DAGSpec{
		DagID: "etl",
		Tasks: []domain.TaskSpec{
			{TaskID: "extract"},
			{TaskID: "load", DependsOn: []string{"extract"}},
		},
	}}
	cache := newSpecCache()
	ctx := context.Background()
	v := versionUUID(0x33)

	_, first, err := cache.get(ctx, getter, v)
	if err != nil {
		t.Fatalf("first get: %v", err)
	}
	_, second, err := cache.get(ctx, getter, v)
	if err != nil {
		t.Fatalf("second get: %v", err)
	}
	if first.DagID != "etl" || len(first.Tasks) != 2 {
		t.Fatalf("unexpected spec: %+v", first)
	}
	if second.DagID != first.DagID || len(second.Tasks) != len(first.Tasks) {
		t.Errorf("repeated read differs: %+v vs %+v", first, second)
	}
	if second.Tasks[1].TaskID != "load" || len(second.Tasks[1].DependsOn) != 1 {
		t.Errorf("dependencies not preserved on cached read: %+v", second.Tasks[1])
	}
}

// TestSpecCacheNotSharedMutated pins the other half of requirement (a): the
// ActiveRuns copy discipline (copy Tasks before applyDefaultRetries) means a run
// filling its retry defaults on the returned spec must NOT write through to the
// shared cache entry. Without the copy, the first run's applyDefaultRetries would
// mutate every later run's view of the same version.
func TestSpecCacheNotSharedMutated(t *testing.T) {
	getter := &countingVersionGetter{spec: domain.DAGSpec{
		DagID:       "d",
		DefaultArgs: &domain.DefaultArgs{Retries: 3},
		Tasks:       []domain.TaskSpec{{TaskID: "a"}}, // no explicit retries
	}}
	cache := newSpecCache()
	ctx := context.Background()
	v := versionUUID(0x44)

	// Simulate one ActiveRuns iteration: get the shared spec, copy Tasks, then
	// fill defaults on the copy exactly as ActiveRuns does.
	_, cached, err := cache.get(ctx, getter, v)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	spec := cached
	spec.Tasks = make([]domain.TaskSpec, len(cached.Tasks))
	copy(spec.Tasks, cached.Tasks)
	applyDefaultRetries(&spec)
	if spec.Tasks[0].Retries == nil || *spec.Tasks[0].Retries != 3 {
		t.Fatalf("per-run copy should carry filled default retries, got %v", spec.Tasks[0].Retries)
	}

	// A second run's read must see the original, unmutated spec: Retries still nil.
	_, again, err := cache.get(ctx, getter, v)
	if err != nil {
		t.Fatalf("second get: %v", err)
	}
	if again.Tasks[0].Retries != nil {
		t.Errorf("cache was mutated through the shared Tasks slice: Retries=%v, want nil",
			*again.Tasks[0].Retries)
	}
}
