package storage

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dexadata/dexaflow/internal/domain"
)

func dagKey(dagID string) currentSpecKey {
	return currentSpecKey{tenant: versionUUID(0x01), dagID: dagID}
}

func mustGet(t *testing.T, cache *specCache, getter versionGetter, ids ...pgtype.UUID) {
	t.Helper()
	for _, id := range ids {
		if _, _, err := cache.get(context.Background(), getter, id); err != nil {
			t.Fatalf("get: %v", err)
		}
	}
}

// TestSpecCacheEvictsLeastRecentlyUsed pins the bound: past its limit the cache
// drops the version read longest ago and keeps the ones read recently.
func TestSpecCacheEvictsLeastRecentlyUsed(t *testing.T) {
	getter := &countingVersionGetter{spec: domain.DAGSpec{DagID: "d"}}
	cache := newSpecCacheWithLimit(2)
	v1, v2, v3 := versionUUID(0x11), versionUUID(0x22), versionUUID(0x33)

	mustGet(t, cache, getter, v1, v2, v1, v3) // v2 is the least recently used.
	mustGet(t, cache, getter, v1, v3)
	if getter.calls[v1] != 1 || getter.calls[v3] != 1 {
		t.Errorf("recent versions refetched: v1=%d v3=%d, want 1 each", getter.calls[v1], getter.calls[v3])
	}
	mustGet(t, cache, getter, v2)
	if getter.calls[v2] != 2 {
		t.Errorf("v2 fetched %d times, want 2 (evicted, then refetched)", getter.calls[v2])
	}
}

// TestSpecCachePinsCurrentVersions pins that a DAG's current version survives
// any amount of churn from other versions, and becomes evictable once the DAG
// moves on to a newer version.
func TestSpecCachePinsCurrentVersions(t *testing.T) {
	getter := &countingVersionGetter{spec: domain.DAGSpec{DagID: "d"}}
	cache := newSpecCacheWithLimit(1)
	ctx := context.Background()
	cur, next := versionUUID(0x10), versionUUID(0x20)

	if _, _, err := cache.getCurrent(ctx, getter, dagKey("etl"), cur); err != nil {
		t.Fatal(err)
	}
	mustGet(t, cache, getter, versionUUID(0x31), versionUUID(0x32), versionUUID(0x33))
	if _, _, err := cache.getCurrent(ctx, getter, dagKey("etl"), cur); err != nil {
		t.Fatal(err)
	}
	if getter.calls[cur] != 1 {
		t.Fatalf("current version fetched %d times, want 1 (pinned)", getter.calls[cur])
	}

	// A new version becomes current: the old one is unpinned and can go.
	if _, _, err := cache.getCurrent(ctx, getter, dagKey("etl"), next); err != nil {
		t.Fatal(err)
	}
	mustGet(t, cache, getter, versionUUID(0x34), versionUUID(0x35))
	mustGet(t, cache, getter, cur)
	if getter.calls[cur] != 2 {
		t.Errorf("old current version fetched %d times, want 2 (unpinned, evicted)", getter.calls[cur])
	}
	if _, _, err := cache.getCurrent(ctx, getter, dagKey("etl"), next); err != nil {
		t.Fatal(err)
	}
	if getter.calls[next] != 1 {
		t.Errorf("new current version fetched %d times, want 1 (pinned)", getter.calls[next])
	}
}

// TestSpecCacheDropsRawSpecBytes pins that the cache keeps only the decoded
// spec, not the JSON it was decoded from as well.
func TestSpecCacheDropsRawSpecBytes(t *testing.T) {
	getter := &countingVersionGetter{spec: domain.DAGSpec{DagID: "d", Tasks: []domain.TaskSpec{{TaskID: "a"}}}}
	cache := newSpecCache()
	ver, spec, err := cache.get(context.Background(), getter, versionUUID(0x11))
	if err != nil {
		t.Fatal(err)
	}
	if ver.Spec != nil {
		t.Errorf("cached version kept %d raw spec bytes, want none", len(ver.Spec))
	}
	if ver.ImageReference != "img:v1" || len(spec.Tasks) != 1 {
		t.Errorf("version or spec lost data: image %q, %d tasks", ver.ImageReference, len(spec.Tasks))
	}
}

func tickGet(t *testing.T, cache *specCache, getter versionGetter, ids []pgtype.UUID) {
	t.Helper()
	cache.beginTick()
	for _, id := range ids {
		if _, _, err := cache.getForTick(context.Background(), getter, id); err != nil {
			t.Fatalf("getForTick: %v", err)
		}
	}
}

func totalCalls(g *countingVersionGetter) int {
	n := 0
	for _, c := range g.calls {
		n += c
	}
	return n
}

// TestSpecCacheKeepsTheTickWorkingSet pins that a scheduler tick reading more
// active versions than the bound does not thrash: every version read in a tick
// stays cached through the next one, so the second tick fetches nothing.
func TestSpecCacheKeepsTheTickWorkingSet(t *testing.T) {
	getter := &countingVersionGetter{spec: domain.DAGSpec{DagID: "d"}}
	cache := newSpecCacheWithLimit(4)
	active := make([]pgtype.UUID, 0, 10)
	for i := range 10 {
		active = append(active, versionUUID(byte(0x40+i)))
	}

	tickGet(t, cache, getter, active)
	if got := totalCalls(getter); got != 10 {
		t.Fatalf("first tick fetched %d versions, want 10", got)
	}
	tickGet(t, cache, getter, active)
	if got := totalCalls(getter); got != 10 {
		t.Errorf("second tick fetched %d more versions, want 0", got-10)
	}
	// An API read of another version must not evict the tick's working set.
	mustGet(t, cache, getter, versionUUID(0x7f))
	tickGet(t, cache, getter, active)
	if got := totalCalls(getter); got != 11 {
		t.Errorf("third tick fetched %d more versions, want 0", got-11)
	}
}

// TestSpecCacheReleasesVersionsTheTickStopsReading pins that the tick
// protection lapses: once two ticks pass without reading a version (its runs
// finished), it is evictable again and the cache returns to its bound.
func TestSpecCacheReleasesVersionsTheTickStopsReading(t *testing.T) {
	getter := &countingVersionGetter{spec: domain.DAGSpec{DagID: "d"}}
	cache := newSpecCacheWithLimit(2)
	old := []pgtype.UUID{versionUUID(0x51), versionUUID(0x52), versionUUID(0x53), versionUUID(0x54)}

	tickGet(t, cache, getter, old)
	tickGet(t, cache, getter, nil)
	tickGet(t, cache, getter, nil)
	mustGet(t, cache, getter, versionUUID(0x61))
	cache.mu.Lock()
	n := cache.lru.Len()
	cache.mu.Unlock()
	if n > 2 {
		t.Errorf("cache holds %d unpinned versions after the tick dropped them, want at most 2", n)
	}
}
