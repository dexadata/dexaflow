package storage

import (
	"container/list"
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/scheduler"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// defaultSpecCacheEntries bounds the versions kept that are not any DAG's
// current version: old versions still referenced by active runs. Current
// versions are pinned and do not count against it.
const defaultSpecCacheEntries = 512

// cachedSpec is the memoized parse of one dag_versions row: the version row
// itself (without its raw spec bytes), its decoded DAGSpec, and the scheduler's
// task index over the spec's tasks. All three are treated as immutable once
// set. The index is built on first use by the scheduler tick (getWithGraph), so
// the API and agent paths, which only read the spec, never pay for it.
type cachedSpec struct {
	id        pgtype.UUID
	version   queries.DagVersion
	spec      domain.DAGSpec
	graphOnce sync.Once
	graph     *scheduler.TaskGraph
	// pins counts the DAGs whose current version this is. A pinned entry is
	// never evicted and is kept out of the LRU list (elem is nil).
	pins int
	elem *list.Element
	// tick is the last scheduler tick that read this version (0: none).
	tick uint64
}

// currentSpecKey names one DAG for pinning its current version.
type currentSpecKey struct {
	tenant pgtype.UUID
	dagID  string
}

// specCache memoizes parsed DAG specs keyed by dag_version_id.
//
// Immutability (why no invalidation is needed): a dag_versions row is
// insert-only. A changed DAG is a NEW row (queries.InsertDagVersion) with a new
// id; the only UPDATE that touches versioning is SetCurrentDagVersion, which
// repoints dags.current_version_id and never rewrites a version's spec column.
// So a given dag_version_id maps to one spec for the life of the process: the
// parse is valid forever and the cache never has to be invalidated.
//
// Bound: every DAG's current version, as last seen by getCurrent, is pinned.
// Other versions (old ones that active runs still reference) live in an LRU of
// at most max entries. The raw spec JSON is dropped once decoded, so an entry
// costs only the decoded spec.
//
// Tick working set: the scheduler reads the version of every active run on
// every tick (beginTick, then getForTick per run). A version read in a tick is
// not evicted until the following tick has ended without reading it, even past
// max, so a tick over more active versions than the bound never thrashes: the
// cache grows to the active set and returns to max once those runs finish.
//
// Concurrency: the scheduler tick (ActiveRuns), the pod-dispatch/agent path
// (ExecutionStore.resolve) and the API (Repository.GetCurrentSpec) read this
// from different goroutines, so access is guarded by a mutex. Fills are
// idempotent: two goroutines racing on a cold key both decode the same
// immutable bytes and store equal values.
//
// Ownership: the cached spec is shared read-only. Callers that need to MUTATE
// the spec for a run (e.g. ActiveRuns filling per-task retry defaults) must copy
// the Tasks slice first so they never write through the shared backing array.
type specCache struct {
	mu      sync.Mutex
	max     int
	entries map[pgtype.UUID]*cachedSpec
	lru     *list.List // unpinned entries, most recently used at the front.
	current map[currentSpecKey]pgtype.UUID
	// tick is the scheduler tick generation, advanced by beginTick.
	tick uint64
}

// versionGetter is the one query the cache needs on a cold key. *queries.Queries
// satisfies it; tests supply a counting fake so the cache's memoization and
// no-shared-mutation guarantees are unit-testable without a database.
type versionGetter interface {
	GetDagVersionByID(ctx context.Context, id pgtype.UUID) (queries.DagVersion, error)
}

// newSpecCache builds an empty spec cache ready for concurrent use.
func newSpecCache() *specCache { return newSpecCacheWithLimit(defaultSpecCacheEntries) }

// newSpecCacheWithLimit builds an empty cache keeping at most max unpinned
// versions (at least one).
func newSpecCacheWithLimit(maxEntries int) *specCache {
	return &specCache{
		max:     max(maxEntries, 1),
		entries: make(map[pgtype.UUID]*cachedSpec),
		lru:     list.New(),
		current: make(map[currentSpecKey]pgtype.UUID),
	}
}

// sharedSpecCache returns the Postgres-owned cache, or a fresh private one when
// the handle was built without NewPostgres (e.g. a bare &Postgres{} in a test).
// A private cache is still correct; it just is not shared across stores.
func sharedSpecCache(pg *Postgres) *specCache {
	if pg.specs != nil {
		return pg.specs
	}
	return newSpecCache()
}

// get returns the version row and decoded spec for a dag_version_id, decoding it
// on a cold key (one GetDagVersionByID + one json.Unmarshal) and serving every
// later call for that id from memory. The returned spec is shared and MUST NOT be
// mutated (see the type doc). The returned version has no raw Spec bytes.
func (c *specCache) get(ctx context.Context, q versionGetter, versionID pgtype.UUID) (queries.DagVersion, domain.DAGSpec, error) {
	return c.load(ctx, q, versionID, nil, false)
}

// beginTick starts a scheduler tick: versions read through getForTick from now
// on belong to this tick's working set.
func (c *specCache) beginTick() {
	c.mu.Lock()
	c.tick++
	c.mu.Unlock()
}

// getForTick is get for the scheduler tick: it also marks the version as part
// of the current tick's working set, so it is not evicted before the next tick
// has had the chance to read it again.
func (c *specCache) getForTick(ctx context.Context, q versionGetter, versionID pgtype.UUID) (queries.DagVersion, domain.DAGSpec, error) {
	return c.load(ctx, q, versionID, nil, true)
}

// getWithGraph is getForTick plus the task index over the version's tasks,
// built on the first call for the version. Versions are immutable, so the index
// is built once per version and shared read-only by every run of it; it stays
// valid for a per-run copy of the spec's Tasks.
func (c *specCache) getWithGraph(ctx context.Context, q versionGetter, versionID pgtype.UUID) (domain.DAGSpec, *scheduler.TaskGraph, error) {
	e, err := c.loadEntry(ctx, q, versionID, nil, true)
	if err != nil {
		return domain.DAGSpec{}, nil, err
	}
	e.graphOnce.Do(func() { e.graph = scheduler.NewTaskGraph(e.spec.Tasks) })
	return e.spec, e.graph, nil
}

// getCurrent is get for the version a DAG currently points at: it also pins
// that version, and unpins the version the DAG pointed at before, if any.
func (c *specCache) getCurrent(ctx context.Context, q versionGetter, key currentSpecKey, versionID pgtype.UUID) (queries.DagVersion, domain.DAGSpec, error) {
	return c.load(ctx, q, versionID, &key, false)
}

func (c *specCache) load(ctx context.Context, q versionGetter, versionID pgtype.UUID, pin *currentSpecKey, inTick bool) (queries.DagVersion, domain.DAGSpec, error) {
	e, err := c.loadEntry(ctx, q, versionID, pin, inTick)
	if err != nil {
		return queries.DagVersion{}, domain.DAGSpec{}, err
	}
	return e.version, e.spec, nil
}

// loadEntry returns the cached entry for a dag_version_id, filling it on a cold
// key, and applies the pin and tick bookkeeping of load.
func (c *specCache) loadEntry(ctx context.Context, q versionGetter, versionID pgtype.UUID, pin *currentSpecKey, inTick bool) (*cachedSpec, error) {
	c.mu.Lock()
	if e, ok := c.entries[versionID]; ok {
		c.touch(e, pin, inTick)
		c.mu.Unlock()
		return e, nil
	}
	c.mu.Unlock()

	version, err := q.GetDagVersionByID(ctx, versionID)
	if err != nil {
		return nil, fmt.Errorf("loading dag version: %w", err)
	}
	var spec domain.DAGSpec
	if uerr := json.Unmarshal(version.Spec, &spec); uerr != nil {
		return nil, fmt.Errorf("decoding spec: %w", uerr)
	}
	version.Spec = nil

	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[versionID]
	if !ok {
		e = &cachedSpec{id: versionID, version: version, spec: spec}
		e.elem = c.lru.PushFront(e)
		c.entries[versionID] = e
	}
	c.touch(e, pin, inTick)
	c.evict()
	return e, nil
}

// touch marks e as just used (and read by the current tick when inTick) and
// applies a pin. The caller holds c.mu.
func (c *specCache) touch(e *cachedSpec, pin *currentSpecKey, inTick bool) {
	if inTick {
		e.tick = c.tick
	}
	if pin != nil {
		if prev, ok := c.current[*pin]; !ok || prev != e.id {
			if ok {
				c.unpin(prev)
			}
			c.current[*pin] = e.id
			e.pins++
			if e.elem != nil {
				c.lru.Remove(e.elem)
				e.elem = nil
			}
		}
	}
	if e.elem != nil {
		c.lru.MoveToFront(e.elem)
	}
}

// unpin drops one pin from a version, returning it to the LRU when it has none
// left. The caller holds c.mu.
func (c *specCache) unpin(id pgtype.UUID) {
	e, ok := c.entries[id]
	if !ok || e.pins == 0 {
		return
	}
	e.pins--
	if e.pins == 0 {
		e.elem = c.lru.PushFront(e)
	}
}

// evict drops least recently used unpinned versions past the limit, skipping
// those in the tick working set (read in this tick or the previous one). When
// every unpinned version is in the working set the cache stays over the limit
// until a later tick releases them. The caller holds c.mu.
func (c *specCache) evict() {
	for skipped := 0; c.lru.Len() > c.max && skipped < c.lru.Len(); {
		oldest := c.lru.Back()
		e, ok := oldest.Value.(*cachedSpec)
		if ok && c.inTickWorkingSet(e) {
			c.lru.MoveToFront(oldest)
			skipped++
			continue
		}
		c.lru.Remove(oldest)
		if ok {
			delete(c.entries, e.id)
		}
	}
}

// inTickWorkingSet reports whether the scheduler read e in the current tick or
// the one before it. The caller holds c.mu.
func (c *specCache) inTickWorkingSet(e *cachedSpec) bool {
	return e.tick != 0 && e.tick+1 >= c.tick
}
