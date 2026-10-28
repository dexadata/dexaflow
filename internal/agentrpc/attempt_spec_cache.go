package agentrpc

import (
	"container/list"
	"context"
	"sync"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
)

const (
	// attemptSpecCacheSize bounds how many attempts keep a cached view. An
	// attempt past the bound simply reloads its spec.
	attemptSpecCacheSize = 8192
	// attemptSpecCacheTTL bounds how long an attempt that never reported a
	// terminal state (a lost pod) keeps its entry.
	attemptSpecCacheTTL = 15 * time.Minute
)

// attemptSpec is the slice of a task spec that PushXCom and FetchXCom need:
// the declared inputs and dependencies that authorize a fetch, and the schema a
// push is validated against. All of it comes from the dag version the attempt
// started under: a dag version is never edited, and a clear with
// run_on_latest_version (ResetDagRunToVersion) moves a run to a newer one only
// when none of its task instances is queued or running (ADR 0086), so the
// version of a live attempt does not change under it. A cached attempt keeps
// authorizing and validating its XComs against the version it started under
// (the one its pod's image was built for) until its entry is dropped: when it
// reports a terminal state or a reschedule, or at most attemptSpecCacheTTL
// (15 minutes) after it was cached.
// It deliberately carries nothing else: no environment, call or operator args,
// params, or declared secret names, so the secret RPCs and GetTaskSpec always
// read the store.
type attemptSpec struct {
	XComInputMapping map[string][]string
	DependsOn        []string
	XComSchema       map[string]any
}

func attemptSpecOf(spec TaskSpec) attemptSpec {
	return attemptSpec{XComInputMapping: spec.XComInputMapping, DependsOn: spec.DependsOn, XComSchema: spec.XComSchema}
}

// attemptKey identifies one attempt. Every field of the authenticated identity
// that names the attempt is part of it, so another try, run, tenant or
// execution of the same try (attempt_epoch, ADR 0051) never shares an entry.
// A token without the epoch claim keys as epoch 0 with hasEpoch unset, apart
// from a claimed epoch 0.
type attemptKey struct {
	tenantID, dagID, runID, taskID, taskInstanceID string
	tryNumber, attemptEpoch                        int
	hasEpoch                                       bool
}

func attemptKeyOf(id auth.AgentIdentity) attemptKey {
	return attemptKey{
		tenantID: id.TenantID, dagID: id.DagID, runID: id.RunID, taskID: id.TaskID,
		taskInstanceID: id.TaskInstanceID, tryNumber: id.TryNumber,
		attemptEpoch: id.AttemptEpoch, hasEpoch: id.HasAttemptEpoch,
	}
}

type attemptSpecEntry struct {
	key     attemptKey
	spec    attemptSpec
	expires time.Time
}

// attemptSpecCache keeps each live attempt's attemptSpec so its XCom RPCs do
// not reload the full task spec (a run lookup and a reschedule lookup) on every
// call. An entry is dropped when the attempt reports a terminal state or a
// reschedule, when it expires, or when the cache is full.
//
// Every entry lives ttl from its last put, so the order of the last puts is the
// order of expiry: order holds the entries oldest first, and eviction takes
// from its front instead of scanning the map (#1621).
type attemptSpecCache struct {
	mu      sync.Mutex
	max     int
	ttl     time.Duration
	now     func() time.Time
	entries map[attemptKey]*list.Element // of *attemptSpecEntry
	order   *list.List
}

func newAttemptSpecCache(maxEntries int, ttl time.Duration, now func() time.Time) *attemptSpecCache {
	return &attemptSpecCache{
		max: maxEntries, ttl: ttl, now: now,
		entries: make(map[attemptKey]*list.Element), order: list.New(),
	}
}

func (c *attemptSpecCache) get(id auth.AgentIdentity) (attemptSpec, bool) {
	k := attemptKeyOf(id)
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[k]
	if !ok {
		return attemptSpec{}, false
	}
	e := entryAt(el)
	if !c.now().Before(e.expires) {
		c.removeLocked(el)
		return attemptSpec{}, false
	}
	return e.spec, true
}

func (c *attemptSpecCache) put(id auth.AgentIdentity, spec attemptSpec) {
	k := attemptKeyOf(id)
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[k]; ok {
		e := entryAt(el)
		e.spec, e.expires = spec, now.Add(c.ttl)
		c.order.MoveToBack(el)
		return
	}
	if len(c.entries) >= c.max {
		c.evictLocked(now)
	}
	c.entries[k] = c.order.PushBack(&attemptSpecEntry{key: k, spec: spec, expires: now.Add(c.ttl)})
}

// evictLocked drops the expired entries at the front of order and, if the
// cache is still full, the oldest one. It runs only when a new attempt arrives
// at a full cache, and touches only the entries it drops.
func (c *attemptSpecCache) evictLocked(now time.Time) {
	for el := c.order.Front(); el != nil; el = c.order.Front() {
		if now.Before(entryAt(el).expires) {
			break
		}
		c.removeLocked(el)
	}
	if len(c.entries) >= c.max {
		if el := c.order.Front(); el != nil {
			c.removeLocked(el)
		}
	}
}

// entryAt returns the entry an element of order holds. Only put adds to
// order, always a *attemptSpecEntry; anything else reads as an expired entry,
// so the next get or eviction drops it.
func entryAt(el *list.Element) *attemptSpecEntry {
	if e, ok := el.Value.(*attemptSpecEntry); ok {
		return e
	}
	return &attemptSpecEntry{}
}

func (c *attemptSpecCache) removeLocked(el *list.Element) {
	c.order.Remove(el)
	delete(c.entries, entryAt(el).key)
}

func (c *attemptSpecCache) drop(id auth.AgentIdentity) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[attemptKeyOf(id)]; ok {
		c.removeLocked(el)
	}
}

func (c *attemptSpecCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// xcomSpec returns the caller attempt's attemptSpec, loading the task spec
// from the store only when the attempt has no cached view. A failed load is
// not cached.
func (s *Server) xcomSpec(ctx context.Context, id *auth.AgentIdentity) (attemptSpec, error) {
	if spec, ok := s.attemptSpecs.get(*id); ok {
		return spec, nil
	}
	spec, err := s.store.TaskSpec(ctx, *id)
	if err != nil {
		return attemptSpec{}, err
	}
	view := attemptSpecOf(spec)
	s.attemptSpecs.put(*id, view)
	return view, nil
}
