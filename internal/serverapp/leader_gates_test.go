package serverapp

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/scheduler"
)

type staleTerms struct{}

func (staleTerms) Term() int64                           { return 3 }
func (staleTerms) Current(context.Context) (bool, error) { return false, nil }

// TestLeaderGatesFollowLeaseFencing: with scheduler.lease_fencing on, the
// leader-only side effects close and the dispatch workers drop their pod
// creates once a newer term holds the lease, while the replica still
// believes it leads (ADR 0106, points 5 and 6); with it off, nothing reads
// the lease.
func TestLeaderGatesFollowLeaseFencing(t *testing.T) {
	sched := scheduler.NewScheduler(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Second)
	sched.SetLeaseTerms(staleTerms{})
	sched.SetLeading(true)

	cfg := &config.ServerConfig{}
	leading, check := leaderGates(cfg, sched)
	if check != nil || !leading() {
		t.Fatalf("fencing off: check set=%v leading=%v, want no check and the plain leadership", check != nil, leading())
	}
	cfg.Scheduler.LeaseFencing = true
	leading, check = leaderGates(cfg, sched)
	if check == nil || check(context.Background()) || leading() {
		t.Fatal("fencing on with a superseded term: the gates stayed open")
	}
}
