package scheduler

import (
	"io"
	"log/slog"
	"testing"
	"time"
)

// TestStalledForOnlyReportsAStalledLeader is #539: only a leader whose loop
// has ticked and then stopped for longer than the limit is stalled.
func TestStalledForOnlyReportsAStalledLeader(t *testing.T) {
	s := NewScheduler(&fakeStore{}, slog.New(slog.NewTextHandler(io.Discard, nil)), 10*time.Millisecond)

	s.lastTick.Store(time.Now().Add(-time.Hour).UnixNano())
	if stalled, _ := s.StalledFor(time.Minute); stalled {
		t.Error("a follower with an old tick was reported stalled")
	}

	s.SetLeading(true) // resets the tick clock
	if stalled, _ := s.StalledFor(time.Minute); stalled {
		t.Error("a leader before its first tick was reported stalled")
	}

	s.lastTick.Store(time.Now().Add(-30 * time.Second).UnixNano())
	if stalled, _ := s.StalledFor(time.Minute); stalled {
		t.Error("a leader that ticked 30s ago was reported stalled with a 1m limit")
	}

	old := time.Now().Add(-2 * time.Minute)
	s.lastTick.Store(old.UnixNano())
	stalled, last := s.StalledFor(time.Minute)
	if !stalled {
		t.Error("a leader that last ticked 2m ago was not reported stalled with a 1m limit")
	}
	if !last.Equal(time.Unix(0, old.UnixNano()).UTC()) {
		t.Errorf("last tick = %v, want %v", last, old)
	}
}
