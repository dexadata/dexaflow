package agentrpc

import (
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
// started under: a dag version is never edited, but a clear with
// run_on_latest_version (ResetDagRunToVersion) can rebind the run to a newer
// one while sibling attempts still run. A cached attempt does not see that
// rebind and keeps authorizing and validating its XComs against the version it
// started under (the one its pod's image was built for) until its entry is
// dropped: when it reports a terminal state or a reschedule, or at most
// attemptSpecCacheTTL (15 minutes) after it was cached. Until then another
// replica that has no entry for the attempt may already load the new version.
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
// that names the attempt is part of it, so another try, run or tenant never
// shares an entry.
type attemptKey struct {
	tenantID, dagID, runID, taskID, taskInstanceID string
	tryNumber                                      int
}

func attemptKeyOf(id auth.AgentIdentity) attemptKey {
	return attemptKey{
		tenantID: id.TenantID, dagID: id.DagID, runID: id.RunID, taskID: id.TaskID,
		taskInstanceID: id.TaskInstanceID, tryNumber: id.TryNumber,
	}
}

type attemptSpecEntry struct {
	spec    attemptSpec
	expires time.Time
}

// attemptSpecCache keeps each live attempt's attemptSpec so its XCom RPCs do
// not reload the full task spec (a run lookup and a reschedule lookup) on every
// call. An entry is dropped when the attempt reports a terminal state or a
// reschedule, when it expires, or when the cache is full.
type attemptSpecCache struct {
	mu      sync.Mutex
	max     int
	ttl     time.Duration
	now     func() time.Time
	entries map[attemptKey]attemptSpecEntry
}

func newAttemptSpecCache(maxEntries int, ttl time.Duration, now func() time.Time) *attemptSpecCache {
	return &attemptSpecCache{max: maxEntries, ttl: ttl, now: now, entries: make(map[attemptKey]attemptSpecEntry)}
}

func (c *attemptSpecCache) get(id auth.AgentIdentity) (attemptSpec, bool) {
	k := attemptKeyOf(id)
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[k]
	if !ok {
		return attemptSpec{}, false
	}
	if !c.now().Before(e.expires) {
		delete(c.entries, k)
		return attemptSpec{}, false
	}
	return e.spec, true
}

func (c *attemptSpecCache) put(id auth.AgentIdentity, spec attemptSpec) {
	k := attemptKeyOf(id)
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[k]; !ok && len(c.entries) >= c.max {
		c.evictLocked(now)
	}
	c.entries[k] = attemptSpecEntry{spec: spec, expires: now.Add(c.ttl)}
}

// evictLocked drops every expired entry and, if the cache is still full, the
// one closest to expiring (the oldest). It runs only when a new attempt
// arrives at a full cache.
func (c *attemptSpecCache) evictLocked(now time.Time) {
	var oldest attemptKey
	var oldestAt time.Time
	first := true
	for k, e := range c.entries {
		if !now.Before(e.expires) {
			delete(c.entries, k)
			continue
		}
		if first || e.expires.Before(oldestAt) {
			oldest, oldestAt, first = k, e.expires, false
		}
	}
	if len(c.entries) >= c.max && !first {
		delete(c.entries, oldest)
	}
}

func (c *attemptSpecCache) drop(id auth.AgentIdentity) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, attemptKeyOf(id))
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
