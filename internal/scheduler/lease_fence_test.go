package scheduler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type fakeTerms struct {
	term    atomic.Int64
	current atomic.Bool
	err     error
	reads   atomic.Int64
}

func (f *fakeTerms) Term() int64 { return f.term.Load() }

func (f *fakeTerms) Current(context.Context) (bool, error) {
	f.reads.Add(1)
	return f.current.Load(), f.err
}

// TestLeaseGateFollowsTheCurrentTerm: the gate in front of the leader's side
// effects (ADR 0106, point 6) closes once another replica holds a newer
// term, even while this replica still believes it leads, and reads the lease
// at most once per cache interval.
func TestLeaseGateFollowsTheCurrentTerm(t *testing.T) {
	s := &Scheduler{}
	if s.LeaderTerm() != 0 || !s.TermCurrent(context.Background()) {
		t.Fatal("without a lease the term is 0 and the gate stays open")
	}
	f := &fakeTerms{}
	f.term.Store(4)
	f.current.Store(true)
	s.SetLeaseTerms(f)
	s.SetLeading(true)
	s.termCacheTTL = time.Hour

	if s.LeaderTerm() != 4 {
		t.Fatalf("LeaderTerm = %d, want 4", s.LeaderTerm())
	}
	if !s.FencedLeading() {
		t.Fatal("leading on the current term, gate closed")
	}
	f.current.Store(false)
	if !s.FencedLeading() {
		t.Fatal("the cached answer should hold within the interval")
	}
	if f.reads.Load() != 1 {
		t.Fatalf("lease read %d times within one interval, want 1", f.reads.Load())
	}
	s.termCacheTTL = 0
	if s.FencedLeading() || s.TermCurrent(context.Background()) {
		t.Fatal("a newer term holds the lease, gate open")
	}
	s.SetLeading(false)
	f.current.Store(true)
	if s.FencedLeading() {
		t.Fatal("not leading, gate open")
	}
}

// TestLeaseGateOpensOnAnUnreadableLease: a lease read that fails leaves the
// gate open, so a database blip does not stop every dispatch; the database
// fence still refuses the deposed leader's writes.
func TestLeaseGateOpensOnAnUnreadableLease(t *testing.T) {
	s := &Scheduler{}
	f := &fakeTerms{err: errors.New("blip")}
	s.SetLeaseTerms(f)
	if !s.TermCurrent(context.Background()) {
		t.Fatal("an unreadable lease closed the gate")
	}
}
