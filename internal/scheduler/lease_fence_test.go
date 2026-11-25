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

// blockingTerms is a LeaseTerms whose read waits until released.
type blockingTerms struct {
	fakeTerms
	started chan struct{}
	release chan struct{}
}

func (b *blockingTerms) Current(ctx context.Context) (bool, error) {
	b.started <- struct{}{}
	<-b.release
	return b.fakeTerms.Current(ctx)
}

// TestLeaseGateReadsOnceWhileOthersAnswerFromTheCache: while one caller reads
// the lease, the others answer at once from the last answer instead of
// queuing on a slow database, and a failed read is cached like an answer.
func TestLeaseGateReadsOnceWhileOthersAnswerFromTheCache(t *testing.T) {
	b := &blockingTerms{started: make(chan struct{}, 1), release: make(chan struct{})}
	b.term.Store(3)
	b.current.Store(false)
	s := &Scheduler{}
	s.SetLeaseTerms(b)
	s.termCacheTTL = time.Hour

	done := make(chan bool)
	go func() { done <- s.TermCurrent(context.Background()) }()
	<-b.started
	for range 5 {
		if !s.TermCurrent(context.Background()) {
			t.Fatal("a caller with no answer yet should get the open gate, not wait")
		}
	}
	close(b.release)
	if <-done {
		t.Fatal("the reader should return the lease's answer: not current")
	}
	if s.TermCurrent(context.Background()) || b.reads.Load() != 1 {
		t.Fatalf("after the read, the cache answers: reads %d, want 1", b.reads.Load())
	}

	f := &fakeTerms{err: errors.New("database down")}
	f.term.Store(1)
	s2 := &Scheduler{}
	s2.SetLeaseTerms(f)
	s2.termCacheTTL = time.Hour
	for range 3 {
		if !s2.TermCurrent(context.Background()) {
			t.Fatal("an unreadable lease keeps the gate open")
		}
	}
	if f.reads.Load() != 1 {
		t.Fatalf("a failed read was retried %d times within one interval, want once", f.reads.Load())
	}
}

// TestLeaseGateRereadsOnANewTerm: a re-elected leader does not answer from
// its previous term's reading.
func TestLeaseGateRereadsOnANewTerm(t *testing.T) {
	f := &fakeTerms{}
	f.term.Store(5)
	f.current.Store(false)
	s := &Scheduler{}
	s.SetLeaseTerms(f)
	s.termCacheTTL = time.Hour
	if s.TermCurrent(context.Background()) {
		t.Fatal("term 5 is not current")
	}
	f.term.Store(7)
	f.current.Store(true)
	if !s.TermCurrent(context.Background()) {
		t.Fatal("the new term 7 answered from term 5's cached reading")
	}
}
