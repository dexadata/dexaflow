package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// LeaseTerms is the term side of the leadership lease (ADR 0106): the term
// this replica took at its latest acquisition, and whether it is still the
// lease's current one. internal/storage implements it with the Lease.
type LeaseTerms interface {
	// Term is the term of this replica's latest leadership, 0 before any.
	Term() int64
	// Current reads whether that term is still the lease's.
	Current(ctx context.Context) (bool, error)
}

// defaultTermCacheTTL bounds how often the gate reads the lease. Every
// leader-only side effect (a pod create, a warm pod, a collection delete)
// asks it, so it answers from a cache this young.
const defaultTermCacheTTL = time.Second

// leaseGate is the Scheduler's cached view of LeaseTerms.
type leaseGate struct {
	mu      sync.Mutex
	at      time.Time
	current bool
}

// SetLeaseTerms gives the scheduler its lease's terms, for the pod term
// annotation and the gate in front of leader-only side effects. Call it
// before Run.
func (s *Scheduler) SetLeaseTerms(t LeaseTerms) {
	s.leaseTerms = t
	if s.termCacheTTL == 0 {
		s.termCacheTTL = defaultTermCacheTTL
	}
}

// LeaderTerm is the lease term of this replica's latest leadership, 0
// without a lease.
func (s *Scheduler) LeaderTerm() int64 {
	if s.leaseTerms == nil {
		return 0
	}
	return s.leaseTerms.Term()
}

// TermCurrent reports whether this replica's term is still the lease's
// current one, from a cache at most termCacheTTL old. Without a lease, and
// when the lease cannot be read, it answers true: the gate narrows the window
// of a deposed leader's side effects, it does not stop a leader whose
// database blinked, and the database fence still refuses that leader's
// writes (ADR 0106, point 6).
func (s *Scheduler) TermCurrent(ctx context.Context) bool {
	if s.leaseTerms == nil {
		return true
	}
	g := &s.leaseGate
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.at.IsZero() && time.Since(g.at) < s.termCacheTTL {
		return g.current
	}
	current, err := s.leaseTerms.Current(ctx)
	if err != nil {
		slog.WarnContext(ctx, "reading the scheduler lease term; treating it as current", "error", err)
		return true
	}
	g.current, g.at = current, time.Now()
	return current
}

// FencedLeading is IsLeading for leader-only side effects with lease fencing
// on: leading, and on the lease's current term.
func (s *Scheduler) FencedLeading() bool {
	if !s.IsLeading() {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.TermCurrent(ctx)
}
