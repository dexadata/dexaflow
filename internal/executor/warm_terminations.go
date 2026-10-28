package executor

import (
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// warmTerminationMemory is how long the last terminated reason of a warm worker
// is remembered after it was observed. It outlasts a reaper tick and a polling
// reconcile by a wide margin, so the warm-worker-lost reaper still finds the
// reason of a worker that was deleted between two of its ticks (#1835).
const warmTerminationMemory = 2 * warmCacheLiveListInterval

// warmTerminations remembers the warm-worker container's terminated reason (for
// example OOMKilled) per warm pod name, for warmTerminationMemory after it was
// last observed. On cgroup v2 an OOM kills the whole warm-worker container, and
// the pod is usually gone (deleted by the reconciler as terminal) before the
// warm-worker-lost reaper runs, which leaves no container status to read. This
// memory keeps the cause so the reaper can still name the OOM kill (#1835). It
// only names a cause: it never decides that a worker is dead, which stays with
// the reaper's live LIST (ADR 0058, #461). Entries expire on every read and
// write, so the map is bounded by the warm pods that terminate within one
// window. A nil *warmTerminations records nothing and reads "".
type warmTerminations struct {
	mu   sync.Mutex
	seen map[string]warmTermination
	now  func() time.Time
}

// warmTermination is one remembered terminated reason and when it was observed.
type warmTermination struct {
	reason string
	at     time.Time
}

func newWarmTerminations() *warmTerminations {
	return &warmTerminations{seen: map[string]warmTermination{}, now: time.Now}
}

// observe records p's warm-worker container terminated reason, when it has one.
func (m *warmTerminations) observe(p *corev1.Pod) {
	if m == nil || p == nil {
		return
	}
	reason := warmTerminationReason(p)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
	if reason == "" {
		return
	}
	m.seen[p.Name] = warmTermination{reason: reason, at: m.now()}
}

// reason returns the remembered terminated reason of the named warm pod, or ""
// when none was observed within warmTerminationMemory.
func (m *warmTerminations) reason(name string) string {
	if m == nil {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
	return m.seen[name].reason
}

// pruneLocked drops the entries older than warmTerminationMemory. m.mu held.
func (m *warmTerminations) pruneLocked() {
	now := m.now()
	for name, e := range m.seen {
		if now.Sub(e.at) >= warmTerminationMemory {
			delete(m.seen, name)
		}
	}
}
