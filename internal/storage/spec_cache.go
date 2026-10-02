package storage

import (
	"container/list"
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// defaultSpecCacheEntries bounds the versions kept that are not any DAG's
// current version: old versions still referenced by active runs. Current
// versions are pinned and do not count against it.
const defaultSpecCacheEntries = 512

// cachedSpec is the memoized parse of one dag_versions row: the version row
// itself (without its raw spec bytes) plus its decoded DAGSpec. Both are
// treated as immutable once cached.
type cachedSpec struct {
	id      pgtype.UUID
	version queries.DagVersion
	spec    domain.DAGSpec
	// pins counts the DAGs whose current version this is. A pinned entry is
	// never evicted and is kept out of the LRU list (elem is nil).
	pins int
	elem *list.Element
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
// So a given dag_version_id maps to one spec for the life of the process — the
// parse is valid forever and the cache never has to be invalidated.
//
// Bound: every DAG's current version, as last seen by getCurrent, is pinned.
// Other versions (old ones that active runs still reference) live in an LRU of
// at most max entries. The raw spec JSON is dropped once decoded, so an entry
// costs only the decoded spec.
//
// Concurrency: the scheduler tick (ActiveRuns), the pod-dispatch/agent path
// (ExecutionStore.resolve) and the API (Repository.GetCurrentSpec) read this
// from different goroutines, so access is guarded by a mutex. Fills are
// idempotent — two goroutines racing on a cold key both decode the same
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
// A private cache is still correct — it just is not shared across stores.
func sharedSpecCache(pg *Postgres) *specCache {
	if pg.specs != nil {
		return pg.specs
	}
	return newSpecCache()
}

// get returns the version row and decoded spec for a dag_version_id, decoding it
// on a cold key (one GetDagVersionByID + one json.Unmarshal) and serving every
// later call for that id from memory. The returned spec is shared and MUST NOT be
// mutated — see the type doc. The returned version has no raw Spec bytes.
func (c *specCache) get(ctx context.Context, q versionGetter, versionID pgtype.UUID) (queries.DagVersion, domain.DAGSpec, error) {
	return c.load(ctx, q, versionID, nil)
}

// getCurrent is get for the version a DAG currently points at: it also pins
// that version, and unpins the version the DAG pointed at before, if any.
func (c *specCache) getCurrent(ctx context.Context, q versionGetter, key currentSpecKey, versionID pgtype.UUID) (queries.DagVersion, domain.DAGSpec, error) {
	return c.load(ctx, q, versionID, &key)
}

func (c *specCache) load(ctx context.Context, q versionGetter, versionID pgtype.UUID, pin *currentSpecKey) (queries.DagVersion, domain.DAGSpec, error) {
	c.mu.Lock()
	if e, ok := c.entries[versionID]; ok {
		c.touch(e, pin)
		c.mu.Unlock()
		return e.version, e.spec, nil
	}
	c.mu.Unlock()

	version, err := q.GetDagVersionByID(ctx, versionID)
	if err != nil {
		return queries.DagVersion{}, domain.DAGSpec{}, fmt.Errorf("loading dag version: %w", err)
	}
	var spec domain.DAGSpec
	if uerr := json.Unmarshal(version.Spec, &spec); uerr != nil {
		return queries.DagVersion{}, domain.DAGSpec{}, fmt.Errorf("decoding spec: %w", uerr)
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
	c.touch(e, pin)
	c.evict()
	return e.version, e.spec, nil
}

// touch marks e as just used and applies a pin. The caller holds c.mu.
func (c *specCache) touch(e *cachedSpec, pin *currentSpecKey) {
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

// evict drops least recently used unpinned versions past the limit. The caller
// holds c.mu.
func (c *specCache) evict() {
	for c.lru.Len() > c.max {
		oldest := c.lru.Back()
		e, ok := c.lru.Remove(oldest).(*cachedSpec)
		if ok {
			delete(c.entries, e.id)
		}
	}
}
