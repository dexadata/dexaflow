package serverapp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/dexadata/dexaflow/internal/observability"
	"github.com/dexadata/dexaflow/internal/storage"
)

type fakeWaitingSource struct {
	ages  []storage.WaitingAge
	err   error
	reads int
}

func (f *fakeWaitingSource) OldestWaiting(context.Context) ([]storage.WaitingAge, error) {
	f.reads++
	return f.ages, f.err
}

type fakeWaitingGauge struct {
	sets [][]observability.WaitingAge
}

func (f *fakeWaitingGauge) SetOldestWaiting(ages []observability.WaitingAge) {
	f.sets = append(f.sets, ages)
}

func TestOldestWaitingLeaderPublishesTheAges(t *testing.T) {
	src := &fakeWaitingSource{ages: []storage.WaitingAge{{Tenant: "acme", State: "queued", AgeSeconds: 12}}}
	g := &fakeWaitingGauge{}
	w := &oldestWaiting{src: src, gauge: g, leading: func() bool { return true }, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	w.cycle(context.Background())
	if len(g.sets) != 1 || len(g.sets[0]) != 1 {
		t.Fatalf("sets = %v, want one set with one age", g.sets)
	}
	if got := g.sets[0][0]; got != (observability.WaitingAge{Tenant: "acme", State: "queued", AgeSeconds: 12}) {
		t.Fatalf("age = %+v", got)
	}
}

// A follower reads nothing and clears its series, so only the leader exports
// ages and a replica that lost the lead does not keep stale ones.
func TestOldestWaitingFollowerClearsWithoutReading(t *testing.T) {
	src := &fakeWaitingSource{}
	g := &fakeWaitingGauge{}
	w := &oldestWaiting{src: src, gauge: g, leading: func() bool { return false }, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	w.cycle(context.Background())
	if src.reads != 0 {
		t.Errorf("follower read the store %d times, want 0", src.reads)
	}
	if len(g.sets) != 1 || g.sets[0] != nil {
		t.Fatalf("sets = %v, want one clearing set", g.sets)
	}
}

func TestOldestWaitingReadFailureClears(t *testing.T) {
	src := &fakeWaitingSource{err: errors.New("down")}
	g := &fakeWaitingGauge{}
	w := &oldestWaiting{src: src, gauge: g, leading: func() bool { return true }, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	w.cycle(context.Background())
	w.cycle(context.Background())
	if !w.failing {
		t.Error("failing not set after a read error")
	}
	for _, s := range g.sets {
		if s != nil {
			t.Fatalf("set %v after a read error, want a clear", s)
		}
	}
}
