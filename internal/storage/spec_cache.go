package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// cachedSpec is the memoized parse of one dag_versions row: the version row
// itself plus its decoded DAGSpec. Both are treated as immutable once cached.
type cachedSpec struct {
	version queries.DagVersion
	spec    domain.DAGSpec
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
// Concurrency: the scheduler tick (ActiveRuns) and the pod-dispatch/agent path
// (ExecutionStore.resolve) read this from different goroutines, so access is
// guarded by an RWMutex. Fills are idempotent — two goroutines racing on a cold
// key both decode the same immutable bytes and store equal values.
//
// Ownership: the cached spec is shared read-only. Callers that need to MUTATE
// the spec for a run (e.g. ActiveRuns filling per-task retry defaults) must copy
// the Tasks slice first so they never write through the shared backing array.
type specCache struct {
	mu      sync.RWMutex
	entries map[pgtype.UUID]cachedSpec
}

// versionGetter is the one query the cache needs on a cold key. *queries.Queries
// satisfies it; tests supply a counting fake so the cache's memoization and
// no-shared-mutation guarantees are unit-testable without a database.
type versionGetter interface {
	GetDagVersionByID(ctx context.Context, id pgtype.UUID) (queries.DagVersion, error)
}

// newSpecCache builds an empty spec cache ready for concurrent use.
func newSpecCache() *specCache {
	return &specCache{entries: make(map[pgtype.UUID]cachedSpec)}
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
// mutated — see the type doc.
func (c *specCache) get(ctx context.Context, q versionGetter, versionID pgtype.UUID) (queries.DagVersion, domain.DAGSpec, error) {
	c.mu.RLock()
	entry, ok := c.entries[versionID]
	c.mu.RUnlock()
	if ok {
		return entry.version, entry.spec, nil
	}

	version, err := q.GetDagVersionByID(ctx, versionID)
	if err != nil {
		return queries.DagVersion{}, domain.DAGSpec{}, fmt.Errorf("loading dag version: %w", err)
	}
	var spec domain.DAGSpec
	if uerr := json.Unmarshal(version.Spec, &spec); uerr != nil {
		return queries.DagVersion{}, domain.DAGSpec{}, fmt.Errorf("decoding spec: %w", uerr)
	}

	c.mu.Lock()
	c.entries[versionID] = cachedSpec{version: version, spec: spec}
	c.mu.Unlock()
	return version, spec, nil
}
